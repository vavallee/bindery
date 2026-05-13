// telemetry-server is a tiny HTTP service that counts active Bindery installs.
// It accepts anonymous pings from Bindery instances and returns the latest
// published version so clients can surface an update badge.
//
// Endpoints:
//
//	GET  /               — welcome page with logo and GitHub link
//	GET  /stats          — public dashboard with version/OS/arch charts
//	GET  /stats.json     — public JSON snapshot {active,total,latest} for bots
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

// statsJSON is the response shape for the public /stats.json endpoint. It
// surfaces the three numbers the Discord stats bot (and any future scraper)
// needs without requiring the full /api/stats token-gated payload.
type statsJSON struct {
	Active int    `json:"active"` // 30-day active install count
	Total  int    `json:"total"`  // all-time install count
	Latest string `json:"latest"` // latest released version, e.g. "v1.9.5"
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
	mux.HandleFunc("GET /stats.json", s.handleStatsJSON)
	mux.HandleFunc("GET /stats/preview", s.handlePreviewPage)
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
	defer rowsNew.Close()
	newDayCount := make(map[string]int)
	for rowsNew.Next() {
		var day string
		var count int
		if err := rowsNew.Scan(&day, &count); err != nil {
			return nil, err
		}
		newDayCount[day] = count
	}
	if err := rowsNew.Err(); err != nil {
		return nil, err
	}
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
	defer rowsLon.Close()
	lonMap := make(map[string]int)
	for rowsLon.Next() {
		var bucket string
		var count int
		if err := rowsLon.Scan(&bucket, &count); err != nil {
			return nil, err
		}
		lonMap[bucket] = count
	}
	if err := rowsLon.Err(); err != nil {
		return nil, err
	}
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
	defer rowsMon.Close()
	for rowsMon.Next() {
		var month string
		var count int
		if err := rowsMon.Scan(&month, &count); err != nil {
			return nil, err
		}
		label := month
		if t, err := time.Parse("2006-01", month); err == nil {
			label = t.Format("Jan '06")
		}
		d.Monthly = append(d.Monthly, statsBucket{Label: label, Count: count})
	}
	if err := rowsMon.Err(); err != nil {
		return nil, err
	}

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
	defer rowsVer.Close()
	vtMap := make(map[string]map[string]int) // day -> version -> count
	for rowsVer.Next() {
		var day, ver string
		var count int
		if err := rowsVer.Scan(&day, &ver, &count); err != nil {
			return nil, err
		}
		if vtMap[day] == nil {
			vtMap[day] = make(map[string]int)
		}
		vtMap[day][ver] = count
	}
	if err := rowsVer.Err(); err != nil {
		return nil, err
	}
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

// handleStatsJSON returns a small public JSON payload with active install
// count, total install count, and the configured latest version. It does NOT
// run the full computeStats pipeline (versions, OS, arch, longevity, etc.)
// because callers like the Discord stats bot only need three numbers and hit
// the endpoint every 10 minutes. The two COUNT(*) queries mirror the head of
// computeStats — keeping them inline avoids dragging the heavy aggregation
// path onto this hot, unauthenticated endpoint.
func (s *server) handleStatsJSON(w http.ResponseWriter, r *http.Request) {
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	var resp statsJSON
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM installs WHERE last_seen >= ?`, cutoff,
	).Scan(&resp.Active); err != nil {
		slog.Error("stats.json: active count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM installs`,
	).Scan(&resp.Total); err != nil {
		slog.Error("stats.json: total count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp.Latest = s.latestVersion

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_ = json.NewEncoder(w).Encode(resp)
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

// ---------------------------------------------------------------------------
// /stats/preview — richer interactive dashboard.
//
// Parallel to /stats. Charts are rendered client-side by Chart.js (pinned to
// 4.4.6 via jsdelivr). Filters (range / os / deploy) are server-side: changing
// a <select> submits the form, the page re-runs every query with the narrowed
// WHERE clause, and the new JSON payload is embedded in the page.
//
// previewData is intentionally kept separate from statsData so the preview's
// shape can evolve without dragging the existing /stats page along.
// ---------------------------------------------------------------------------

// previewFilters carries the validated query-string filters for /stats/preview.
// All four fields default to "all" (no narrowing) on invalid or missing input;
// any value rendered back into the page is from this canonical set, never from
// raw user input.
type previewFilters struct {
	Range  string // 7d, 30d, 90d, all
	OS     string // linux, windows, darwin, all
	Deploy string // docker, binary, kubernetes, helm, all
}

// validPreviewRanges enumerates the accepted values for the range filter.
// Map → seconds so callers can resolve a cutoff without a switch.
var validPreviewRanges = map[string]time.Duration{
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

var validPreviewOS = map[string]bool{
	"linux":   true,
	"windows": true,
	"darwin":  true,
}

// parsePreviewFilters extracts and validates the three filter query params,
// substituting "all" / "30d" for anything unrecognised. Whitelist-only, so the
// caller can safely interpolate the OS/deploy values into SQL by way of bind
// parameters (they're never spliced into a string).
func parsePreviewFilters(q map[string][]string) previewFilters {
	f := previewFilters{Range: "30d", OS: "all", Deploy: "all"}
	if v := first(q["range"]); v != "" {
		if _, ok := validPreviewRanges[v]; ok || v == "all" {
			f.Range = v
		}
	}
	if v := first(q["os"]); v != "" {
		if validPreviewOS[v] || v == "all" {
			f.OS = v
		}
	}
	if v := first(q["deploy"]); v != "" {
		if validDeploys[v] || v == "all" {
			f.Deploy = v
		}
	}
	return f
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// cutoff returns the time threshold for the current range, plus a bool
// indicating whether a cutoff applies at all (range=all → false). The zero
// time is returned when no cutoff applies so callers can use a single sentinel.
func (f previewFilters) cutoff(now time.Time) (time.Time, bool) {
	d, ok := validPreviewRanges[f.Range]
	if !ok {
		return time.Time{}, false
	}
	return now.Add(-d), true
}

// whereClause builds the OS/deploy portion of a WHERE clause and the matching
// bind parameter slice. Returns ("", nil) when no extra filter is needed.
// Callers prepend whatever cutoff/range filter they need and combine the
// result with the existing query template.
func (f previewFilters) whereClause() (string, []any) {
	var parts []string
	var args []any
	if f.OS != "all" {
		parts = append(parts, "os = ?")
		args = append(args, f.OS)
	}
	if f.Deploy != "all" {
		parts = append(parts, "deploy = ?")
		args = append(args, f.Deploy)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}

// retentionCohort holds one weekly cohort's retention rates. The percentages
// are computed in Go from the raw counts (`Size`, `R7`, `R14`, `R30`).
type retentionCohort struct {
	WeekStart time.Time // Monday of the cohort week (first ping in this week)
	Size      int       // installs first seen in this week (W₀)
	R7        int       // still pinging ≥7 days after first_seen
	R14       int       // still pinging ≥14 days after first_seen
	R30       int       // still pinging ≥30 days after first_seen
}

// previewData is the dashboard payload for /stats/preview. Fields map 1:1 onto
// the charts on the page; everything is precomputed server-side so the
// client-side Chart.js setup is just `new Chart(ctx, {data})`.
type previewData struct {
	Filters previewFilters

	// Hero stats row.
	ActiveRange    int     // active installs within the current range
	Total          int     // all-time total, ignores range filter
	NewRange       int     // new installs (first_seen) within the current range
	ActivePrev     int     // active count in the equal-length window ending at cutoff
	DeltaActive    int     // ActiveRange − ActivePrev (omit card when range=all)
	DeltaActivePct float64 // percentage change as a float (positive or negative)
	ShowDelta      bool    // false when range=all
	RangeLabel     string  // human label, e.g. "last 30 days"

	Daily       []dailyBucket // line chart: active per day across window
	DailyNew    []dailyBucket // bar chart: new installs per day across window
	OS          []statsBucket // doughnut: OS share
	Arch        []statsBucket // doughnut: arch share
	Deploy      []statsBucket // doughnut: deploy share
	Longevity   []statsBucket // bar: age buckets
	TopVersions []string      // legend order for the stacked area
	VersionDays []versionTrendDay

	Cohorts []retentionCohort
}

// rangeLabel returns a short human-readable summary of the active range.
func (f previewFilters) rangeLabel() string {
	switch f.Range {
	case "7d":
		return "last 7 days"
	case "30d":
		return "last 30 days"
	case "90d":
		return "last 90 days"
	default:
		return "all time"
	}
}

// rangeDays returns the inclusive number of days the window covers. The
// daily-bucket loops use this to size the x-axis. range=all is treated as
// 90 days for the daily charts (more would get too crowded).
func (f previewFilters) rangeDays() int {
	switch f.Range {
	case "7d":
		return 7
	case "30d":
		return 30
	case "90d":
		return 90
	default:
		return 90
	}
}

// computePreviewData runs every preview-dashboard query under the supplied
// filters and returns one assembled snapshot. Most queries mirror the existing
// statsData ones but with an additional OS/deploy WHERE fragment and a
// caller-controlled cutoff.
func (s *server) computePreviewData(ctx context.Context, f previewFilters) (*previewData, error) {
	now := time.Now().UTC()
	d := &previewData{Filters: f, RangeLabel: f.rangeLabel()}
	cutoff, hasCutoff := f.cutoff(now)
	extraWhere, extraArgs := f.whereClause()

	// Active in range. When range=all we count anyone who ever pinged.
	{
		var args []any
		q := `SELECT COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- extraWhere is built by previewFilters.whereClause() from a static set of "<col> = ?" fragments; user values are bound via extraArgs.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.ActiveRange); err != nil {
			return nil, err
		}
	}

	// All-time total. Range is intentionally ignored — that's the headline
	// "how many installs have we ever seen?" number. OS/deploy filters still
	// apply so the card matches the rest of the page.
	{
		q := `SELECT COUNT(*) FROM installs WHERE 1=1` + extraWhere
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, extraArgs...).Scan(&d.Total); err != nil {
			return nil, err
		}
	}

	// New installs in range.
	{
		var args []any
		q := `SELECT COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND first_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.NewRange); err != nil {
			return nil, err
		}
	}

	// Δ vs. previous equal-length window. Skip for range=all (no notion of
	// "previous" — the active count is monotonic over all-time).
	if hasCutoff {
		d.ShowDelta = true
		span := validPreviewRanges[f.Range]
		prevStart := cutoff.Add(-span)
		prevEnd := cutoff
		args := []any{prevStart, prevEnd}
		q := `SELECT COUNT(*) FROM installs WHERE last_seen >= ? AND last_seen < ?` + extraWhere
		args = append(args, extraArgs...)
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&d.ActivePrev); err != nil {
			return nil, err
		}
		d.DeltaActive = d.ActiveRange - d.ActivePrev
		if d.ActivePrev > 0 {
			d.DeltaActivePct = float64(d.DeltaActive) / float64(d.ActivePrev) * 100
		}
	}

	// Histograms (OS/arch/deploy/longevity) — each ranged + filtered.
	queryBuckets := func(col string) ([]statsBucket, error) {
		var args []any
		// #nosec G202 — col is a hard-coded literal from the caller.
		q := `SELECT ` + col + `, COUNT(*) FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		q += extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
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

	versions, err := queryBuckets("version")
	if err != nil {
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

	// Longevity buckets — reuse the same CASE expression as /stats, only
	// adapted to honour the preview's optional cutoff and OS/deploy filter.
	{
		var args []any
		q := `SELECT
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
			FROM installs WHERE 1=1`
		if hasCutoff {
			q += ` AND last_seen >= ?`
			args = append(args, cutoff)
		}
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		q += extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY bucket`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		lonMap := make(map[string]int)
		for rows.Next() {
			var bucket string
			var count int
			if err := rows.Scan(&bucket, &count); err != nil {
				return nil, err
			}
			lonMap[bucket] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, label := range []string{"< 1 week", "1–4 weeks", "1–3 months", "3+ months"} {
			d.Longevity = append(d.Longevity, statsBucket{Label: label, Count: lonMap[label]})
		}
	}

	// Daily activity over the range. Fill zero days so the line chart has a
	// continuous axis. When range=all we still cap the window at 90 days for
	// the chart — drawing 800 daily bars on a 600px-wide canvas is illegible.
	days := f.rangeDays()
	today := now.Truncate(24 * time.Hour)
	chartCutoff := today.AddDate(0, 0, -(days - 1))
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(last_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE last_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
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
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			d.Daily = append(d.Daily, dailyBucket{Day: day, Count: dayCount[key]})
		}
	}

	// New installs per day over the same window.
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(first_seen, 1, 10) AS day, COUNT(*) FROM installs WHERE first_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
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
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			d.DailyNew = append(d.DailyNew, dailyBucket{Day: day, Count: dayCount[key]})
		}
	}

	// Top-6 versions for the stacked area, ordered by overall count within the
	// active window (already sorted by queryBuckets).
	for i, v := range versions {
		if i >= 6 {
			break
		}
		d.TopVersions = append(d.TopVersions, v.Label)
	}

	// Per-day per-version active counts over the chart window. Reuses the
	// existing query shape but with the range-aware cutoff + OS/deploy filter.
	{
		args := []any{chartCutoff}
		// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
		q := `SELECT substr(last_seen, 1, 10) AS day, version, COUNT(*) FROM installs WHERE last_seen >= ?` + extraWhere
		args = append(args, extraArgs...)
		q += ` GROUP BY day, version ORDER BY day`
		// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		vtMap := make(map[string]map[string]int)
		for rows.Next() {
			var day, ver string
			var count int
			if err := rows.Scan(&day, &ver, &count); err != nil {
				return nil, err
			}
			if vtMap[day] == nil {
				vtMap[day] = make(map[string]int)
			}
			vtMap[day][ver] = count
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			vd := versionTrendDay{Day: day, Versions: vtMap[key]}
			if vd.Versions == nil {
				vd.Versions = make(map[string]int)
			}
			for _, cnt := range vd.Versions {
				vd.Total += cnt
			}
			d.VersionDays = append(d.VersionDays, vd)
		}
	}

	// Retention cohorts.
	cohorts, err := s.computeRetentionCohorts(ctx, f, now)
	if err != nil {
		return nil, err
	}
	d.Cohorts = cohorts

	return d, nil
}

// computeRetentionCohorts groups installs by ISO week of first_seen and counts
// how many were still pinging (last_seen ≥ first_seen + N days) at N ∈ {7,14,30}.
// Done in Go so the bucketing logic and "skip cohorts younger than 30 days"
// guard stays readable — one SQL query, all the math in memory.
//
// Cohort window: the same range as the rest of the page when a cutoff is set;
// when range=all, the trailing 26 weeks (≈6 months). Cohorts whose week ends
// less than 30 days ago are dropped because they can't have a 30-day data
// point yet, which would otherwise make the heatmap look broken.
func (s *server) computeRetentionCohorts(ctx context.Context, f previewFilters, now time.Time) ([]retentionCohort, error) {
	var cohortCutoff time.Time
	if c, ok := f.cutoff(now); ok {
		cohortCutoff = c
	} else {
		cohortCutoff = now.AddDate(0, 0, -26*7)
	}
	extraWhere, extraArgs := f.whereClause()

	args := []any{cohortCutoff}
	args = append(args, extraArgs...)
	// Pull every install whose first_seen is within the cohort window.
	// SQLite's substr() is fine for stable last_seen/first_seen formatting.
	// #nosec G202 -- extraWhere is static fragments; values bound via extraArgs.
	q := `SELECT substr(first_seen, 1, 10), substr(last_seen, 1, 10) FROM installs WHERE first_seen >= ?` + extraWhere
	// #nosec G202 G701 -- see whereClause; static fragments + ? placeholders.
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Bucket by ISO Monday of first_seen.
	type bucket struct {
		Size, R7, R14, R30 int
	}
	buckets := make(map[time.Time]*bucket)
	for rows.Next() {
		var firstStr, lastStr string
		if err := rows.Scan(&firstStr, &lastStr); err != nil {
			return nil, err
		}
		first, err1 := time.Parse("2006-01-02", firstStr)
		last, err2 := time.Parse("2006-01-02", lastStr)
		if err1 != nil || err2 != nil {
			continue
		}
		week := mondayOf(first)
		b := buckets[week]
		if b == nil {
			b = &bucket{}
			buckets[week] = b
		}
		b.Size++
		age := int(last.Sub(first).Hours() / 24)
		if age >= 7 {
			b.R7++
		}
		if age >= 14 {
			b.R14++
		}
		if age >= 30 {
			b.R30++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Drop cohorts whose week+30d hasn't passed yet — their R30 is structurally
	// 0 because no install in that cohort has had time to reach the milestone.
	maxWeek := now.AddDate(0, 0, -30)
	weeks := make([]time.Time, 0, len(buckets))
	for w := range buckets {
		if w.AddDate(0, 0, 6).Before(maxWeek) {
			weeks = append(weeks, w)
		}
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Before(weeks[j]) })

	out := make([]retentionCohort, 0, len(weeks))
	for _, w := range weeks {
		b := buckets[w]
		out = append(out, retentionCohort{
			WeekStart: w,
			Size:      b.Size,
			R7:        b.R7,
			R14:       b.R14,
			R30:       b.R30,
		})
	}
	return out, nil
}

// mondayOf returns the Monday (UTC midnight) of the ISO week containing t.
// time.Weekday() treats Sunday as 0; remap so Monday is 0 and subtract.
func mondayOf(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := int(t.Weekday()) // Sun=0..Sat=6
	if wd == 0 {
		wd = 7
	}
	return t.AddDate(0, 0, -(wd - 1))
}

// previewPayload is the JSON shape embedded in the page for Chart.js to read.
// Keeping it separate from previewData lets us strip server-side concerns
// (the *time.Time values, raw struct names) before serialisation.
type previewPayload struct {
	Labels        []string         `json:"labels"` // daily labels (Jan 2)
	Active        []int            `json:"active"` // line: daily active
	New           []int            `json:"new"`    // bar:  daily new
	OS            []labelCount     `json:"os"`
	Arch          []labelCount     `json:"arch"`
	Deploy        []labelCount     `json:"deploy"`
	Longevity     []labelCount     `json:"longevity"`
	TopVersions   []string         `json:"top_versions"`
	VersionSeries map[string][]int `json:"version_series"` // version → daily active across labels
	Cohorts       []cohortPayload  `json:"cohorts"`
}

type labelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type cohortPayload struct {
	WeekStart string  `json:"week_start"` // ISO date of Monday
	Size      int     `json:"size"`
	R7        int     `json:"r7"`
	R14       int     `json:"r14"`
	R30       int     `json:"r30"`
	R7Pct     float64 `json:"r7_pct"`
	R14Pct    float64 `json:"r14_pct"`
	R30Pct    float64 `json:"r30_pct"`
}

// buildPreviewPayload reshapes previewData into the JSON payload embedded in
// the rendered page. Done as a separate step so the SQL layer stays free of
// presentation concerns.
func buildPreviewPayload(d *previewData) previewPayload {
	p := previewPayload{
		TopVersions:   d.TopVersions,
		VersionSeries: make(map[string][]int, len(d.TopVersions)+1),
	}
	for _, day := range d.Daily {
		p.Labels = append(p.Labels, day.Day.Format("Jan 2"))
		p.Active = append(p.Active, day.Count)
	}
	for _, day := range d.DailyNew {
		p.New = append(p.New, day.Count)
	}
	for _, b := range d.OS {
		p.OS = append(p.OS, labelCount(b))
	}
	for _, b := range d.Arch {
		p.Arch = append(p.Arch, labelCount(b))
	}
	for _, b := range d.Deploy {
		p.Deploy = append(p.Deploy, labelCount(b))
	}
	for _, b := range d.Longevity {
		p.Longevity = append(p.Longevity, labelCount(b))
	}

	// Version-mix stacked area: one series per top version + "(other)".
	for _, v := range d.TopVersions {
		p.VersionSeries[v] = make([]int, 0, len(d.VersionDays))
	}
	otherKey := "(other)"
	p.VersionSeries[otherKey] = make([]int, 0, len(d.VersionDays))
	topSet := make(map[string]struct{}, len(d.TopVersions))
	for _, v := range d.TopVersions {
		topSet[v] = struct{}{}
	}
	for _, day := range d.VersionDays {
		other := 0
		for _, v := range d.TopVersions {
			p.VersionSeries[v] = append(p.VersionSeries[v], day.Versions[v])
		}
		for ver, cnt := range day.Versions {
			if _, ok := topSet[ver]; !ok {
				other += cnt
			}
		}
		p.VersionSeries[otherKey] = append(p.VersionSeries[otherKey], other)
	}

	for _, c := range d.Cohorts {
		var r7p, r14p, r30p float64
		if c.Size > 0 {
			r7p = float64(c.R7) / float64(c.Size) * 100
			r14p = float64(c.R14) / float64(c.Size) * 100
			r30p = float64(c.R30) / float64(c.Size) * 100
		}
		p.Cohorts = append(p.Cohorts, cohortPayload{
			WeekStart: c.WeekStart.Format("2006-01-02"),
			Size:      c.Size,
			R7:        c.R7,
			R14:       c.R14,
			R30:       c.R30,
			R7Pct:     r7p,
			R14Pct:    r14p,
			R30Pct:    r30p,
		})
	}

	return p
}

// previewPageTemplate is the HTML/CSS/JS for /stats/preview. Filters render
// the <option selected> attribute server-side; Chart.js receives its data via
// a JSON <script type="application/json"> tag the inline JS reads on load.
//
// Token placeholders (replaced via strings.NewReplacer in handlePreviewPage):
//
//	{{HERO}}        — hero stats row HTML
//	{{COHORTS}}     — cohort table HTML
//	{{FILTERS}}     — filter form HTML
//	{{JSON}}        — JSON payload (already script-tag-safe)
//	{{GENERATED}}   — generated-at footer string
//	{{RANGELABEL}}  — human-readable range label, HTML-escaped
//
// Token-substitution avoids fighting fmt.Sprintf with all the `%` characters
// in the embedded CSS (width:100%) and JS (palette[i % palette.length]).
const previewPageTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bindery — Telemetry Preview</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100svh;
    background: #0f1117;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    padding: 2.5rem 1.5rem;
  }
  .container { max-width: 1080px; margin: 0 auto; }
  header { margin-bottom: 1.75rem; display: flex; align-items: flex-end; justify-content: space-between; flex-wrap: wrap; gap: 1rem; }
  header .titleblock h1 { font-size: 1.85rem; font-weight: 700; letter-spacing: -0.02em; margin: .35rem 0 .15rem; display: flex; align-items: center; gap: .65rem; }
  header .badge { display: inline-block; font-size: .65rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase; padding: .2rem .5rem; border-radius: 999px; background: #1e293b; color: #10b981; border: 1px solid #334155; }
  header a.back { color: #94a3b8; font-size: .85rem; text-decoration: none; }
  header a.back:hover { color: #e2e8f0; }
  header .compare { color: #94a3b8; font-size: .8rem; }
  header .compare a { color: #10b981; text-decoration: none; }
  header .compare a:hover { text-decoration: underline; }

  form.filters { display: flex; gap: .75rem; flex-wrap: wrap; margin-bottom: 2rem; background: #1e293b; border: 1px solid #334155; padding: .85rem 1rem; border-radius: 10px; align-items: center; }
  form.filters label { color: #94a3b8; font-size: .78rem; text-transform: uppercase; letter-spacing: .08em; margin-right: .3rem; }
  form.filters select { background: #0f1117; color: #e2e8f0; border: 1px solid #334155; padding: .4rem .65rem; border-radius: 6px; font-size: .85rem; }
  form.filters .field { display: flex; align-items: center; gap: .35rem; }
  form.filters noscript button { background: #10b981; color: #fff; border: 0; padding: .4rem .85rem; border-radius: 6px; font-weight: 600; cursor: pointer; }

  .hero { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 2rem; }
  .hero .stat { padding: 1.1rem 1.3rem; background: #1e293b; border: 1px solid #334155; border-radius: 10px; }
  .hero .stat .num { font-size: 2rem; font-weight: 700; color: #10b981; line-height: 1; font-variant-numeric: tabular-nums; }
  .hero .stat .label { color: #94a3b8; font-size: .8rem; margin-top: .4rem; text-transform: uppercase; letter-spacing: .06em; }
  .hero .stat.delta .num.up { color: #10b981; }
  .hero .stat.delta .num.down { color: #ef4444; }
  .hero .stat.delta .sub { color: #94a3b8; font-size: .8rem; margin-top: .25rem; }

  section { margin-bottom: 2rem; }
  section > h2 { font-size: .78rem; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; color: #94a3b8; margin-bottom: .75rem; }
  .panel { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 1rem 1.1rem; }
  .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; }
  .grid-3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1rem; }

  canvas { width: 100% !important; height: auto !important; }
  .canvas-wrap { position: relative; height: 260px; }
  .canvas-wrap.tall { height: 320px; }
  .canvas-wrap.short { height: 220px; }

  table.cohorts { width: 100%; border-collapse: collapse; font-size: .85rem; }
  table.cohorts th { text-align: left; color: #94a3b8; font-weight: 600; padding: .45rem .65rem; border-bottom: 1px solid #334155; font-size: .72rem; text-transform: uppercase; letter-spacing: .08em; }
  table.cohorts td { padding: .45rem .65rem; color: #cbd5e1; font-variant-numeric: tabular-nums; border-bottom: 1px solid #1e293b; }
  table.cohorts td.pct { text-align: right; border-radius: 4px; font-weight: 600; color: #0f1117; }
  table.cohorts td.empty { color: #475569; text-align: right; }

  footer { margin-top: 2.5rem; color: #64748b; font-size: .75rem; text-align: center; line-height: 1.5; }

  @media (max-width: 720px) {
    .hero { grid-template-columns: 1fr 1fr; }
    .grid-2, .grid-3 { grid-template-columns: 1fr; }
    header { align-items: flex-start; }
  }
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="titleblock">
      <a class="back" href="/">← Bindery</a>
      <h1>Telemetry · Preview <span class="badge">beta</span></h1>
      <p class="compare">Richer charts and filters. <a href="/stats">Old dashboard →</a></p>
    </div>
  </header>

  {{FILTERS}}

  {{HERO}}

  <section>
    <h2>Daily activity ({{RANGELABEL}})</h2>
    <div class="panel"><div class="canvas-wrap tall"><canvas id="chart-active"></canvas></div></div>
  </section>

  <section>
    <h2>New installs per day</h2>
    <div class="panel"><div class="canvas-wrap"><canvas id="chart-new"></canvas></div></div>
  </section>

  <section>
    <h2>Version adoption (active installs per version, stacked)</h2>
    <div class="panel"><div class="canvas-wrap tall"><canvas id="chart-versions"></canvas></div></div>
  </section>

  <section>
    <h2>Distribution</h2>
    <div class="grid-3">
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-os"></canvas></div>
      </div>
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-arch"></canvas></div>
      </div>
      <div class="panel">
        <div class="canvas-wrap short"><canvas id="chart-deploy"></canvas></div>
      </div>
    </div>
  </section>

  <section>
    <h2>Install longevity</h2>
    <div class="panel"><div class="canvas-wrap short"><canvas id="chart-longevity"></canvas></div></div>
  </section>

  <section>
    <h2>Retention cohorts (% of week-N installs still pinging)</h2>
    <div class="panel">{{COHORTS}}</div>
  </section>

  <footer>
    Generated {{GENERATED}}. Data refreshes on every page load.<br>
    Anonymous install counts only — opaque install UUID, version, OS/arch, deploy method. No identifying information collected.
  </footer>
</div>

<script type="application/json" id="telemetry-data">{{JSON}}</script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.6/dist/chart.umd.min.js" integrity="sha384-Sse/HDqcypGpyTDpvZOJNnG0TT3feGQUkF9H+mnRvic+LjR+K1NhTt8f51KIQ3v3" crossorigin="anonymous"></script>
<script>
(function() {
  var raw = document.getElementById('telemetry-data').textContent;
  var data = JSON.parse(raw);
  var palette = ['#10b981','#3b82f6','#f59e0b','#a855f7','#ef4444','#06b6d4','#ec4899','#84cc16'];
  var otherColour = '#475569';
  var gridColour = '#334155';
  var tickColour = '#94a3b8';

  Chart.defaults.color = tickColour;
  Chart.defaults.borderColor = gridColour;
  Chart.defaults.font.family = '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';

  var axes = {
    x: { grid: { color: gridColour, drawOnChartArea: false }, ticks: { color: tickColour, maxRotation: 0, autoSkip: true, maxTicksLimit: 12 } },
    y: { grid: { color: gridColour }, ticks: { color: tickColour, precision: 0 }, beginAtZero: true }
  };

  // Daily active line.
  new Chart(document.getElementById('chart-active'), {
    type: 'line',
    data: {
      labels: data.labels,
      datasets: [{ label: 'Active', data: data.active, borderColor: '#10b981', backgroundColor: 'rgba(16,185,129,0.15)', fill: true, tension: 0.25, pointRadius: 0, borderWidth: 2 }]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });

  // New installs bar.
  new Chart(document.getElementById('chart-new'), {
    type: 'bar',
    data: { labels: data.labels, datasets: [{ label: 'New installs', data: data.new, backgroundColor: '#3b82f6' }] },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });

  // Stacked area for version adoption.
  var versionDatasets = [];
  data.top_versions.forEach(function(ver, i) {
    versionDatasets.push({
      label: ver,
      data: data.version_series[ver] || [],
      borderColor: palette[i % palette.length],
      backgroundColor: palette[i % palette.length],
      fill: true,
      pointRadius: 0,
      tension: 0.2,
      borderWidth: 1
    });
  });
  if (data.version_series['(other)']) {
    versionDatasets.push({
      label: '(other)',
      data: data.version_series['(other)'],
      borderColor: otherColour,
      backgroundColor: otherColour,
      fill: true,
      pointRadius: 0,
      tension: 0.2,
      borderWidth: 1
    });
  }
  new Chart(document.getElementById('chart-versions'), {
    type: 'line',
    data: { labels: data.labels, datasets: versionDatasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: { legend: { position: 'bottom', labels: { color: tickColour, boxWidth: 10 } } },
      scales: {
        x: axes.x,
        y: Object.assign({}, axes.y, { stacked: true })
      },
      interaction: { mode: 'index', intersect: false }
    }
  });

  function doughnut(canvasId, items) {
    if (!items || items.length === 0) return;
    var labels = items.map(function(x) { return x.label; });
    var counts = items.map(function(x) { return x.count; });
    var colours = items.map(function(_, i) { return palette[i % palette.length]; });
    new Chart(document.getElementById(canvasId), {
      type: 'doughnut',
      data: { labels: labels, datasets: [{ data: counts, backgroundColor: colours, borderColor: '#0f1117', borderWidth: 2 }] },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { position: 'bottom', labels: { color: tickColour, boxWidth: 10, padding: 8, font: { size: 11 } } }, title: { display: true, text: canvasId.replace('chart-', '').toUpperCase(), color: tickColour, font: { size: 11, weight: '600' } } }
      }
    });
  }
  doughnut('chart-os', data.os);
  doughnut('chart-arch', data.arch);
  doughnut('chart-deploy', data.deploy);

  // Longevity bar.
  new Chart(document.getElementById('chart-longevity'), {
    type: 'bar',
    data: {
      labels: data.longevity.map(function(x) { return x.label; }),
      datasets: [{ data: data.longevity.map(function(x) { return x.count; }), backgroundColor: ['#ef4444', '#f59e0b', '#3b82f6', '#10b981'] }]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: axes }
  });
})();
</script>
</body>
</html>`

// renderPreviewHero builds the 4 (or 3 when range=all) stat cards.
func renderPreviewHero(d *previewData) string {
	var sb strings.Builder
	sb.WriteString(`<section class="hero-row"><div class="hero">`)
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">Active (%s)</div></div>`,
		d.ActiveRange, html.EscapeString(d.RangeLabel))
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">Total installs</div></div>`,
		d.Total)
	fmt.Fprintf(&sb,
		`<div class="stat"><div class="num">%d</div><div class="label">New (%s)</div></div>`,
		d.NewRange, html.EscapeString(d.RangeLabel))
	if d.ShowDelta {
		dir := "up"
		sign := "+"
		arrow := "↑"
		if d.DeltaActive < 0 {
			dir = "down"
			sign = "−"
			arrow = "↓"
		}
		// Use absolute values for display so the sign is rendered exactly once.
		abs := d.DeltaActive
		if abs < 0 {
			abs = -abs
		}
		absPct := d.DeltaActivePct
		if absPct < 0 {
			absPct = -absPct
		}
		fmt.Fprintf(&sb,
			`<div class="stat delta"><div class="num %s">%s%d</div><div class="sub">%s %.1f%% vs. previous %s</div></div>`,
			dir, sign, abs, arrow, absPct, html.EscapeString(d.RangeLabel))
	}
	sb.WriteString(`</div></section>`)
	return sb.String()
}

// renderPreviewFilters renders the three-dropdown filter form. Selected option
// is set from f. Form submits via GET so query params drive the page; a tiny
// noscript fallback lets users without JS still apply filters (everything is
// server-side anyway).
func renderPreviewFilters(f previewFilters) string {
	opt := func(value, label, selected string) string {
		sel := ""
		if value == selected {
			sel = " selected"
		}
		return fmt.Sprintf(`<option value="%s"%s>%s</option>`,
			html.EscapeString(value), sel, html.EscapeString(label))
	}
	var sb strings.Builder
	sb.WriteString(`<form class="filters" method="get" action="/stats/preview" onchange="this.submit()">`)
	sb.WriteString(`<div class="field"><label>Range</label><select name="range">`)
	sb.WriteString(opt("7d", "Last 7 days", f.Range))
	sb.WriteString(opt("30d", "Last 30 days", f.Range))
	sb.WriteString(opt("90d", "Last 90 days", f.Range))
	sb.WriteString(opt("all", "All time", f.Range))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<div class="field"><label>OS</label><select name="os">`)
	sb.WriteString(opt("all", "All", f.OS))
	sb.WriteString(opt("linux", "Linux", f.OS))
	sb.WriteString(opt("darwin", "macOS", f.OS))
	sb.WriteString(opt("windows", "Windows", f.OS))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<div class="field"><label>Deploy</label><select name="deploy">`)
	sb.WriteString(opt("all", "All", f.Deploy))
	sb.WriteString(opt("docker", "Docker", f.Deploy))
	sb.WriteString(opt("binary", "Binary", f.Deploy))
	sb.WriteString(opt("kubernetes", "Kubernetes", f.Deploy))
	sb.WriteString(opt("helm", "Helm", f.Deploy))
	sb.WriteString(`</select></div>`)
	sb.WriteString(`<noscript><button type="submit">Apply</button></noscript>`)
	sb.WriteString(`</form>`)
	return sb.String()
}

// renderPreviewCohorts renders the retention-cohort table. Each percentage
// cell is given an inline background colour scaled from red (0%) to green
// (100%) so the table doubles as a quick-read heatmap.
func renderPreviewCohorts(cohorts []retentionCohort) string {
	if len(cohorts) == 0 {
		return `<p style="color:#94a3b8;font-size:.85rem;">No cohort data yet — need at least 30 days of pings to populate the heatmap.</p>`
	}
	var sb strings.Builder
	sb.WriteString(`<table class="cohorts"><thead><tr>`)
	sb.WriteString(`<th>Cohort week</th><th>Size</th><th>Day 7</th><th>Day 14</th><th>Day 30</th>`)
	sb.WriteString(`</tr></thead><tbody>`)
	for _, c := range cohorts {
		var r7p, r14p, r30p float64
		if c.Size > 0 {
			r7p = float64(c.R7) / float64(c.Size) * 100
			r14p = float64(c.R14) / float64(c.Size) * 100
			r30p = float64(c.R30) / float64(c.Size) * 100
		}
		fmt.Fprintf(&sb,
			`<tr><td>%s</td><td>%d</td>%s%s%s</tr>`,
			c.WeekStart.Format("Jan 2"), c.Size,
			cohortCell(c.R7, r7p, c.Size),
			cohortCell(c.R14, r14p, c.Size),
			cohortCell(c.R30, r30p, c.Size),
		)
	}
	sb.WriteString(`</tbody></table>`)
	return sb.String()
}

// cohortCell renders one retention table cell. Empty cohorts (size 0) get a
// muted dash; populated cells get a heatmap background interpolating between
// red (#ef4444) at 0% and green (#10b981) at 100%.
func cohortCell(count int, pct float64, size int) string {
	if size == 0 {
		return `<td class="empty">—</td>`
	}
	// Interpolate red → amber → green. Simple two-segment lerp.
	var bg string
	switch {
	case pct >= 50:
		bg = lerpHex("#f59e0b", "#10b981", (pct-50)/50)
	default:
		bg = lerpHex("#ef4444", "#f59e0b", pct/50)
	}
	return fmt.Sprintf(`<td class="pct" style="background:%s">%.0f%% <small style="font-weight:500;opacity:.7">(%d)</small></td>`,
		bg, pct, count)
}

// lerpHex linearly interpolates between two `#rrggbb` colour strings. Fraction
// is clamped to [0, 1].
func lerpHex(a, b string, fraction float64) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	ar, ag, ab := hexRGB(a)
	br, bg, bb := hexRGB(b)
	r := int(float64(ar) + (float64(br)-float64(ar))*fraction)
	g := int(float64(ag) + (float64(bg)-float64(ag))*fraction)
	bl := int(float64(ab) + (float64(bb)-float64(ab))*fraction)
	return fmt.Sprintf("#%02x%02x%02x", r, g, bl)
}

func hexRGB(s string) (int, int, int) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0
	}
	var r, g, b int
	_, _ = fmt.Sscanf(s[1:], "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

// handlePreviewPage renders /stats/preview. Reads filters from query string,
// runs computePreviewData, marshals the JSON payload, and writes the template
// with both the server-rendered HTML pieces (hero/cohorts/filters) and the
// JSON blob that Chart.js consumes client-side.
func (s *server) handlePreviewPage(w http.ResponseWriter, r *http.Request) {
	filters := parsePreviewFilters(r.URL.Query())
	d, err := s.computePreviewData(r.Context(), filters)
	if err != nil {
		slog.Error("preview page: compute", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	payload := buildPreviewPayload(d)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Error("preview page: marshal", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Defensive: escape `</script>` so a future label embedded in JSON can't
	// break out of the <script type="application/json"> island. json.Marshal
	// does not escape forward slashes by default.
	safeJSON := strings.ReplaceAll(string(payloadJSON), "</", `<\/`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The site-wide CSP set in secureHeaders blocks all scripts. The preview
	// page is intentionally the only route that loads a CDN script + inline
	// setup, so we widen the policy *only* for this response. The middleware
	// runs first; this Set replaces its value before WriteHeader is called.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src https:; style-src 'unsafe-inline'; "+
			"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
			"connect-src 'self'")

	page := strings.NewReplacer(
		"{{HERO}}", renderPreviewHero(d),
		"{{COHORTS}}", renderPreviewCohorts(d.Cohorts),
		"{{FILTERS}}", renderPreviewFilters(filters),
		"{{JSON}}", safeJSON,
		"{{GENERATED}}", time.Now().UTC().Format("2006-01-02 15:04 MST"),
		"{{RANGELABEL}}", html.EscapeString(d.RangeLabel),
	).Replace(previewPageTemplate)
	if _, err := io.WriteString(w, page); err != nil {
		slog.Warn("preview page: write response", "error", err)
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
