package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v1.7.0", true},
		{"1.7.0", true},
		{"v0.0.1", true},
		{"v10.20.30", true},

		{"dev", false},
		{"dev-abc1234", false},
		{"sha-abc1234", false},
		{"v1.7.0-3-gabc1234", false},
		{"v1.7.0-rc.1", false},
		{"", false},
		{"latest", false},
		{"v1.7", false},
		{"1.7.0.1", false},
	}
	for _, tc := range cases {
		if got := isReleaseVersion(tc.version); got != tc.want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// Top-9 buckets (8 visible + sha overflow tail) used by the pin tests below.
// "1.8.0" sits at index 8, beyond the maxBars=8 cutoff, so without pinning
// it would be folded into "(other)".
func chartFixture() []statsBucket {
	return []statsBucket{
		{"1.6.0", 34},
		{"sha-a4aeaf0", 24},
		{"sha-09ef045", 19},
		{"1.7.0", 11},
		{"sha-6a433d5", 10},
		{"sha-83faf3b", 10},
		{"sha-0c4544f", 4},
		{"sha-dd31a9f", 3},
		{"1.8.0", 1},
		{"sha-zzzzzzz", 1},
	}
}

func TestRenderBarChartPinsFreshRelease(t *testing.T) {
	html := renderBarChart(chartFixture(), 8, "1.8.0")
	if !strings.Contains(html, "1.8.0 (latest)") {
		t.Errorf("expected pinned row labelled `1.8.0 (latest)`, got:\n%s", html)
	}
	// Without the pin "1.8.0" would be inside (other)=2; the swap should
	// displace sha-dd31a9f (count=3) to the tail so (other) becomes 3+1=4.
	if !strings.Contains(html, `<td class="count-cell">4</td>`) {
		t.Errorf("expected (other) count 4 after swap; chart:\n%s", html)
	}
}

func TestRenderBarChartNoPinLabelKeepsLegacyBehaviour(t *testing.T) {
	html := renderBarChart(chartFixture(), 8, "")
	if strings.Contains(html, "(latest)") {
		t.Errorf("did not expect any (latest) annotation when pinLabel is empty:\n%s", html)
	}
	// (other) should be the natural tail sum: 1.8.0 (1) + sha-zzzzzzz (1) = 2.
	if !strings.Contains(html, `<td class="count-cell">2</td>`) {
		t.Errorf("expected (other) count 2 with no pin; chart:\n%s", html)
	}
}

func TestRenderBarChartPinAlreadyVisible(t *testing.T) {
	// 1.7.0 is at index 3 — already visible. Should be annotated but not moved
	// (no swap, no change to tail).
	html := renderBarChart(chartFixture(), 8, "1.7.0")
	if !strings.Contains(html, "1.7.0 (latest)") {
		t.Errorf("expected `1.7.0 (latest)` annotation when pinLabel is in head:\n%s", html)
	}
	if !strings.Contains(html, `<td class="count-cell">2</td>`) {
		t.Errorf("expected unchanged (other) count 2 when pin is already visible; chart:\n%s", html)
	}
}

func TestRenderBarChartPinMissingFromBuckets(t *testing.T) {
	// pinLabel that doesn't appear at all is a no-op (next release before
	// any install has reported it).
	html := renderBarChart(chartFixture(), 8, "1.9.0")
	if strings.Contains(html, "(latest)") {
		t.Errorf("did not expect (latest) annotation when pin is absent:\n%s", html)
	}
	if !strings.Contains(html, `<td class="count-cell">2</td>`) {
		t.Errorf("expected unchanged (other) count 2 when pin missing; chart:\n%s", html)
	}
}

func TestRenderBarChartDoesNotMutateInput(t *testing.T) {
	in := chartFixture()
	_ = renderBarChart(in, 8, "1.8.0")
	if in[7].Label != "sha-dd31a9f" || in[8].Label != "1.8.0" {
		t.Errorf("renderBarChart mutated caller's slice: %+v", in)
	}
}

// newTestServer spins up an in-memory SQLite DB with the installs schema
// matching the production migration, ready for handler tests.
func newTestServer(t *testing.T, latest string) *server {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Same aggregate-table DDL as production so tests can't drift from the
	// real schema. The installs CREATE differs (the deploy/features ALTERs
	// are folded in), so it stays inline.
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE installs (
		install_id  TEXT PRIMARY KEY,
		version     TEXT NOT NULL,
		os          TEXT NOT NULL,
		arch        TEXT NOT NULL,
		first_seen  DATETIME NOT NULL,
		last_seen   DATETIME NOT NULL,
		deploy      TEXT NOT NULL DEFAULT '',
		features    TEXT
	)`); err != nil {
		t.Fatalf("create installs: %v", err)
	}
	if err := createAggregateTables(context.Background(), db); err != nil {
		t.Fatalf("create aggregate tables: %v", err)
	}
	s := &server{db: db}
	s.setLatest(latest)
	return s
}

// stubGitHubLatest points githubAPIBase at a test server that serves
// releases/latest with the given tag (or a status code when tag is "").
func stubGitHubLatest(t *testing.T, repo, tag string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+repo+"/releases/latest" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
	}))
	t.Cleanup(srv.Close)
	prev := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = prev })
}

func TestFetchLatestRelease(t *testing.T) {
	stubGitHubLatest(t, "vavallee/bindery", "v1.22.1", http.StatusOK)
	got, err := fetchLatestRelease(context.Background(), "vavallee/bindery")
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if got != "v1.22.1" {
		t.Errorf("tag = %q, want v1.22.1", got)
	}
}

func TestFetchLatestRelease_Non200(t *testing.T) {
	stubGitHubLatest(t, "vavallee/bindery", "", http.StatusForbidden)
	if _, err := fetchLatestRelease(context.Background(), "vavallee/bindery"); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}

func TestRunLatestVersionLoop_UpdatesFromGitHub(t *testing.T) {
	stubGitHubLatest(t, "vavallee/bindery", "v1.22.1", http.StatusOK)
	s := newTestServer(t, "v1.0.0") // seeded from "env"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runLatestVersionLoop(ctx, "vavallee/bindery")
	waitFor(t, 2*time.Second, func() bool { return s.latest() == "v1.22.1" })
	if s.latest() != "v1.22.1" {
		t.Fatalf("latest = %q, want v1.22.1 (poller should override the seed)", s.latest())
	}
}

func TestRunLatestVersionLoop_KeepsSeedOnBadTag(t *testing.T) {
	stubGitHubLatest(t, "vavallee/bindery", "nightly", http.StatusOK) // not a release tag
	s := newTestServer(t, "v1.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runLatestVersionLoop(ctx, "vavallee/bindery")
	// Give the immediate check time to run and (correctly) reject the tag.
	time.Sleep(200 * time.Millisecond)
	if s.latest() != "v1.0.0" {
		t.Fatalf("latest = %q, want the seed v1.0.0 to be kept for a non-release tag", s.latest())
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleStatsJSON(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	now := time.Now().UTC()
	// Two recently-active installs and one stale (>30 days old) install.
	rows := []struct {
		id        string
		firstSeen time.Time
		lastSeen  time.Time
	}{
		{"11111111-1111-1111-1111-111111111111", now.Add(-40 * 24 * time.Hour), now.Add(-1 * time.Hour)},
		{"22222222-2222-2222-2222-222222222222", now.Add(-5 * 24 * time.Hour), now.Add(-2 * 24 * time.Hour)},
		{"33333333-3333-3333-3333-333333333333", now.Add(-90 * 24 * time.Hour), now.Add(-45 * 24 * time.Hour)}, // stale
	}
	for _, r := range rows {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
			 VALUES (?, '1.9.5', 'linux', 'amd64', ?, ?, 'docker')`,
			r.id, r.firstSeen, r.lastSeen); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/stats.json", nil)
	rec := httptest.NewRecorder()
	s.handleStatsJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got statsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.Active != 2 {
		t.Errorf("Active = %d, want 2", got.Active)
	}
	if got.Total != 3 {
		t.Errorf("Total = %d, want 3", got.Total)
	}
	if got.Latest != "v1.9.5" {
		t.Errorf("Latest = %q, want v1.9.5", got.Latest)
	}
}

// TestStatsTokenValid covers the constant time bearer check added for #2138.
// The behaviour must be identical to the plain != it replaced, so these cases
// pin the accept/reject boundary rather than trying to time the comparison.
func TestStatsTokenValid(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	s.statsToken = "s3cret"

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"exact match", "Bearer s3cret", true},
		{"wrong token", "Bearer nope", false},
		{"correct prefix only", "Bearer s3c", false},
		{"token without scheme", "s3cret", false},
		{"wrong scheme case", "bearer s3cret", false},
		{"trailing byte", "Bearer s3cretx", false},
		{"missing header", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if got := s.statsTokenValid(req); got != tc.want {
				t.Errorf("statsTokenValid(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}

	// An unconfigured token must never be satisfiable, including by a request
	// that sends the bare scheme and an empty secret.
	t.Run("unconfigured token rejects everything", func(t *testing.T) {
		s.statsToken = ""
		for _, header := range []string{"", "Bearer ", "Bearer "} {
			req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
			req.Header.Set("Authorization", header)
			if s.statsTokenValid(req) {
				t.Errorf("empty statsToken accepted header %q", header)
			}
		}
	})
}

func TestHandleBackup(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	s.statsToken = "secret"
	s.dbDir = t.TempDir() // stand-in for the writable data volume
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
		 VALUES ('11111111-1111-1111-1111-111111111111', '1.9.5', 'linux', 'amd64', ?, ?, 'docker')`,
		time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("insert: %v", err)
	}

	t.Run("ok", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.handleBackup(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		// A valid SQLite file begins with the literal header "SQLite format 3\0".
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte("SQLite format 3\x00")) {
			t.Errorf("response body is not a SQLite database")
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.sqlite3" {
			t.Errorf("Content-Type = %q, want application/vnd.sqlite3", ct)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
		req.Header.Set("Authorization", "Bearer nope")
		rec := httptest.NewRecorder()
		s.handleBackup(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

// insertInstall is a test helper that inserts a single install row with the
// given version, first_seen and last_seen.
func insertInstall(t *testing.T, s *server, id, version string, firstSeen, lastSeen time.Time) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
		 VALUES (?, ?, 'linux', 'amd64', ?, ?, 'docker')`,
		id, version, firstSeen, lastSeen); err != nil {
		t.Fatalf("insert install %s: %v", id, err)
	}
	insertActivity(t, s, id, version, lastSeen)
}

// insertActivity mirrors the ledger row handlePing writes alongside every
// installs upsert, attributing the install to its ping day.
func insertActivity(t *testing.T, s *server, id, version string, day time.Time) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO daily_activity (day, install_id, version) VALUES (?, ?, ?)
		 ON CONFLICT(day, install_id) DO UPDATE SET version = excluded.version`,
		day.UTC().Format("2006-01-02"), id, version); err != nil {
		t.Fatalf("insert activity %s: %v", id, err)
	}
}

// uuid builds a deterministic UUID v4 shaped string from a one-byte seed so
// tests can name their fixture rows readably ("v1" → "11...").
func uuid(seed byte) string {
	c := string([]byte{seed, seed, seed, seed, seed, seed, seed, seed})
	return c[:8] + "-" + c[:4] + "-" + c[:4] + "-" + c[:4] + "-" + c[:8] + c[:4]
}

// TestComputeStats_ActiveWindowsAreSeparate verifies the 7d and 30d counts
// scope to different windows. Mirrors the dashboard headline "Active 7d /
// Active 30d / Total" so we catch any future regression that conflates them.
func TestComputeStats_ActiveWindowsAreSeparate(t *testing.T) {
	s := newTestServer(t, "v1.15.2")
	now := time.Now().UTC()

	insertInstall(t, s, uuid('1'), "1.15.2", now.Add(-2*24*time.Hour), now.Add(-1*time.Hour))      // 7d + 30d
	insertInstall(t, s, uuid('2'), "1.15.1", now.Add(-10*24*time.Hour), now.Add(-3*24*time.Hour))  // 7d + 30d
	insertInstall(t, s, uuid('3'), "1.14.0", now.Add(-30*24*time.Hour), now.Add(-15*24*time.Hour)) // 30d only
	insertInstall(t, s, uuid('4'), "1.8.1", now.Add(-90*24*time.Hour), now.Add(-50*24*time.Hour))  // neither

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	if d.Active7d != 2 {
		t.Errorf("Active7d = %d, want 2 (last_seen 1h + 3d ago)", d.Active7d)
	}
	if d.Active30d != 3 {
		t.Errorf("Active30d = %d, want 3 (last_seen 1h + 3d + 15d ago)", d.Active30d)
	}
	if d.Total != 4 {
		t.Errorf("Total = %d, want 4 (all rows including the dormant one)", d.Total)
	}
}

// TestComputeStats_VersionsRecentBucketsBy7d verifies VersionsRecent counts
// fall in the 7-day cohort, matching the "Active 7d" column on the dashboard.
func TestComputeStats_VersionsRecentBucketsBy7d(t *testing.T) {
	s := newTestServer(t, "v1.15.2")
	now := time.Now().UTC()

	// Two v1.15.2 installs, one fresh, one too old for 7d.
	insertInstall(t, s, uuid('1'), "1.15.2", now.Add(-30*24*time.Hour), now.Add(-1*time.Hour))
	insertInstall(t, s, uuid('2'), "1.15.2", now.Add(-30*24*time.Hour), now.Add(-20*24*time.Hour))
	// One dormant v1.8.1 that should only show in the 30d bucket.
	insertInstall(t, s, uuid('3'), "1.8.1", now.Add(-30*24*time.Hour), now.Add(-25*24*time.Hour))

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}

	recent := bucketMap(d.VersionsRecent)
	if got := recent["1.15.2"]; got != 1 {
		t.Errorf("VersionsRecent[1.15.2] = %d, want 1", got)
	}
	if _, found := recent["1.8.1"]; found {
		t.Errorf("VersionsRecent must not include 1.8.1 (last_seen 25 days ago); got %v", recent)
	}

	all := bucketMap(d.Versions)
	if got := all["1.15.2"]; got != 2 {
		t.Errorf("Versions[1.15.2] = %d, want 2", got)
	}
	if got := all["1.8.1"]; got != 1 {
		t.Errorf("Versions[1.8.1] = %d, want 1", got)
	}
}

func bucketMap(bs []statsBucket) map[string]int {
	m := make(map[string]int, len(bs))
	for _, b := range bs {
		m[b.Label] = b.Count
	}
	return m
}

// TestComputeStats_LongevityYoungDB verifies the footnote flag fires when the
// DB has been collecting for less than 30 days, and clears once it has.
func TestComputeStats_LongevityYoungDB(t *testing.T) {
	now := time.Now().UTC()

	t.Run("young DB sets the flag", func(t *testing.T) {
		s := newTestServer(t, "v1.15.2")
		insertInstall(t, s, uuid('1'), "1.15.2", now.Add(-10*24*time.Hour), now.Add(-1*time.Hour))
		d, err := s.computeStats(context.Background())
		if err != nil {
			t.Fatalf("computeStats: %v", err)
		}
		if !d.LongevityYoungDB {
			t.Error("LongevityYoungDB = false, want true when earliest first_seen is 10d ago")
		}
	})

	t.Run("mature DB clears the flag", func(t *testing.T) {
		s := newTestServer(t, "v1.15.2")
		insertInstall(t, s, uuid('1'), "1.15.2", now.Add(-60*24*time.Hour), now.Add(-1*time.Hour))
		d, err := s.computeStats(context.Background())
		if err != nil {
			t.Fatalf("computeStats: %v", err)
		}
		if d.LongevityYoungDB {
			t.Error("LongevityYoungDB = true, want false when earliest first_seen is 60d ago")
		}
	})

	t.Run("empty DB does not set the flag", func(t *testing.T) {
		s := newTestServer(t, "v1.15.2")
		d, err := s.computeStats(context.Background())
		if err != nil {
			t.Fatalf("computeStats: %v", err)
		}
		if d.LongevityYoungDB {
			t.Error("LongevityYoungDB = true on empty DB; should be false (footnote only makes sense once data exists)")
		}
	})
}

// TestSweepStaleAndDev verifies the retention sweep drops rows older than the
// retention window and the legacy dev/test fixtures, and leaves real recent
// rows alone.
func TestSweepStaleAndDev(t *testing.T) {
	s := newTestServer(t, "v1.15.2")
	now := time.Now().UTC()

	insertInstall(t, s, uuid('1'), "1.15.2", now.Add(-2*24*time.Hour), now.Add(-1*time.Hour))      // keep
	insertInstall(t, s, uuid('2'), "1.15.1", now.Add(-90*24*time.Hour), now.Add(-65*24*time.Hour)) // drop (stale)
	insertInstall(t, s, uuid('3'), "dev", now.Add(-1*time.Hour), now.Add(-1*time.Hour))            // drop (dev version)
	// Non-UUID install_id (legacy): goes via raw SQL since insertInstall
	// would have rejected it through normal handlePing flow.
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
		 VALUES ('not-a-uuid', '1.15.1', 'linux', 'amd64', ?, ?, 'docker')`,
		now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	n, err := sweepStaleAndDev(context.Background(), s.db)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 3 {
		t.Errorf("rows deleted = %d, want 3", n)
	}

	var remaining int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM installs`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining rows = %d, want 1 (the fresh real install)", remaining)
	}
}

// TestRenderVersionsTableTwoCohorts verifies the rendered table includes both
// 7d and 30d count columns, the long tail collapses, and the latest version
// is pinned regardless of which cohort it sits in.
func TestRenderVersionsTableTwoCohorts(t *testing.T) {
	b30 := []statsBucket{
		{"1.15.2", 4},
		{"1.15.1", 46},
		{"1.14.2", 81},
		{"1.14.1", 96},
		{"1.8.1", 133},
		{"1.11.2", 26},
		{"1.14.0", 21},
		{"1.12.0", 20},
		{"1.12.1", 15},
	}
	b7 := []statsBucket{
		{"1.15.2", 4},
		{"1.15.1", 46},
		{"1.14.2", 81},
		{"1.14.1", 96},
		{"1.8.1", 18},
	}
	html := renderVersionsTable(b30, b7, 8, "1.15.2")

	for _, want := range []string{"1.15.2 (latest)", ">7d<", ">30d<", ">18<", ">133<", `class="count-cell">0<`} {
		if !strings.Contains(html, want) {
			t.Errorf("expected output to contain %q, got: %s", want, html)
		}
	}
}

// TestRenderLongevityFootnote verifies the footnote renders iff the DB is
// young, and that the chart itself still appears either way.
func TestRenderLongevityFootnote(t *testing.T) {
	buckets := []statsBucket{
		{"< 1 week", 50},
		{"1–4 weeks", 30},
	}
	withFootnote := renderLongevity(buckets, true)
	withoutFootnote := renderLongevity(buckets, false)

	if !strings.Contains(withFootnote, "cannot populate yet") {
		t.Errorf("expected footnote text when youngDB=true; got: %s", withFootnote)
	}
	if strings.Contains(withoutFootnote, "cannot populate yet") {
		t.Errorf("did not expect footnote text when youngDB=false; got: %s", withoutFootnote)
	}
	// Chart itself appears in both paths (HTML-escaped angle bracket).
	for _, html := range []string{withFootnote, withoutFootnote} {
		if !strings.Contains(html, "&lt; 1 week") {
			t.Errorf("expected bucket label in output; got: %s", html)
		}
	}
}

// insertInstallWithFeatures inserts a row with a serialized features JSON
// payload. The features arg is marshalled here so callers can pass a literal
// struct without dealing with json.Marshal.
func insertInstallWithFeatures(t *testing.T, s *server, id, version string, firstSeen, lastSeen time.Time, features any) {
	t.Helper()
	var featuresJSON sql.NullString
	if features != nil {
		buf, err := json.Marshal(features)
		if err != nil {
			t.Fatalf("marshal features: %v", err)
		}
		featuresJSON = sql.NullString{String: string(buf), Valid: true}
	}
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy, features)
		 VALUES (?, ?, 'linux', 'amd64', ?, ?, 'docker', ?)`,
		id, version, firstSeen, lastSeen, featuresJSON); err != nil {
		t.Fatalf("insert install %s: %v", id, err)
	}
	insertActivity(t, s, id, version, lastSeen)
}

// TestHandlePing_StoresFeatures verifies a ping with a features payload
// persists it as JSON in the installs.features column, and that a ping
// without features stores NULL.
func TestHandlePing_StoresFeatures(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	s.limiter = newRateLimiter(time.Hour, time.Minute)

	t.Run("with features", func(t *testing.T) {
		body := pingRequest{
			InstallID: "11111111-1111-1111-1111-111111111111",
			Version:   "1.15.3",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
			Features: &featuresPayload{
				Indexers:       ptr(2),
				CalibreEnabled: ptr(true),
			},
		}
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/ping", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.1:1234" // bypasses rate limit (unique IP)
		rec := httptest.NewRecorder()
		s.handlePing(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}

		var stored sql.NullString
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT features FROM installs WHERE install_id = ?`, body.InstallID,
		).Scan(&stored); err != nil {
			t.Fatalf("read back features: %v", err)
		}
		if !stored.Valid {
			t.Fatal("features column is NULL; expected JSON payload")
		}
		if !strings.Contains(stored.String, `"indexers":2`) {
			t.Errorf("stored features missing indexers:2; got: %s", stored.String)
		}
		if !strings.Contains(stored.String, `"calibre_enabled":true`) {
			t.Errorf("stored features missing calibre_enabled:true; got: %s", stored.String)
		}
	})

	t.Run("without features", func(t *testing.T) {
		body := pingRequest{
			InstallID: "22222222-2222-2222-2222-222222222222",
			Version:   "1.15.2",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
		}
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/ping", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.2:1234"
		rec := httptest.NewRecorder()
		s.handlePing(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}

		var stored sql.NullString
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT features FROM installs WHERE install_id = ?`, body.InstallID,
		).Scan(&stored); err != nil {
			t.Fatalf("read back features: %v", err)
		}
		if stored.Valid {
			t.Errorf("features column should be NULL for legacy payload; got: %s", stored.String)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// TestComputeFeatureAdoption verifies the aggregated counts: denominator is
// the count of 7d-active installs with non-NULL features, numerator per
// field is the count of installs whose features payload contains a truthy
// (non-zero / true) value for that field. Older-client rows (NULL features)
// don't contribute to either side.
func TestComputeFeatureAdoption(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Now().UTC()

	// Three reporting installs, two with calibre on, one without.
	insertInstallWithFeatures(t, s, uuid('1'), "1.15.3", now.Add(-1*time.Hour), now.Add(-1*time.Hour),
		map[string]any{"indexers": 2, "calibre_enabled": true})
	insertInstallWithFeatures(t, s, uuid('2'), "1.15.3", now.Add(-2*time.Hour), now.Add(-2*time.Hour),
		map[string]any{"indexers": 1, "calibre_enabled": true, "multi_user": true})
	insertInstallWithFeatures(t, s, uuid('3'), "1.15.3", now.Add(-3*time.Hour), now.Add(-3*time.Hour),
		map[string]any{"indexers": 0}) // explicit zero should not count

	// One older client with no features payload at all.
	insertInstall(t, s, uuid('4'), "1.15.2", now.Add(-4*time.Hour), now.Add(-4*time.Hour))

	// One install that pinged outside the 7d window; ignored entirely.
	insertInstallWithFeatures(t, s, uuid('5'), "1.15.3", now.Add(-20*24*time.Hour), now.Add(-10*24*time.Hour),
		map[string]any{"calibre_enabled": true})

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	if d.FeaturesReporting != 3 {
		t.Errorf("FeaturesReporting = %d, want 3 (three 7d-active installs with features)", d.FeaturesReporting)
	}

	got := bucketMap(d.Features)
	if got["Indexers configured"] != 2 {
		t.Errorf("Indexers configured = %d, want 2", got["Indexers configured"])
	}
	if got["Calibre integration"] != 2 {
		t.Errorf("Calibre integration = %d, want 2", got["Calibre integration"])
	}
	if got["Multi-user"] != 1 {
		t.Errorf("Multi-user = %d, want 1", got["Multi-user"])
	}
	if got["Audiobookshelf integration"] != 0 {
		t.Errorf("Audiobookshelf integration = %d, want 0 (no install has abs_enabled)", got["Audiobookshelf integration"])
	}
}

// TestRenderFeatures_NoData verifies the empty-state message renders when
// no install has reported features yet (typical immediately after the
// telemetry-server upgrade but before any v1.15.3+ client has pinged).
func TestRenderFeatures_NoData(t *testing.T) {
	html := renderFeatures(nil, 0)
	if !strings.Contains(html, "No features data yet") {
		t.Errorf("expected empty-state message; got: %s", html)
	}
}

// TestRenderFeatures_WithData verifies the header includes the install
// count and the bar chart appears.
func TestRenderFeatures_WithData(t *testing.T) {
	html := renderFeatures([]statsBucket{
		{"Indexers configured", 14},
		{"Calibre integration", 6},
	}, 20)
	for _, want := range []string{"Out of 20 installs reporting", "Indexers configured", "Calibre integration", ">14<", ">6<"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected output to contain %q; got: %s", want, html)
		}
	}
	// Singular "install" agreement when reporting=1.
	if !strings.Contains(renderFeatures([]statsBucket{{"X", 1}}, 1), "Out of 1 install reporting") {
		t.Errorf("expected singular form when reporting=1")
	}
}

// TestHandleTelemetryFields verifies the public schema doc renders and
// includes the core wire fields a privacy-conscious user would want to
// audit (install_id, version, deploy) plus the opt-out instructions.
func TestHandleTelemetryFields(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	req := httptest.NewRequest(http.MethodGet, "/telemetry-fields", nil)
	rec := httptest.NewRecorder()
	s.handleTelemetryFields(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"install_id", "version", "deploy", "features",
		"BINDERY_TELEMETRY_DISABLED",
		"telemetry.enabled",
		"What we don't collect",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected /telemetry-fields to contain %q", want)
		}
	}
}

// TestSnapshotDay verifies that a single call to snapshotDay populates all
// three aggregate tables correctly for a target day with mixed data:
// installs pinged on that day, installs that pinged a different day, and
// installs that reported features payloads.
func TestSnapshotDay(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	target := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC) // snapshot yesterday

	// Three installs pinged on 2026-05-27: two v1.15.2, one v1.15.1
	// (two with features, one of those with calibre on).
	insertInstallWithFeatures(t, s, uuid('1'), "1.15.2",
		now.AddDate(0, 0, -10), time.Date(2026, 5, 27, 8, 0, 0, 0, time.UTC),
		map[string]any{"indexers": 2, "calibre_enabled": true})
	insertInstallWithFeatures(t, s, uuid('2'), "1.15.2",
		now.AddDate(0, 0, -8), time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC),
		map[string]any{"indexers": 1})
	insertInstall(t, s, uuid('3'), "1.15.1",
		now.AddDate(0, 0, -5), time.Date(2026, 5, 27, 20, 0, 0, 0, time.UTC))

	// One install pinged on a different day; must not contribute to the
	// 2026-05-27 snapshot.
	insertInstall(t, s, uuid('4'), "1.14.0",
		now.AddDate(0, 0, -20), time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC))

	// A fifth install that was first_seen on 2026-05-27 but pinged later;
	// should show up in new_installs even though it's not in active_day.
	insertInstall(t, s, uuid('5'), "1.15.3",
		time.Date(2026, 5, 27, 6, 0, 0, 0, time.UTC), now.Add(-1*time.Hour))

	if err := s.snapshotDay(context.Background(), target); err != nil {
		t.Fatalf("snapshotDay: %v", err)
	}

	// daily_global row for 2026-05-27.
	var activeDay, newInstalls, total int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT active_day, new_installs, total FROM daily_global WHERE day = ?`,
		"2026-05-27",
	).Scan(&activeDay, &newInstalls, &total); err != nil {
		t.Fatalf("read daily_global: %v", err)
	}
	if activeDay != 3 {
		t.Errorf("active_day = %d, want 3 (two v1.15.2 + one v1.15.1)", activeDay)
	}
	if newInstalls != 1 {
		t.Errorf("new_installs = %d, want 1 (the v1.15.3 install with first_seen=2026-05-27)", newInstalls)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5 (all rows)", total)
	}

	// daily_version rows for 2026-05-27.
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT version, active_count FROM daily_version WHERE day = ? ORDER BY version`,
		"2026-05-27")
	if err != nil {
		t.Fatalf("read daily_version: %v", err)
	}
	defer rows.Close()
	versionCounts := map[string]int{}
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			t.Fatalf("scan version row: %v", err)
		}
		versionCounts[v] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate version rows: %v", err)
	}
	if versionCounts["1.15.2"] != 2 || versionCounts["1.15.1"] != 1 {
		t.Errorf("daily_version[2026-05-27] = %v, want {1.15.2: 2, 1.15.1: 1}", versionCounts)
	}
	if _, present := versionCounts["1.14.0"]; present {
		t.Errorf("1.14.0 should not appear (last_seen was 2026-05-25)")
	}

	// daily_features rows for 2026-05-27.
	featRows, err := s.db.QueryContext(context.Background(),
		`SELECT field, enabled_count, reporting_count FROM daily_features WHERE day = ?`,
		"2026-05-27")
	if err != nil {
		t.Fatalf("read daily_features: %v", err)
	}
	defer featRows.Close()
	enabled := map[string]int{}
	var reporting int
	for featRows.Next() {
		var f string
		var e, r int
		if err := featRows.Scan(&f, &e, &r); err != nil {
			t.Fatalf("scan feature row: %v", err)
		}
		enabled[f] = e
		reporting = r // same for every row
	}
	if err := featRows.Err(); err != nil {
		t.Fatalf("iterate feature rows: %v", err)
	}
	if reporting != 2 {
		t.Errorf("reporting_count = %d, want 2 (two installs reported features)", reporting)
	}
	if enabled["indexers"] != 2 {
		t.Errorf("indexers enabled = %d, want 2 (both reporting installs have indexers > 0)", enabled["indexers"])
	}
	if enabled["calibre_enabled"] != 1 {
		t.Errorf("calibre_enabled = %d, want 1 (one install has calibre on)", enabled["calibre_enabled"])
	}
}

// TestSnapshotDayReplaceVersionRows verifies that re-snapshotting a day
// drops version rows whose count went to zero. Without this guard a
// version that was active on day N but not on day N+1's re-snapshot would
// be left at its day-N count, exaggerating reach in trend charts.
func TestSnapshotDayReplaceVersionRows(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	target := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)

	// First snapshot: one install on 1.14.0.
	insertInstall(t, s, uuid('1'), "1.14.0",
		target.AddDate(0, 0, -10), time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC))
	if err := s.snapshotDay(context.Background(), target); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	// Move the install to 1.15.2 (both the installs row and its ledger row
	// for the day, as a real upgrade ping would); re-snapshot. The 1.14.0
	// row from the first snapshot must disappear.
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE installs SET version = '1.15.2' WHERE install_id = ?`, uuid('1'),
	); err != nil {
		t.Fatalf("update version: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE daily_activity SET version = '1.15.2' WHERE install_id = ?`, uuid('1'),
	); err != nil {
		t.Fatalf("update ledger version: %v", err)
	}
	if err := s.snapshotDay(context.Background(), target); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	var rows int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_version WHERE day = ? AND version = '1.14.0'`,
		"2026-05-27",
	).Scan(&rows); err != nil {
		t.Fatalf("query 1.14.0 rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("expected 1.14.0 row to be cleared on re-snapshot; got %d rows", rows)
	}

	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_version WHERE day = ? AND version = '1.15.2'`,
		"2026-05-27",
	).Scan(&rows); err != nil {
		t.Fatalf("query 1.15.2 rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1.15.2 row to exist after re-snapshot; got %d rows", rows)
	}
}

// TestBackfillNewInstalls verifies the startup backfill creates
// daily_global rows for every distinct first_seen day, with the correct
// counts; idempotent across multiple calls; safe to run alongside an
// already-populated table (preserves active_day, total).
func TestBackfillNewInstalls(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	d1 := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 5, 27, 11, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	insertInstall(t, s, uuid('1'), "1.15.2", d1, now)
	insertInstall(t, s, uuid('2'), "1.15.2", d1, now)
	insertInstall(t, s, uuid('3'), "1.15.1", d2, now)

	// Pre-seed daily_global for 2026-05-26 with non-zero active_day; the
	// backfill must update new_installs without clobbering active_day.
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO daily_global (day, active_day, new_installs, total)
		 VALUES ('2026-05-26', 99, 0, 999)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.backfillNewInstalls(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var active, newCount, total int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT active_day, new_installs, total FROM daily_global WHERE day = '2026-05-26'`,
	).Scan(&active, &newCount, &total); err != nil {
		t.Fatalf("read 2026-05-26: %v", err)
	}
	if newCount != 2 {
		t.Errorf("new_installs[2026-05-26] = %d, want 2", newCount)
	}
	if active != 99 {
		t.Errorf("active_day must be preserved across backfill; got %d, want 99", active)
	}
	if total != 999 {
		t.Errorf("total must be preserved across backfill; got %d, want 999", total)
	}

	if err := s.db.QueryRowContext(context.Background(),
		`SELECT new_installs FROM daily_global WHERE day = '2026-05-27'`,
	).Scan(&newCount); err != nil {
		t.Fatalf("read 2026-05-27: %v", err)
	}
	if newCount != 1 {
		t.Errorf("new_installs[2026-05-27] = %d, want 1", newCount)
	}

	// Idempotency: second call must leave state identical.
	if err := s.backfillNewInstalls(context.Background()); err != nil {
		t.Fatalf("backfill twice: %v", err)
	}
	var seen int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_global`,
	).Scan(&seen); err != nil {
		t.Fatalf("count: %v", err)
	}
	if seen != 2 {
		t.Errorf("daily_global row count after second backfill = %d, want 2", seen)
	}
}

// TestComputeStats_MonthlyReadsFromAggregates verifies the monthly new
// installs chart sources data from daily_global, not from the installs
// table directly. Once raw rows expire from the 60-day retention window
// the monthly chart should still show their contribution to history.
func TestComputeStats_MonthlyReadsFromAggregates(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Now().UTC()

	// Seed daily_global with a row that has no corresponding installs row
	// (simulating data from before the retention window).
	pastMonth := now.AddDate(0, -3, 0).Format("2006-01-02")
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO daily_global (day, active_day, new_installs, total)
		 VALUES (?, 0, 42, 0)`, pastMonth); err != nil {
		t.Fatalf("seed past month: %v", err)
	}

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}

	monthLabel := now.AddDate(0, -3, 0).Format("Jan '06")
	var got int
	for _, b := range d.Monthly {
		if b.Label == monthLabel {
			got = b.Count
			break
		}
	}
	if got != 42 {
		t.Errorf("Monthly[%s] = %d, want 42 (from daily_global with no raw rows)", monthLabel, got)
	}
}

// TestBackfillNewInstalls_Monotonic is the regression test for the erosion
// bug: once installs first-seen on a day age past the 60-day retention window
// and are swept, recomputing that day's new_installs from surviving rows
// yields a smaller count. The MAX-guarded conflict clause must refuse to
// lower a day's already-recorded value.
func TestBackfillNewInstalls_Monotonic(t *testing.T) {
	s := newTestServer(t, "v1.20.0")
	day := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	// The true count for 2026-05-05 was 5, recorded back when the day was
	// fresh. Simulate that historical record.
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO daily_global (day, active_day, new_installs, total)
		 VALUES ('2026-05-05', 0, 5, 0)`); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	// Only one install first-seen that day has survived the retention sweep;
	// the other four rows are long gone.
	insertInstall(t, s, uuid('1'), "1.20.0", day, now)

	if err := s.backfillNewInstalls(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var got int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT new_installs FROM daily_global WHERE day = '2026-05-05'`,
	).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != 5 {
		t.Errorf("new_installs eroded to %d; MAX guard must keep the recorded 5", got)
	}
}

// TestComputeStats_Cumulative verifies the cumulative headline sums
// daily_global.new_installs across all days, independent of the raw installs
// table (which the retention sweep bounds to 60 days).
func TestComputeStats_Cumulative(t *testing.T) {
	s := newTestServer(t, "v1.25.0")
	for _, r := range []struct {
		day string
		n   int
	}{
		{"2026-05-05", 5},
		{"2026-05-06", 3},
		{"2026-06-01", 10},
		{"2026-07-01", 2},
	} {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO daily_global (day, active_day, new_installs, total) VALUES (?, 0, ?, 0)`,
			r.day, r.n); err != nil {
			t.Fatalf("seed %s: %v", r.day, err)
		}
	}

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	if d.Cumulative != 20 {
		t.Errorf("Cumulative = %d, want 20 (5+3+10+2)", d.Cumulative)
	}
	if d.Total != 0 {
		t.Errorf("Total = %d, want 0 (no live installs rows); cumulative must not read the installs table", d.Total)
	}
}

// TestReconcileNewInstallsSeed verifies the one-time repair hook: it raises
// eroded/missing days to the seeded value, never lowers a healthy day, skips
// malformed keys, and is idempotent.
func TestReconcileNewInstallsSeed(t *testing.T) {
	s := newTestServer(t, "v1.25.0")

	// Existing state: one eroded day (below seed) and one healthy day (above seed).
	for _, r := range []struct {
		day string
		n   int
	}{
		{"2026-05-05", 1},  // eroded — seed says 5, should be raised
		{"2026-06-01", 20}, // healthy — seed says 10, must NOT be lowered
	} {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO daily_global (day, active_day, new_installs, total) VALUES (?, 0, ?, 0)`,
			r.day, r.n); err != nil {
			t.Fatalf("seed %s: %v", r.day, err)
		}
	}

	seed := map[string]int{
		"2026-05-05": 5,  // raise 1 -> 5
		"2026-05-20": 7,  // insert brand-new day
		"2026-06-01": 10, // must not lower 20
		"garbage":    99, // must be skipped
	}
	buf, _ := json.Marshal(seed)
	path := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	raised, err := s.reconcileNewInstallsSeed(context.Background(), path)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if raised != 2 {
		t.Errorf("raised = %d, want 2 (2026-05-05 up, 2026-05-20 inserted)", raised)
	}

	want := map[string]int{"2026-05-05": 5, "2026-05-20": 7, "2026-06-01": 20}
	for day, exp := range want {
		var got int
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT new_installs FROM daily_global WHERE day = ?`, day,
		).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", day, err)
		}
		if got != exp {
			t.Errorf("new_installs[%s] = %d, want %d", day, got, exp)
		}
	}
	// The malformed key must not have created a row.
	var junk int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_global WHERE day = 'garbage'`,
	).Scan(&junk); err != nil {
		t.Fatalf("count junk: %v", err)
	}
	if junk != 0 {
		t.Errorf("malformed seed key created %d rows, want 0", junk)
	}

	// Idempotent: re-running raises nothing.
	raised2, err := s.reconcileNewInstallsSeed(context.Background(), path)
	if err != nil {
		t.Fatalf("reconcile twice: %v", err)
	}
	if raised2 != 0 {
		t.Errorf("second reconcile raised = %d, want 0 (idempotent)", raised2)
	}
}

// TestSqliteDSN_PragmasApply opens a real file-backed DB with the
// production DSN and asserts the pragmas took effect. This is the
// regression test for the silent-DSN bug: the old mattn-style params
// (`_journal=WAL&_timeout=5000`) were ignored by modernc.org/sqlite, so
// journal_mode stayed DELETE and busy_timeout stayed 0 — invisible to any
// test that opened ":memory:" without the production DSN.
func TestSqliteDSN_PragmasApply(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(t.TempDir()+"/t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var journal string
	if err := db.QueryRowContext(context.Background(),
		`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var timeout int
	if err := db.QueryRowContext(context.Background(),
		`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}
}

// TestComputeStats_DailyCountsEveryActiveDay is the regression test for
// the daily-activity chart: an install that pings on two consecutive days
// must count toward BOTH days. The old query grouped installs by
// substr(last_seen,1,10), which attributed each install only to its most
// recent ping day — historical days degraded into "installs that went
// dormant that day" and the newest days absorbed everyone else, rendering
// as spurious spikes.
func TestComputeStats_DailyCountsEveryActiveDay(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)

	// installs row carries only the latest state (last_seen = today) …
	insertInstall(t, s, uuid('1'), "1.15.3", now.AddDate(0, 0, -10), now)
	// … but the ledger remembers yesterday's activity too.
	insertActivity(t, s, uuid('1'), "1.15.3", yesterday)

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	got := map[string]int{}
	for _, b := range d.Daily {
		got[b.Day.Format("2006-01-02")] = b.Count
	}
	for _, day := range []string{
		yesterday.Format("2006-01-02"),
		now.Format("2006-01-02"),
	} {
		if got[day] != 1 {
			t.Errorf("daily[%s] = %d, want 1 (install was active both days)", day, got[day])
		}
	}

	// Same property for the stacked version trend.
	verGot := map[string]int{}
	for _, vd := range d.VersionTrend {
		verGot[vd.Day.Format("2006-01-02")] = vd.Versions["1.15.3"]
	}
	for _, day := range []string{
		yesterday.Format("2006-01-02"),
		now.Format("2006-01-02"),
	} {
		if verGot[day] != 1 {
			t.Errorf("versionTrend[%s][1.15.3] = %d, want 1", day, verGot[day])
		}
	}
}

// TestHandlePing_WritesLedger verifies the ping handler writes the
// (day, install) ledger row alongside the installs upsert, and that a
// second same-day ping updates the ledger row's version in place instead
// of duplicating it.
func TestHandlePing_WritesLedger(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	s.limiter = newRateLimiter(time.Hour, time.Minute)

	ping := func(version, remoteAddr string) {
		t.Helper()
		body, _ := json.Marshal(pingRequest{
			InstallID: "11111111-1111-1111-1111-111111111111",
			Version:   version, OS: "linux", Arch: "amd64",
		})
		r := httptest.NewRequest(http.MethodPost, "/api/ping", bytes.NewReader(body))
		r.RemoteAddr = remoteAddr // distinct IPs bypass the per-IP rate limit
		w := httptest.NewRecorder()
		s.handlePing(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("ping status = %d, body = %s", w.Code, w.Body.String())
		}
	}
	ping("1.15.2", "192.0.2.10:1234")
	ping("1.15.3", "192.0.2.11:1234") // same-day upgrade

	today := time.Now().UTC().Format("2006-01-02")
	var n int
	var version string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*), MAX(version) FROM daily_activity WHERE day = ?`, today,
	).Scan(&n, &version); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if n != 1 {
		t.Errorf("ledger rows for today = %d, want 1 (same-day pings collapse)", n)
	}
	if version != "1.15.3" {
		t.Errorf("ledger version = %q, want 1.15.3 (last ping of the day wins)", version)
	}
}

// TestSeedActivityLedger verifies the one-time migration: an empty ledger
// is seeded from installs.last_seen and the epoch is recorded; a second
// call is a no-op (idempotent across restarts).
func TestSeedActivityLedger(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Now().UTC()
	// Bypass insertInstall — pre-migration DBs have no ledger rows.
	for _, id := range []byte{'1', '2'} {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
			 VALUES (?, '1.15.2', 'linux', 'amd64', ?, ?, 'docker')`,
			uuid(id), now.AddDate(0, 0, -10), now); err != nil {
			t.Fatalf("insert install: %v", err)
		}
	}

	if err := seedActivityLedger(context.Background(), s.db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_activity`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("ledger rows after seed = %d, want 2", n)
	}
	var epoch string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT value FROM meta WHERE key = ?`, ledgerEpochKey).Scan(&epoch); err != nil {
		t.Fatalf("epoch: %v", err)
	}
	if epoch != now.Format("2006-01-02") {
		t.Errorf("epoch = %q, want %q", epoch, now.Format("2006-01-02"))
	}

	// Second call: no-op, no duplicate rows, epoch unchanged.
	if err := seedActivityLedger(context.Background(), s.db); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM daily_activity`).Scan(&n); err != nil {
		t.Fatalf("recount: %v", err)
	}
	if n != 2 {
		t.Errorf("ledger rows after re-seed = %d, want 2 (idempotent)", n)
	}
}

// TestSweepStaleAndDev_LedgerRetention verifies ledger rows age out on the
// 400-day ledger clock while recent rows survive the 60-day installs sweep.
func TestSweepStaleAndDev_LedgerRetention(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	now := time.Now().UTC()
	insertActivity(t, s, uuid('1'), "1.15.2", now.AddDate(0, 0, -401)) // past ledger retention
	insertActivity(t, s, uuid('2'), "1.15.2", now.AddDate(0, 0, -100)) // inside it (but past installs')
	insertActivity(t, s, uuid('3'), "1.15.3", now)

	if _, err := sweepStaleAndDev(context.Background(), s.db); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT install_id FROM daily_activity ORDER BY day`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var kept []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		kept = append(kept, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iter: %v", err)
	}
	want := []string{uuid('2'), uuid('3')}
	if len(kept) != 2 || kept[0] != want[0] || kept[1] != want[1] {
		t.Errorf("kept = %v, want %v", kept, want)
	}
}

// TestLoadLedgerBackfill covers the historical-recovery path: importing a
// gzipped (day, install_id, version) CSV populates the ledger, re-derives
// active_day + daily_version for the touched days without disturbing
// new_installs/total, lowers the epoch, and is a no-op on re-run.
func TestLoadLedgerBackfill(t *testing.T) {
	s := newTestServer(t, "v1.15.3")
	ctx := context.Background()

	// Pre-existing state: a (wrong, undercounted) aggregate row for the
	// backfill day with historical new_installs/total that must survive,
	// an epoch later than the backfill span, and one ledger row that the
	// import must not duplicate.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO daily_global (day, active_day, new_installs, total) VALUES ('2026-06-01', 7, 12, 300)`,
	); err != nil {
		t.Fatalf("seed daily_global: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, '2026-08-01')`, ledgerEpochKey,
	); err != nil {
		t.Fatalf("seed epoch: %v", err)
	}
	insertActivity(t, s, uuid('1'), "1.15.2", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	// uuid('1') on 2026-06-01 duplicates the existing ledger row; the
	// malformed line must be skipped without failing the import.
	for _, line := range []string{
		"2026-06-01," + uuid('1') + ",1.15.2",
		"2026-06-01," + uuid('2') + ",1.15.2",
		"2026-06-01," + uuid('3') + ",1.15.1",
		"2026-06-02," + uuid('2') + ",1.15.2",
		"not-a-day,junk,1.15.2",
	} {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backfill.csv.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write backfill: %v", err)
	}

	n, err := s.loadLedgerBackfill(ctx, path)
	if err != nil {
		t.Fatalf("loadLedgerBackfill: %v", err)
	}
	if n != 3 {
		t.Errorf("rows inserted = %d, want 3 (1 dup ignored, 1 malformed skipped)", n)
	}

	var activeDay, newInstalls, total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT active_day, new_installs, total FROM daily_global WHERE day = '2026-06-01'`,
	).Scan(&activeDay, &newInstalls, &total); err != nil {
		t.Fatalf("read daily_global: %v", err)
	}
	if activeDay != 3 {
		t.Errorf("active_day = %d, want 3 (re-derived from ledger)", activeDay)
	}
	if newInstalls != 12 || total != 300 {
		t.Errorf("new_installs/total = %d/%d, want 12/300 (historical values preserved)", newInstalls, total)
	}

	var verCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT active_count FROM daily_version WHERE day = '2026-06-01' AND version = '1.15.2'`,
	).Scan(&verCount); err != nil {
		t.Fatalf("read daily_version: %v", err)
	}
	if verCount != 2 {
		t.Errorf("daily_version[1.15.2] = %d, want 2", verCount)
	}

	var epoch string
	if err := s.db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, ledgerEpochKey).Scan(&epoch); err != nil {
		t.Fatalf("read epoch: %v", err)
	}
	if epoch != "2026-06-01" {
		t.Errorf("epoch = %q, want 2026-06-01 (lowered to earliest backfill day)", epoch)
	}

	// Second run with the same file: done-marker short-circuits.
	n2, err := s.loadLedgerBackfill(ctx, path)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if n2 != 0 {
		t.Errorf("re-run inserted = %d, want 0 (done marker)", n2)
	}
}

func TestFunnelCapable(t *testing.T) {
	for v, want := range map[string]bool{
		// The boundary is a PATCH bump (1.30.1), so the pair that matters
		// most is 1.30.0/1.30.1 — a major/minor-only comparison would let
		// the whole 1.30.x line in and pollute the cohort with installs
		// that cannot report funnel fields at all.
		"1.30.1": true, "1.30.2": true, "1.31.0": true, "1.32.0": true, "2.0.0": true,
		"1.30.0": false, "1.29.9": false, "1.29.1": false, "0.99.9": false,
		"sha-abc": false, "dev": false, "": false,
		// The Docker image reports a v-prefixed version (ci.yml builds it from
		// `git describe --tags --match 'v*'`), while the GoReleaser binaries
		// report the bare form. Both must land in the same cohort; requiring
		// the bare form excluded every Docker install.
		"v1.30.1": true, "v1.30.2": true, "v1.31.0": true, "v2.0.0": true,
		"v1.30.0": false, "v1.29.9": false,
		// Still not a release version just because it starts with a v.
		"vdev": false, "v1.30": false, "1.30.1-rc1": false,
	} {
		if got := funnelCapable(v); got != want {
			t.Errorf("funnelCapable(%q) = %v, want %v", v, got, want)
		}
	}
}

// TestComputeSetupFunnel exercises the per-column denominators: an install
// too young for a window stays out of that window's denominator, funnel-
// incapable versions stay out entirely, and day offsets bucket correctly.
func TestComputeSetupFunnel(t *testing.T) {
	s := newTestServer(t, "v1.30.1")
	ctx := context.Background()
	now := time.Now().UTC()

	insert := func(id byte, version string, ageDays int, features string) {
		t.Helper()
		var f any
		if features != "" {
			f = features
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO installs (install_id, version, os, arch, deploy, features, first_seen, last_seen)
			VALUES (?, ?, 'linux', 'amd64', 'docker', ?, ?, ?)`,
			uuid(id), version, f, now.AddDate(0, 0, -ageDays), now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// 10 days old, full same-day setup → counts in D1, D7, ever.
	insert('1', "1.30.1", 10, `{"setup_indexer_day":0,"setup_client_day":0,"first_grab_day":2}`)
	// 10 days old, never configured anything → in all denominators, no numerators.
	insert('2', "1.30.1", 10, `{}`)
	// 3 days old, indexer on day 2 → in D1 denominator (missed), NOT in D7 denominator.
	insert('3', "1.31.0", 3, `{"setup_indexer_day":2}`)
	// funnel-incapable version → excluded from the cohort entirely.
	insert('4', "1.30.0", 10, `{"indexers":3}`)

	d := &statsData{}
	if err := s.computeSetupFunnel(ctx, now.AddDate(0, 0, -30), d); err != nil {
		t.Fatalf("computeSetupFunnel: %v", err)
	}
	if len(d.Funnel) != 5 {
		t.Fatalf("stages = %d, want 5", len(d.Funnel))
	}
	idx := d.Funnel[0] // Indexer configured
	if idx.Cohort != 3 {
		t.Errorf("cohort = %d, want 3 (1.30.0 install excluded)", idx.Cohort)
	}
	if idx.D1Den != 3 || idx.D1Num != 1 {
		t.Errorf("D1 = %d/%d, want 1/3 (only install 1 by day 1)", idx.D1Num, idx.D1Den)
	}
	if idx.D7Den != 2 || idx.D7Num != 1 {
		t.Errorf("D7 = %d/%d, want 1/2 (3-day-old install not in D7 window)", idx.D7Num, idx.D7Den)
	}
	if idx.Ever != 2 {
		t.Errorf("ever = %d, want 2 (installs 1 and 3)", idx.Ever)
	}
	grab := d.Funnel[3] // First grab
	if grab.D1Num != 0 || grab.D7Num != 1 {
		t.Errorf("grab D1/D7 = %d/%d, want 0 and 1 (day-2 grab misses D1)", grab.D1Num, grab.D7Num)
	}
}

// TestHandlePing_PreservesFunnelFields guards the normalising re-marshal in
// handlePing: a features payload with funnel day offsets must round-trip
// into the stored JSON, including a 0 ("same day") value.
func TestHandlePing_PreservesFunnelFields(t *testing.T) {
	s := newTestServer(t, "v1.30.1")
	s.limiter = newRateLimiter(time.Hour, time.Minute)

	body := []byte(`{"install_id":"11111111-1111-1111-1111-111111111111","version":"1.30.1",` +
		`"os":"linux","arch":"amd64","features":{"indexers":2,"setup_indexer_day":0,"first_grab_day":3}}`)
	r := httptest.NewRequest(http.MethodPost, "/api/ping", bytes.NewReader(body))
	r.RemoteAddr = "192.0.2.30:1234"
	w := httptest.NewRecorder()
	s.handlePing(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ping status = %d, body = %s", w.Code, w.Body.String())
	}

	var stored string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT features FROM installs WHERE install_id = '11111111-1111-1111-1111-111111111111'`,
	).Scan(&stored); err != nil {
		t.Fatalf("read features: %v", err)
	}
	var f featuresPayload
	if err := json.Unmarshal([]byte(stored), &f); err != nil {
		t.Fatalf("parse stored features: %v", err)
	}
	if f.SetupIndexerDay == nil || *f.SetupIndexerDay != 0 {
		t.Errorf("setup_indexer_day = %v, want 0 (zero must survive the re-marshal)", f.SetupIndexerDay)
	}
	if f.FirstGrabDay == nil || *f.FirstGrabDay != 3 {
		t.Errorf("first_grab_day = %v, want 3", f.FirstGrabDay)
	}
	if f.SetupClientDay != nil {
		t.Errorf("setup_client_day = %d, want nil (not sent)", *f.SetupClientDay)
	}
}
