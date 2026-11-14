package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// registerHealthRoutes mounts the two probe routes under /api/v1. Both are
// listed in auth.AllowUnauthPath and auth.RequesterAllowList, so they answer
// without credentials and for every role.
//
//   - GET /health is static: it proves the process is serving HTTP and
//     nothing more. Liveness probes and the Docker HEALTHCHECK use it.
//   - GET /health/ready also round trips the database. The chart's
//     readinessProbe uses it.
func registerHealthRoutes(r chi.Router, database *sql.DB) {
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
	})
	r.Get("/health/ready", readinessHandler(database))
}

// readinessTimeout bounds the database round trip behind
// GET /api/v1/health/ready, inside the chart's 3 s probe timeout. A variable
// only so a test can shorten it.
var readinessTimeout = 2 * time.Second

// readinessHandler answers GET /api/v1/health/ready. It returns 503 only when
// the database reports a real error (closed handle, unreadable file, I/O or
// corruption) and 200 otherwise, adding "busy":true when the check could not
// finish in time. The pool holds a single connection (db.SetMaxOpenConns(1)),
// so a library scan, a big import or a VACUUM can hold it well past any probe
// budget while the app is fine. Failing readiness then would pull the pod out
// of the Service and show users a 503 for no reason.
//
// It is unauthenticated (auth.AllowUnauthPath) so the kubelet can call it,
// which is why the body is only ever a fixed status word: the cause goes to the
// log, never to the caller.
//
// The chart's readinessProbe uses this route; the livenessProbe and the Docker
// HEALTHCHECK stay on the static /api/v1/health on purpose. A liveness probe
// that touches the database restarts the pod whenever the disk is slow or a
// long write holds the connection, and a restart fixes neither: it only drops
// in flight work and adds a SQLite recovery on top.
func readinessHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		err := checkDatabaseReady(ctx, database)
		switch {
		case err == nil:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case databaseBusy(err):
			slog.Debug("readiness check: database busy, reporting ready", "error", err)
			_, _ = w.Write([]byte(`{"status":"ok","busy":true}`))
		default:
			slog.Warn("readiness check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
		}
	}
}

func checkDatabaseReady(ctx context.Context, database *sql.DB) error {
	if err := database.PingContext(ctx); err != nil {
		return err
	}
	var one int
	return database.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// databaseBusy reports whether err means "busy" rather than "broken".
//
// database/sql returns the context's own error, unwrapped, when the deadline
// passes while a call waits for a pool connection (DB.conn), so
// context.DeadlineExceeded is the pool wait timeout. A closed handle is the
// distinct "sql: database is closed" error and anything else came from the
// driver. From modernc.org/sqlite, SQLITE_BUSY and SQLITE_LOCKED mean another
// writer holds the file, and SQLITE_INTERRUPT is the driver cancelling the
// statement when the deadline fired after it got the connection. Those are
// busy too. context.Canceled on its own is not: it means the caller went away,
// which says nothing about the database.
func databaseBusy(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_INTERRUPT:
			return true
		}
	}
	return false
}
