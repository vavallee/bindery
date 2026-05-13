package main

import (
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
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE installs (
		install_id  TEXT PRIMARY KEY,
		version     TEXT NOT NULL,
		os          TEXT NOT NULL,
		arch        TEXT NOT NULL,
		first_seen  DATETIME NOT NULL,
		last_seen   DATETIME NOT NULL,
		deploy      TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create table: %v", err)
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
