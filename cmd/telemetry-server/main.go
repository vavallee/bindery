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
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// buildSHA is stamped at image build time with -X main.buildSHA=<git sha>.
// It exists because nothing could previously tell which build was serving:
// /health returns a bare 200 and the binary carries no version. The deployed
// image sat 41 days behind this tree once, a July build still serving after
// the August new_install logging landed, and the only way anyone noticed was
// grepping pod logs for a line that could not be there. The deployment lives
// in a private infrastructure repo, so this is the only handle this repo has
// on what is actually running. Reported on the token-gated /api/stats rather than public
// /health: a public exact build sha tells a stranger precisely which
// advisories apply, and the drift check that reads it already holds the
// stats token.
var buildSHA = "unknown"

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
	dbDir         string // directory holding the DB, the writable data volume
	latestVersion atomic.Pointer[string]
	statsToken    string
	limiter       *rateLimiter
	// trustedProxies are the peers whose X-Real-Ip / X-Forwarded-For are
	// believed for rate limiting. Empty means use the TCP peer address.
	trustedProxies []*net.IPNet
}

// latest returns the most recent published version the server advertises.
// Backed by an atomic so the background poller (runLatestVersionLoop) can
// update it while request handlers read it concurrently.
func (s *server) latest() string {
	if p := s.latestVersion.Load(); p != nil {
		return *p
	}
	return ""
}

// setLatest atomically replaces the advertised latest version.
func (s *server) setLatest(v string) { s.latestVersion.Store(&v) }

func main() {
	dbPath := env("DB_PATH", "/data/telemetry.db")
	addr := env("ADDR", ":8080")
	canonicalHost := env("CANONICAL_HOST", "")
	latestVersion := env("LATEST_VERSION", "v1.4.3")
	statsToken := env("STATS_TOKEN", "")
	releaseRepo := env("GITHUB_REPO", "vavallee/bindery")

	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
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

	// Add the features JSON column for per-subsystem adoption snapshots
	// (indexer count, ABS enabled, etc.). Stored as a JSON blob so the
	// schema can grow without further ALTERs; aggregate queries use
	// SQLite's json_extract. Older clients send no features field and the
	// row stores NULL, which surfaces as "not reported" on the dashboard.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE installs ADD COLUMN features TEXT`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		slog.Error("add features column", "error", err)
		os.Exit(1)
	}

	if err := createAggregateTables(context.Background(), db); err != nil {
		slog.Error("create aggregate table", "error", err)
		os.Exit(1)
	}

	// One-time ledger seed: on first boot with an empty daily_activity,
	// project each existing install onto its last_seen day so the 30-day
	// charts don't start from a cliff. Seeded days still undercount (the
	// per-day data was never recorded before the ledger existed), so the
	// epoch is stored in meta and the dashboard footnotes days before it.
	if err := seedActivityLedger(context.Background(), db); err != nil {
		slog.Error("seed activity ledger", "error", err)
		os.Exit(1)
	}

	// Boot-time sweep so dashboards on a fresh process are clean immediately;
	// the nightly cron below keeps them clean from then on.
	if _, err := sweepStaleAndDev(context.Background(), db); err != nil {
		slog.Error("startup sweep", "error", err)
		os.Exit(1)
	}

	s := &server{
		db:         db,
		dbDir:      filepath.Dir(dbPath),
		statsToken: statsToken,
		// Each IP may ping at most once per hour.
		limiter: newRateLimiter(1*time.Hour, 5*time.Minute),
		// Forwarding headers are only trusted from these peers. Unset means
		// loopback and private ranges; set TRUSTED_PROXIES to a CIDR list to
		// narrow that, or to "none" to use the TCP peer only.
		trustedProxies: resolveTrustedProxies(env("TRUSTED_PROXIES", "")),
	}
	// Seed the advertised version from the env; the poller below overrides it
	// with the newest GitHub release once it can reach the API.
	s.setLatest(latestVersion)

	// Nightly retention job: drop rows whose last_seen is older than 60 days
	// (dormant installs that won't return) and any dev/test rows that older
	// clients still post. Runs once a day, fired immediately after startup
	// so the process doesn't have to wait 24 hours for first cleanup. The
	// 60-day window matches the active-install definition used downstream
	// (60-day evict means an install ID is either active or absent).
	go s.runRetentionLoop(context.Background())

	// Backfill the one historically-recoverable aggregate (new_installs by
	// day, computed from first_seen which never changes). active_day,
	// version splits, and feature adoption can't be backfilled because the
	// installs table holds only the most-recent ping per install; those
	// metrics begin accumulating from today's snapshot forward. Best-effort:
	// a failure here does not block startup.
	if err := s.backfillNewInstalls(context.Background()); err != nil {
		slog.Warn("aggregate backfill failed", "error", err)
	}

	// Optional one-time repair: when NEW_INSTALLS_SEED_PATH points at a
	// {day: count} JSON file, raise each day's new_installs to the seeded
	// value (never lowers). Used to restore historical new_installs that an
	// earlier, non-monotonic backfill eroded once installs aged past the
	// 60-day retention window. Aggregate-only data, idempotent, no-op when
	// the env var is unset; safe to leave mounted across restarts.
	if seedPath := env("NEW_INSTALLS_SEED_PATH", ""); seedPath != "" {
		if n, err := s.reconcileNewInstallsSeed(context.Background(), seedPath); err != nil {
			slog.Warn("new-installs seed reconcile failed", "path", seedPath, "error", err)
		} else {
			slog.Info("new-installs seed reconciled", "path", seedPath, "days_raised", n)
		}
	}

	// Optional one-time recovery: when LEDGER_BACKFILL_PATH points at a
	// (day, install_id, version) CSV (.gz supported), import historical
	// per-day activity into the ledger and re-derive those days' aggregate
	// rows. See loadLedgerBackfill for provenance and idempotency; no-op
	// when unset, safe to leave mounted across restarts.
	// A missing file is a normal state (the deployment mounts an optional
	// ConfigMap), so it skips silently rather than warning every boot.
	if backfillPath := env("LEDGER_BACKFILL_PATH", ""); backfillPath != "" {
		if n, err := s.loadLedgerBackfill(context.Background(), backfillPath); errors.Is(err, os.ErrNotExist) {
			// no backfill mounted — nothing to do
		} else if err != nil {
			slog.Warn("ledger backfill failed", "path", backfillPath, "error", err)
		} else {
			slog.Info("ledger backfill applied", "path", backfillPath, "rows_inserted", n)
		}
	}

	// Daily snapshot job for the aggregate tables. Mirrors the retention
	// loop's lifecycle: fires immediately, then every 24 hours. Each tick
	// snapshots the current state into today's row (UPSERT), so a process
	// restart can recover today's partial counts without manual intervention.
	go s.runAggregateLoop(context.Background())

	// Self-update the advertised "latest" version from the GitHub Releases API
	// so a new release propagates without a runner→cluster push (which times
	// out — the cluster API isn't reachable from GitHub Actions).
	go s.runLatestVersionLoop(context.Background(), releaseRepo)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /stats", s.handleStatsPage)
	mux.HandleFunc("GET /stats.json", s.handleStatsJSON)
	mux.HandleFunc("GET /stats/preview", s.handlePreviewPage)
	mux.HandleFunc("GET /telemetry-fields", s.handleTelemetryFields)
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
	slog.Info("telemetry-server starting", "addr", addr, "latest", latestVersion, "build", buildSHA)
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
