package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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
// GET /api/v1/health/ready. The pool holds a single connection
// (db.SetMaxOpenConns(1)), so the ping queues behind whatever query is running;
// two seconds rides out an ordinary write and still answers inside the chart's
// probe timeout.
const readinessTimeout = 2 * time.Second

// readinessHandler answers GET /api/v1/health/ready: 200 when the database
// answers a ping and a trivial SELECT within readinessTimeout, 503 otherwise.
// It is unauthenticated (auth.AllowUnauthPath) so the kubelet can call it,
// which is why the body is only ever a fixed status word: the cause goes to the
// log, never to the caller.
//
// The chart's readinessProbe uses this route; the livenessProbe and the Docker
// HEALTHCHECK stay on the static /api/v1/health on purpose. A liveness probe
// that touches the database restarts the pod whenever the disk is slow or a
// long write holds the connection, and a restart fixes neither: it only drops
// in flight work and adds a SQLite recovery on top. Readiness failing just
// takes the pod out of the Service until the database answers again.
func readinessHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := checkDatabaseReady(ctx, database); err != nil {
			slog.Warn("readiness check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

func checkDatabaseReady(ctx context.Context, database *sql.DB) error {
	if err := database.PingContext(ctx); err != nil {
		return err
	}
	var one int
	return database.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}
