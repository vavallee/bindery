package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, stmt := range []string{
		`CREATE TABLE installs (
			install_id  TEXT PRIMARY KEY,
			version     TEXT NOT NULL,
			os          TEXT NOT NULL,
			arch        TEXT NOT NULL,
			first_seen  DATETIME NOT NULL,
			last_seen   DATETIME NOT NULL,
			deploy      TEXT NOT NULL DEFAULT '',
			features    TEXT
		)`,
		`CREATE TABLE daily_global (
			day          TEXT PRIMARY KEY,
			active_day   INTEGER NOT NULL DEFAULT 0,
			new_installs INTEGER NOT NULL DEFAULT 0,
			total        INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE daily_version (
			day          TEXT NOT NULL,
			version      TEXT NOT NULL,
			active_count INTEGER NOT NULL,
			PRIMARY KEY (day, version)
		)`,
		`CREATE TABLE daily_features (
			day              TEXT NOT NULL,
			field            TEXT NOT NULL,
			enabled_count    INTEGER NOT NULL,
			reporting_count  INTEGER NOT NULL,
			PRIMARY KEY (day, field)
		)`,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return &server{db: db, latestVersion: latest}
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

	// Move the install to 1.15.2; re-snapshot. The 1.14.0 row from the
	// first snapshot must disappear.
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE installs SET version = '1.15.2' WHERE install_id = ?`, uuid('1'),
	); err != nil {
		t.Fatalf("update version: %v", err)
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
