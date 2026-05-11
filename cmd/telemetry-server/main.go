// telemetry-server is a tiny HTTP service that counts active Bindery installs.
// It accepts anonymous pings from Bindery instances and returns the latest
// published version so clients can surface an update badge.
//
// Endpoints:
//
//	GET  /               — welcome page with logo and GitHub link
//	GET  /stats          — public dashboard with version/OS/arch charts
//	POST /api/ping       — upsert install record, return latest version (rate-limited)
//	GET  /api/stats      — active/total counts + version breakdown (token-gated)
//	GET  /api/backup     — sqlite VACUUM INTO snapshot of installs DB (token-gated)
//	GET  /health         — liveness probe
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// releaseVersionPattern matches semver-shaped release tags ("1.7.0", "v1.7.0").
// Older Bindery clients (before the corresponding client-side filter) still
// ping with non-release labels like "sha-abc1234", "dev-abc1234", and the
// git-describe "v1.7.0-3-gabc1234" form for commits past a tag. Drop those
// here so the version histogram doesn't grow a row per CI commit.
var releaseVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// isReleaseVersion reports whether v looks like a semver release tag.
func isReleaseVersion(v string) bool {
	return releaseVersionPattern.MatchString(v)
}

type server struct {
	db            *sql.DB
	latestVersion string
	statsToken    string
	limiter       *rateLimiter
}

type pingRequest struct {
	InstallID string `json:"install_id"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Deploy    string `json:"deploy"`
}

// validDeploys constrains the set of deploy strings we accept on /api/ping.
// Anything outside this list is replaced with "" so a malicious client can't
// pollute the chart's legend with arbitrary labels.
var validDeploys = map[string]bool{
	"kubernetes": true,
	"docker":     true,
	"binary":     true,
	"helm":       true, // Helm chart can opt-in via BINDERY_DEPLOY_METHOD=helm
}

type pingResponse struct {
	LatestVersion string `json:"latest_version"`
}

type statsResponse struct {
	Active30d int            `json:"active_30d"`
	Total     int            `json:"total"`
	Versions  map[string]int `json:"versions"`
}

func main() {
	dbPath := env("DB_PATH", "/data/telemetry.db")
	addr := env("ADDR", ":8080")
	canonicalHost := env("CANONICAL_HOST", "")
	latestVersion := env("LATEST_VERSION", "v1.4.3")
	statsToken := env("STATS_TOKEN", "")

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		slog.Error("open db", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS installs (
		install_id  TEXT PRIMARY KEY,
		version     TEXT NOT NULL,
		os          TEXT NOT NULL,
		arch        TEXT NOT NULL,
		first_seen  DATETIME NOT NULL,
		last_seen   DATETIME NOT NULL
	)`); err != nil {
		slog.Error("migrate db", "error", err)
		os.Exit(1)
	}

	// Strip a leading "v" from any pre-existing version starting with "v<digit>",
	// so `v1.4.4` and `1.4.4` collapse into one chart slice. Idempotent: after
	// the first run no rows match, so subsequent startups are no-ops.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE installs SET version = substr(version, 2) WHERE version GLOB 'v[0-9]*'`,
	); err != nil {
		slog.Error("normalize versions", "error", err)
		os.Exit(1)
	}

	// Add the deploy column (kubernetes / docker / binary) for newer pings.
	// Existing rows get an empty string and surface as "(unknown)" in the
	// dashboard. ALTER TABLE ADD COLUMN errors with "duplicate column" once
	// the column exists, which we tolerate so the migration stays idempotent.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE installs ADD COLUMN deploy TEXT NOT NULL DEFAULT ''`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		slog.Error("add deploy column", "error", err)
		os.Exit(1)
	}

	// Sweep test fixtures and locally-built dev rows. These are throwaway
	// UUIDs from `go run` / `go build` sessions that inflate the active count
	// without representing a real installation. New clients no longer send
	// these pings (see internal/telemetry/client.go); the server still drops
	// them belt-and-suspenders below.
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM installs WHERE version = 'dev' OR install_id NOT GLOB '????????-????-????-????-????????????'`,
	); err != nil {
		slog.Error("cleanup dev rows", "error", err)
		os.Exit(1)
	}

	s := &server{
		db:            db,
		latestVersion: latestVersion,
		statsToken:    statsToken,
		// Each IP may ping at most once per hour.
		limiter: newRateLimiter(1*time.Hour, 5*time.Minute),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /stats", s.handleStatsPage)
	mux.HandleFunc("POST /api/ping", s.handlePing)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/backup", s.handleBackup)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      secureHeaders(canonicalHost, mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	slog.Info("telemetry-server starting", "addr", addr, "latest", latestVersion)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
}

// secureHeaders adds security response headers and rejects non-HTTPS requests
// (when running behind Traefik, X-Forwarded-Proto carries the original scheme).
// host is the canonical public hostname (CANONICAL_HOST env var); when set it
// is used in the HTTPS redirect instead of the request's Host header, which a
// client could otherwise set to an arbitrary domain (open redirect).
func secureHeaders(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject plain HTTP when the request came through the public ingress.
		if r.Header.Get("X-Forwarded-Proto") == "http" {
			target := host
			if target == "" {
				target = r.Host
			}
			http.Redirect(w, r, "https://"+target+r.RequestURI, http.StatusMovedPermanently)
			return
		}
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src https:; style-src 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleHome(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    display: flex; align-items: center; justify-content: center;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  }
  .card {
    text-align: center;
    padding: 3rem 2rem;
    max-width: 520px;
  }
  img { width: 96px; height: 96px; margin-bottom: 1.5rem; }
  h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.02em; margin-bottom: .5rem; }
  p  { color: #94a3b8; line-height: 1.7; margin-bottom: 2rem; }
  .features {
    display: flex; flex-direction: column; gap: .5rem;
    margin-bottom: 2rem;
    text-align: left;
  }
  .feature {
    display: flex; align-items: flex-start; gap: .65rem;
    color: #94a3b8; font-size: .9rem; line-height: 1.5;
  }
  .feature-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: #10b981; flex-shrink: 0; margin-top: .45rem;
  }
  .links { display: flex; flex-wrap: wrap; gap: .75rem; justify-content: center; }
  a {
    display: inline-flex; align-items: center; gap: .5rem;
    padding: .65rem 1.4rem;
    border-radius: 8px; text-decoration: none;
    font-weight: 600; font-size: .9rem;
    transition: background .15s, color .15s;
  }
  a.primary { background: #10b981; color: #fff; }
  a.primary:hover { background: #059669; }
  a.secondary {
    background: transparent; color: #94a3b8;
    border: 1px solid #334155;
  }
  a.secondary:hover { background: #1e293b; color: #e2e8f0; }
</style>
</head>
<body>
<div class="card">
  <img src="https://raw.githubusercontent.com/vavallee/bindery/main/.github/assets/logo.png" alt="Bindery logo">
  <h1>Bindery</h1>
  <p>Open-source automated book management for self-hosters. Monitor your
     favourite authors, discover new books, and have them downloaded and
     organized automatically — no scraping, no dead backends.</p>
  <div class="features">
    <div class="feature"><div class="feature-dot"></div><span>Tracks monitored authors via OpenLibrary and surfaces new releases automatically</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Integrates with Prowlarr, qBittorrent, SABnzbd, and Transmission</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Discover page with personalized recommendations based on your library</span></div>
    <div class="feature"><div class="feature-dot"></div><span>Calibre bridge plugin for automatic library import after download</span></div>
    <div class="feature"><div class="feature-dot"></div><span>OPDS feed, OIDC auth, Prometheus metrics, and dark mode</span></div>
  </div>
  <div class="links">
    <a class="primary" href="https://github.com/vavallee/bindery">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor">
        <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z"/>
      </svg>
      View on GitHub
    </a>
    <a class="secondary" href="https://github.com/vavallee/bindery-plugins">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/>
        <path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/>
      </svg>
      Calibre Plugin
    </a>
    <a class="secondary" href="/stats">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <line x1="18" y1="20" x2="18" y2="10"/>
        <line x1="12" y1="20" x2="12" y2="4"/>
        <line x1="6" y1="20" x2="6" y2="14"/>
      </svg>
      Telemetry
    </a>
  </div>
</div>
</body>
</html>`))
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	ip := realIP(r)
	if !s.limiter.allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var req pingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !uuidRE.MatchString(req.InstallID) {
		http.Error(w, "install_id must be a valid UUID", http.StatusBadRequest)
		return
	}
	if len(req.Version) > 64 || len(req.OS) > 32 || len(req.Arch) > 32 || len(req.Deploy) > 32 {
		http.Error(w, "field too long", http.StatusBadRequest)
		return
	}
	req.Version = normalizeVersion(req.Version)
	// Reject pings from non-release builds — the literal "dev" (local
	// `go run` / `go build` with no -ldflags), CI's "sha-abc1234" and
	// "dev-abc1234" interim images, and "v1.7.0-3-gabc1234" git-describe
	// labels for commits past a tag. Each of those becomes its own row
	// in the version histogram, and the install_id is overwhelmingly
	// throwaway (CI / dev containers that recreate the DB on every run).
	// Newer clients skip these client-side; older clients land here.
	if !isReleaseVersion(req.Version) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pingResponse{LatestVersion: s.latestVersion})
		return
	}
	if !validDeploys[req.Deploy] {
		req.Deploy = ""
	}

	now := time.Now().UTC()
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO installs (install_id, version, os, arch, deploy, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(install_id) DO UPDATE SET
			version   = excluded.version,
			os        = excluded.os,
			arch      = excluded.arch,
			deploy    = excluded.deploy,
			last_seen = excluded.last_seen
	`, req.InstallID, req.Version, req.OS, req.Arch, req.Deploy, now, now)
	if err != nil {
		slog.Warn("upsert install", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("ping", "id", req.InstallID[:min(8, len(req.InstallID))], "version", req.Version, "os", req.OS, "arch", req.Arch)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pingResponse{LatestVersion: s.latestVersion})
}

// statsBucket pairs a label (version / OS / arch) with its install count.
type statsBucket struct {
	Label string
	Count int
}

// dailyBucket holds the count of installs whose last_seen falls within a
// single day, used for the 30-day activity sparkline.
type dailyBucket struct {
	Day   time.Time
	Count int
}

// versionTrendDay holds per-version active counts for one day, used in the
// stacked version-adoption chart.
type versionTrendDay struct {
	Day      time.Time
	Versions map[string]int
	Total    int
}

// statsData is the complete aggregated telemetry view used by both the
// auth-gated JSON API and the public HTML dashboard.
type statsData struct {
	Active30d    int
	Total        int
	Versions     []statsBucket
	OS           []statsBucket
	Arch         []statsBucket
	Deploy       []statsBucket
	Daily        []dailyBucket
	DailyNew     []dailyBucket     // new installs per day (first_seen), 30 days
	Longevity    []statsBucket     // age buckets for 30d-active installs
	Monthly      []statsBucket     // new installs per calendar month, last 12 mo
	VersionTrend []versionTrendDay // per-day per-version active counts, 30 days
	TopVersions  []string          // top-N versions for VersionTrend legend
}

// computeStats runs every dashboard query and returns one assembled snapshot.
// All counts are scoped to the active-30-day window except Total.
func (s *server) computeStats(ctx context.Context) (*statsData, error) {
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	d := &statsData{}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ?`, cutoff,
	).Scan(&d.Active30d); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs`,
	).Scan(&d.Total); err != nil {
		return nil, err
	}

	queryBuckets := func(col string) ([]statsBucket, error) {
		// #nosec G202 — col is a literal from the caller, not user input.
		q := `SELECT ` + col + `, COUNT(*) FROM installs WHERE last_seen >= ? GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`
		rows, err := s.db.QueryContext(ctx, q, cutoff)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []statsBucket
		for rows.Next() {
			var b statsBucket
			if err := rows.Scan(&b.Label, &b.Count); err != nil {
				return nil, err
			}
			if b.Label == "" {
				b.Label = "(unknown)"
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}

	var err error
	if d.Versions, err = queryBuckets("version"); err != nil {
		return nil, err
	}
	if d.OS, err = queryBuckets("os"); err != nil {
		return nil, err
	}
	if d.Arch, err = queryBuckets("arch"); err != nil {
		return nil, err
	}
	if d.Deploy, err = queryBuckets("deploy"); err != nil {
		return nil, err
	}

	// Daily activity for the last 30 days. last_seen is stored in Go's
	// time.Time.String() form (`YYYY-MM-DD HH:MM:SS.NNNNNNNNN ±HHMM TZ`),
	// which SQLite's date() can't parse — slice the YYYY-MM-DD prefix instead.
	rows, err := s.db.QueryContext(ctx,
		`SELECT substr(last_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE last_seen >= ? GROUP BY day ORDER BY day`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dayCount := make(map[string]int)
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		dayCount[day] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Fill in zero-count days so the sparkline has a continuous 30-day axis.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		d.Daily = append(d.Daily, dailyBucket{Day: day, Count: dayCount[key]})
	}

	// New installs per day (first_seen, last 30 days).
	rowsNew, err := s.db.QueryContext(ctx,
		`SELECT substr(first_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE first_seen >= ? GROUP BY day ORDER BY day`, cutoff)
	if err != nil {
		return nil, err
	}
	newDayCount := make(map[string]int)
	for rowsNew.Next() {
		var day string
		var count int
		if err := rowsNew.Scan(&day, &count); err != nil {
			rowsNew.Close()
			return nil, err
		}
		newDayCount[day] = count
	}
	if err := rowsNew.Err(); err != nil {
		rowsNew.Close()
		return nil, err
	}
	rowsNew.Close()
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		d.DailyNew = append(d.DailyNew, dailyBucket{Day: day, Count: newDayCount[key]})
	}

	// Install longevity: bucket 30d-active installs by age (last_seen − first_seen).
	rowsLon, err := s.db.QueryContext(ctx, `
		SELECT
		  CASE
		    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 7
		         THEN '< 1 week'
		    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 30
		         THEN '1–4 weeks'
		    WHEN CAST(julianday(substr(last_seen,1,10)) - julianday(substr(first_seen,1,10)) AS INTEGER) < 90
		         THEN '1–3 months'
		    ELSE '3+ months'
		  END AS bucket,
		  COUNT(*) AS n
		FROM installs WHERE last_seen >= ?
		GROUP BY bucket`, cutoff)
	if err != nil {
		return nil, err
	}
	lonMap := make(map[string]int)
	for rowsLon.Next() {
		var bucket string
		var count int
		if err := rowsLon.Scan(&bucket, &count); err != nil {
			rowsLon.Close()
			return nil, err
		}
		lonMap[bucket] = count
	}
	if err := rowsLon.Err(); err != nil {
		rowsLon.Close()
		return nil, err
	}
	rowsLon.Close()
	for _, label := range []string{"< 1 week", "1–4 weeks", "1–3 months", "3+ months"} {
		if cnt, ok := lonMap[label]; ok {
			d.Longevity = append(d.Longevity, statsBucket{Label: label, Count: cnt})
		}
	}

	// Monthly new installs for the last 12 calendar months.
	monthlyCutoff := time.Now().UTC().AddDate(0, -12, 0)
	rowsMon, err := s.db.QueryContext(ctx,
		`SELECT substr(first_seen, 1, 7) AS month, COUNT(*) FROM installs WHERE first_seen >= ? GROUP BY month ORDER BY month`,
		monthlyCutoff)
	if err != nil {
		return nil, err
	}
	for rowsMon.Next() {
		var month string
		var count int
		if err := rowsMon.Scan(&month, &count); err != nil {
			rowsMon.Close()
			return nil, err
		}
		label := month
		if t, err := time.Parse("2006-01", month); err == nil {
			label = t.Format("Jan '06")
		}
		d.Monthly = append(d.Monthly, statsBucket{Label: label, Count: count})
	}
	if err := rowsMon.Err(); err != nil {
		rowsMon.Close()
		return nil, err
	}
	rowsMon.Close()

	// Top versions for the version-trend legend (max 4).
	for i, v := range d.Versions {
		if i >= 4 {
			break
		}
		d.TopVersions = append(d.TopVersions, v.Label)
	}

	// Version mix per day for the last 30 days (stacked trend chart).
	rowsVer, err := s.db.QueryContext(ctx,
		`SELECT substr(last_seen, 1, 10) AS day, version, COUNT(*) FROM installs WHERE last_seen >= ? GROUP BY day, version ORDER BY day`,
		cutoff)
	if err != nil {
		return nil, err
	}
	vtMap := make(map[string]map[string]int) // day -> version -> count
	for rowsVer.Next() {
		var day, ver string
		var count int
		if err := rowsVer.Scan(&day, &ver, &count); err != nil {
			rowsVer.Close()
			return nil, err
		}
		if vtMap[day] == nil {
			vtMap[day] = make(map[string]int)
		}
		vtMap[day][ver] = count
	}
	if err := rowsVer.Err(); err != nil {
		rowsVer.Close()
		return nil, err
	}
	rowsVer.Close()
	for i := 29; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		vd := versionTrendDay{Day: day, Versions: vtMap[key]}
		if vd.Versions == nil {
			vd.Versions = make(map[string]int)
		}
		for _, cnt := range vd.Versions {
			vd.Total += cnt
		}
		d.VersionTrend = append(d.VersionTrend, vd)
	}

	return d, nil
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.statsToken == "" {
		http.Error(w, "stats endpoint not configured", http.StatusForbidden)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.statsToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	d, err := s.computeStats(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	versions := make(map[string]int, len(d.Versions))
	for _, b := range d.Versions {
		versions[b.Label] = b.Count
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statsResponse{
		Active30d: d.Active30d,
		Total:     d.Total,
		Versions:  versions,
	})
}

// handleBackup streams a consistent snapshot of the installs database. Uses
// SQLite's VACUUM INTO so the snapshot is taken under a transaction (no torn
// reads against concurrent ping writes) and lands as a fully self-contained
// file with no WAL sidecar. The endpoint is token-gated identically to
// /api/stats and is intended to be pulled by a scheduled GitHub workflow.
func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.statsToken == "" {
		http.Error(w, "backup endpoint not configured", http.StatusForbidden)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.statsToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	dir, err := os.MkdirTemp("", "bindery-backup-*")
	if err != nil {
		slog.Error("backup: mkdir", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			slog.Warn("backup: cleanup tempdir", "error", removeErr)
		}
	}()
	dst := filepath.Join(dir, "telemetry.db")

	// VACUUM INTO acquires a read transaction, copies pages out, and produces
	// a single self-contained SQLite file. Safer than streaming /data/telemetry.db
	// directly (which would race the WAL). Path is parameterized as a literal
	// string in the SQL because VACUUM INTO does not accept bind parameters.
	// The destination path is a server-generated tempdir path with single-quotes
	// escaped, so SQL injection is not possible here.
	vacuumSQL := `VACUUM INTO '` + strings.ReplaceAll(dst, `'`, `''`) + `'` //nolint:gosec // G202: path is server-generated with quotes escaped
	if _, err := s.db.ExecContext(r.Context(),
		vacuumSQL,
	); err != nil {
		slog.Error("backup: vacuum", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(dst) // #nosec G304 — dst is a server-generated tempdir path.
	if err != nil {
		slog.Error("backup: open", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		slog.Error("backup: stat", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := "bindery-telemetry-" + time.Now().UTC().Format("2006-01-02") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	if _, err := io.Copy(w, f); err != nil {
		slog.Warn("backup: write response", "error", err)
	}
}

// chartPalette cycles through these colours for chart bars and legend swatches.
var chartPalette = []string{
	"#10b981", "#3b82f6", "#f59e0b", "#a855f7",
	"#ef4444", "#06b6d4", "#ec4899", "#84cc16",
}

func paletteColor(i int) string { return chartPalette[i%len(chartPalette)] }

// renderBarChart returns inline SVG for a horizontal bar chart with a legend.
// Each bucket gets its own colour from chartPalette so the swatch in the
// legend column matches the bar.
//
// When pinLabel is non-empty and matches a bucket that would otherwise fall
// into the "(other)" tail, the pinned bucket is swapped into the last visible
// slot so a freshly cut release stays visible before it organically reaches
// top-N. The pinned row's legend is suffixed with " (latest)". Buckets with
// pinLabel already in the visible region are annotated but not reordered.
func renderBarChart(buckets []statsBucket, maxBars int, pinLabel string) string {
	if len(buckets) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	if maxBars > 0 && len(buckets) > maxBars {
		// Copy so we can mutate without surprising the caller (other charts
		// reuse the same slices indirectly via stable-sort in handleStatsPage).
		work := make([]statsBucket, len(buckets))
		copy(work, buckets)
		buckets = work

		if pinLabel != "" {
			pinIdx := -1
			for i := maxBars; i < len(buckets); i++ {
				if buckets[i].Label == pinLabel {
					pinIdx = i
					break
				}
			}
			if pinIdx >= 0 {
				buckets[maxBars-1], buckets[pinIdx] = buckets[pinIdx], buckets[maxBars-1]
			}
		}

		// Collapse the long tail into an "(other)" bucket so the chart
		// doesn't grow unbounded with every release sha.
		head := buckets[:maxBars]
		tail := 0
		for _, b := range buckets[maxBars:] {
			tail += b.Count
		}
		head = append(head, statsBucket{Label: "(other)", Count: tail})
		buckets = head
	}
	max := buckets[0].Count
	for _, b := range buckets {
		if b.Count > max {
			max = b.Count
		}
	}

	var sb strings.Builder
	sb.WriteString(`<div class="chart">`)
	sb.WriteString(`<table class="bars" role="presentation">`)
	for i, b := range buckets {
		colour := paletteColor(i)
		pct := 0
		if max > 0 {
			pct = b.Count * 100 / max
		}
		label := b.Label
		if pinLabel != "" && b.Label == pinLabel {
			label = b.Label + " (latest)"
		}
		fmt.Fprintf(&sb,
			`<tr><td class="legend-cell"><span class="swatch" style="background:%s"></span>%s</td>`+
				`<td class="bar-cell"><div class="bar" style="width:%d%%;background:%s"></div></td>`+
				`<td class="count-cell">%d</td></tr>`,
			colour, html.EscapeString(label), pct, colour, b.Count)
	}
	sb.WriteString(`</table></div>`)
	return sb.String()
}

// renderSparkline returns inline SVG for a 30-day daily-activity bar chart.
func renderSparkline(daily []dailyBucket) string {
	if len(daily) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	max := 0
	for _, d := range daily {
		if d.Count > max {
			max = d.Count
		}
	}
	const w, h, gap = 600, 80, 2
	barW := (w - gap*(len(daily)-1)) / len(daily)
	if barW < 1 {
		barW = 1
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Daily active installs over the last 30 days">`, w, h)
	for i, d := range daily {
		barH := 0
		if max > 0 {
			barH = d.Count * h / max
		}
		if barH < 1 && d.Count > 0 {
			barH = 1
		}
		x := i * (barW + gap)
		y := h - barH
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" fill="#10b981"><title>%s: %d</title></rect>`,
			x, y, barW, barH, d.Day.Format("Jan 2"), d.Count)
	}
	sb.WriteString(`</svg>`)
	// Axis labels: first and last day below the chart.
	first := daily[0].Day.Format("Jan 2")
	last := daily[len(daily)-1].Day.Format("Jan 2")
	fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`, first, last)
	return sb.String()
}

// renderMonthlyChart renders a bar sparkline for monthly new-install counts.
// Each bar is labelled via its title attribute; axis labels show first/last month.
func renderMonthlyChart(points []statsBucket) string {
	if len(points) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	max := 0
	for _, p := range points {
		if p.Count > max {
			max = p.Count
		}
	}
	const w, h, gap = 600, 80, 4
	barW := (w - gap*(len(points)-1)) / len(points)
	if barW < 1 {
		barW = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="New installs per month over the last 12 months">`, w, h)
	for i, p := range points {
		barH := 0
		if max > 0 {
			barH = p.Count * h / max
		}
		if barH < 1 && p.Count > 0 {
			barH = 1
		}
		x := i * (barW + gap)
		y := h - barH
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" fill="#3b82f6"><title>%s: %d new</title></rect>`,
			x, y, barW, barH, html.EscapeString(p.Label), p.Count)
	}
	sb.WriteString(`</svg>`)
	if len(points) >= 2 {
		fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`,
			html.EscapeString(points[0].Label), html.EscapeString(points[len(points)-1].Label))
	}
	return sb.String()
}

// renderVersionTrend renders a stacked-proportion day chart: each day's bar is
// divided by version share, so you can see version N-1 declining as N rises.
// topVersions controls colour assignment; any version not in the list is
// collapsed into an "(other)" dark-grey segment.
func renderVersionTrend(days []versionTrendDay, topVersions []string) string {
	if len(days) == 0 {
		return `<p class="empty">No data yet.</p>`
	}
	const w, h, gap = 600, 80, 2
	barW := (w - gap*(len(days)-1)) / len(days)
	if barW < 1 {
		barW = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Version distribution over the last 30 days">`, w, h)
	for i, d := range days {
		if d.Total == 0 {
			continue
		}
		x := i * (barW + gap)
		y := 0
		for vi, ver := range topVersions {
			cnt := d.Versions[ver]
			if cnt == 0 {
				continue
			}
			segH := cnt * h / d.Total
			if segH < 1 {
				segH = 1
			}
			fmt.Fprintf(&sb,
				`<rect x="%d" y="%d" width="%d" height="%d" fill="%s"><title>%s %s: %d</title></rect>`,
				x, y, barW, segH, paletteColor(vi), d.Day.Format("Jan 2"), html.EscapeString(ver), cnt)
			y += segH
		}
		// Remaining versions collapsed into "(other)".
		other := d.Total
		for _, ver := range topVersions {
			other -= d.Versions[ver]
		}
		if other > 0 {
			segH := other * h / d.Total
			if segH < 1 {
				segH = 1
			}
			fmt.Fprintf(&sb,
				`<rect x="%d" y="%d" width="%d" height="%d" fill="#475569"><title>%s (other): %d</title></rect>`,
				x, y, barW, segH, d.Day.Format("Jan 2"), other)
		}
	}
	sb.WriteString(`</svg>`)
	first := days[0].Day.Format("Jan 2")
	last := days[len(days)-1].Day.Format("Jan 2")
	fmt.Fprintf(&sb, `<div class="sparkline-axis"><span>%s</span><span>%s</span></div>`, first, last)
	// Colour legend.
	sb.WriteString(`<div class="trend-legend">`)
	for vi, ver := range topVersions {
		fmt.Fprintf(&sb, `<span class="trend-key"><span class="swatch" style="background:%s"></span>%s</span>`,
			paletteColor(vi), html.EscapeString(ver))
	}
	if len(topVersions) > 0 {
		sb.WriteString(`<span class="trend-key"><span class="swatch" style="background:#475569"></span>(other)</span>`)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// handleStatsPage renders a public dashboard with charts and a legend.
// The data is the same aggregate counts surfaced by /api/stats; nothing
// install-identifying is exposed.
func (s *server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.computeStats(r.Context())
	if err != nil {
		slog.Error("stats page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stable order for legend-rendering: queryBuckets already sorts by count
	// desc; ties are broken alphabetically here so the page is deterministic.
	stable := func(bs []statsBucket) {
		sort.SliceStable(bs, func(i, j int) bool {
			if bs[i].Count != bs[j].Count {
				return bs[i].Count > bs[j].Count
			}
			return bs[i].Label < bs[j].Label
		})
	}
	stable(d.Versions)
	stable(d.OS)
	stable(d.Arch)
	stable(d.Deploy)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery — Telemetry</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    padding: 3rem 1.5rem;
  }
  .container { max-width: 720px; margin: 0 auto; }
  header { margin-bottom: 2.5rem; }
  header a.back { color: #94a3b8; font-size: .85rem; text-decoration: none; }
  header a.back:hover { color: #e2e8f0; }
  h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.02em; margin: .75rem 0 .5rem; }
  header p { color: #94a3b8; font-size: .9rem; line-height: 1.6; }
  .summary { display: flex; gap: 1rem; margin: 2rem 0; flex-wrap: wrap; }
  .summary .stat {
    flex: 1 1 200px;
    padding: 1.25rem 1.5rem;
    background: #1e293b;
    border: 1px solid #334155;
    border-radius: 10px;
  }
  .summary .stat .num { font-size: 2.25rem; font-weight: 700; color: #10b981; line-height: 1; }
  .summary .stat .label { color: #94a3b8; font-size: .85rem; margin-top: .35rem; }
  section { margin-bottom: 2.5rem; }
  section > h2 {
    font-size: .8rem; font-weight: 700; text-transform: uppercase;
    letter-spacing: .08em; color: #94a3b8; margin-bottom: .85rem;
  }
  .chart { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 1rem 1.25rem; }
  table.bars { width: 100%%; border-collapse: collapse; font-size: .9rem; }
  table.bars td { padding: .35rem .5rem; vertical-align: middle; }
  td.legend-cell { width: 35%%; color: #cbd5e1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  td.bar-cell { width: 50%%; }
  td.count-cell { width: 15%%; text-align: right; color: #94a3b8; font-variant-numeric: tabular-nums; }
  .swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: .5rem; vertical-align: middle; }
  .bar { height: 14px; border-radius: 2px; min-width: 2px; }
  .sparkline { width: 100%%; height: 80px; display: block; }
  .sparkline-axis {
    display: flex; justify-content: space-between;
    color: #94a3b8; font-size: .75rem; margin-top: .35rem;
  }
  .empty { color: #94a3b8; font-size: .85rem; padding: .25rem 0; }
  .trend-legend { display: flex; flex-wrap: wrap; gap: .5rem 1.25rem; margin-top: .75rem; font-size: .8rem; color: #cbd5e1; }
  .trend-key { display: flex; align-items: center; gap: .4rem; }
  footer { margin-top: 3rem; color: #64748b; font-size: .75rem; text-align: center; }
</style>
</head>
<body>
<div class="container">
  <header>
    <a class="back" href="/">← Bindery</a>
    <h1>Telemetry</h1>
    <p>Anonymous install counts from instances that opted in to update checks. No identifying information is collected — only an opaque install UUID, the version, and the OS/arch reported by Go's runtime. Active = pinged in the last 30 days.</p>
  </header>

  <div class="summary">
    <div class="stat"><div class="num">%d</div><div class="label">Active installs (30d)</div></div>
    <div class="stat"><div class="num">%d</div><div class="label">Total installs (all-time)</div></div>
  </div>

  <section>
    <h2>Versions</h2>
    %s
  </section>

  <section>
    <h2>Operating system</h2>
    %s
  </section>

  <section>
    <h2>Architecture</h2>
    %s
  </section>

  <section>
    <h2>Deployment method</h2>
    %s
  </section>

  <section>
    <h2>Daily activity (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>New installs per day (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>New installs per month (last 12 months)</h2>
    <div class="chart">%s</div>
  </section>

  <section>
    <h2>Install longevity (active installs by age)</h2>
    %s
  </section>

  <section>
    <h2>Version mix (last 30 days)</h2>
    <div class="chart">%s</div>
  </section>

  <footer>
    Generated %s. Data refreshes on every page load.
  </footer>
</div>
</body>
</html>`,
		d.Active30d, d.Total,
		renderBarChart(d.Versions, 8, normalizeVersion(s.latestVersion)),
		renderBarChart(d.OS, 0, ""),
		renderBarChart(d.Arch, 0, ""),
		renderBarChart(d.Deploy, 0, ""),
		renderSparkline(d.Daily),
		renderSparkline(d.DailyNew),
		renderMonthlyChart(d.Monthly),
		renderBarChart(d.Longevity, 0, ""),
		renderVersionTrend(d.VersionTrend, d.TopVersions),
		time.Now().UTC().Format("2006-01-02 15:04 MST"),
	); err != nil {
		slog.Warn("stats: write response", "error", err)
	}
}

// normalizeVersion strips a leading "v" from version strings of the form
// "v1.4.4" so they collapse into the same bucket as "1.4.4". Non-release
// strings ("dev", "sha-abc1234") pass through unchanged because the "v"
// strip is gated on the next character being a digit.
func normalizeVersion(v string) string {
	if len(v) >= 2 && v[0] == 'v' && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}
	return v
}

// realIP returns the client IP for rate limiting. Prefers X-Real-Ip set by
// Traefik (which reflects the actual downstream address regardless of any
// X-Forwarded-For the client may have injected). Falls back to the rightmost
// address in X-Forwarded-For — the one appended by the closest trusted proxy —
// and finally to RemoteAddr. Never uses the leftmost X-Forwarded-For value,
// which a client can spoof freely.
func realIP(r *http.Request) string {
	if xri := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	// RemoteAddr is "IP:port"; strip the port so each unique client IP gets
	// one rate-limit bucket regardless of which ephemeral port it connects from.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateLimiter is a simple in-memory token bucket: one ping per window per IP.
type rateLimiter struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	window  time.Duration
	cleanup time.Duration
}

func newRateLimiter(window, cleanup time.Duration) *rateLimiter {
	rl := &rateLimiter{
		seen:    make(map[string]time.Time),
		window:  window,
		cleanup: cleanup,
	}
	go rl.purge()
	return rl
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if last, ok := rl.seen[ip]; ok && time.Since(last) < rl.window {
		return false
	}
	rl.seen[ip] = time.Now()
	return true
}

func (rl *rateLimiter) purge() {
	for range time.Tick(rl.cleanup) {
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.window)
		for ip, t := range rl.seen {
			if t.Before(cutoff) {
				delete(rl.seen, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
