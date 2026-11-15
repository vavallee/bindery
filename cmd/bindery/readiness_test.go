package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

func TestReadinessHandler_OKWithWorkingDB(t *testing.T) {
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	rec := httptest.NewRecorder()
	readinessHandler(conn)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("body = %s", got)
	}
}

// TestReadinessHandler_ReadyWhileConnectionHeld: the pool has one connection,
// and a long scan, import or VACUUM can hold it past the probe's budget. That
// is a busy database, not a broken one, so the pod must stay in the Service.
func TestReadinessHandler_ReadyWhileConnectionHeld(t *testing.T) {
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	prev := readinessTimeout
	readinessTimeout = 100 * time.Millisecond
	t.Cleanup(func() { readinessTimeout = prev })

	// Take the pool's only connection and keep it well past the timeout.
	held, err := conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	rec := httptest.NewRecorder()
	readinessHandler(conn)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 while the connection is merely busy (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok","busy":true}` {
		t.Errorf("body = %s", got)
	}
}

func TestReadinessHandler_UnavailableWithClosedDB(t *testing.T) {
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	rec := httptest.NewRecorder()
	readinessHandler(conn)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	// Unauthenticated route: the cause must not reach the caller.
	if got := rec.Body.String(); got != `{"status":"unavailable"}` {
		t.Errorf("body = %s, want only the generic status", got)
	}
}

// TestHealthRoutes_NoAuthNeeded serves the probe routes behind the real
// useAPIAuth stack with auth enabled and an admin account present, and calls
// them with no credentials at all, the way the kubelet does.
func TestHealthRoutes_NoAuthNeeded(t *testing.T) {
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	secret := strings.Repeat("s", 32)
	for k, v := range map[string]string{
		api.SettingAuthSessionSecret: secret,
		api.SettingAuthMode:          string(auth.ModeEnabled),
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := users.Create(ctx, "admin", "x"); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := users.Create(ctx, "reader", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.SetRole(ctx, reader.ID, auth.RoleRequester); err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{settings: settings, users: users}

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		registerHealthRoutes(r, conn)
		r.Get("/setting", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})

	get := func(path string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Auth is live: an ordinary route refuses an anonymous caller.
	if rec := get("/api/v1/setting", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /setting = %d, want 401 (auth stack not enforcing)", rec.Code)
	}
	for _, p := range []string{"/api/v1/health", "/api/v1/health/ready"} {
		if rec := get(p, nil); rec.Code != http.StatusOK {
			t.Errorf("anonymous %s = %d, want 200 (body %s)", p, rec.Code, rec.Body.String())
		}
	}

	// A signed in requester is held to the allow list; the probe is on it.
	v, err := auth.SignSessionWithEpoch([]byte(secret), reader.ID, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: v}
	if rec := get("/api/v1/health/ready", cookie); rec.Code != http.StatusOK {
		t.Errorf("requester /health/ready = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDatabaseBusy(t *testing.T) {
	for _, c := range []struct {
		err  error
		busy bool
	}{
		{context.DeadlineExceeded, true},
		{fmt.Errorf("ping: %w", context.DeadlineExceeded), true},
		{context.Canceled, false},
		{errors.New("sql: database is closed"), false},
		{errors.New("disk I/O error"), false},
	} {
		if got := databaseBusy(c.err); got != c.busy {
			t.Errorf("databaseBusy(%v) = %v, want %v", c.err, got, c.busy)
		}
	}
}

func TestReadinessCache_OneCheckPerWindow(t *testing.T) {
	var calls atomic.Int32
	rc := &readinessCache{check: func(context.Context) error {
		calls.Add(1)
		return nil
	}}
	for i := 0; i < 5; i++ {
		if err := rc.result(); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("check ran %d times within the cache window, want 1", n)
	}
	// Once the window has passed the next call checks again.
	rc.mu.Lock()
	rc.at = time.Now().Add(-2 * readinessCacheTTL)
	rc.mu.Unlock()
	_ = rc.result()
	if n := calls.Load(); n != 2 {
		t.Fatalf("check ran %d times after the window expired, want 2", n)
	}
}

// TestDatabaseBusy_RealSQLiteBusy gets a genuine SQLITE_BUSY out of the
// driver: one connection holds an exclusive lock, a second handle with no
// busy timeout reads the schema.
func TestDatabaseBusy_RealSQLiteBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	ctx := context.Background()
	lc, err := locker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lc.Close() }()
	if _, err := lc.ExecContext(ctx, "CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	if _, err := lc.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = lc.ExecContext(ctx, "ROLLBACK") }()

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	var n int
	err = other.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&n)
	if err == nil {
		t.Fatal("expected SQLITE_BUSY while another connection holds an exclusive lock")
	}
	if !databaseBusy(err) {
		t.Fatalf("databaseBusy(%v) = false, want true for a driver SQLITE_BUSY", err)
	}
}
