package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// A drift scan must not take the app's one pooled connection. Through the REAL
// auth middleware (which reads settings from that connection on every
// request) and a real database file: while a scan is held open, a settings
// read still completes promptly and a second POST is refused with 409 rather
// than waiting in the middleware and then running a second full scan.
func TestDBIntegrityScanDoesNotBlockTheApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	secret := strings.Repeat("s", 32)
	if err := settings.Set(ctx, api.SettingAuthSessionSecret, secret); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, api.SettingAuthMode, string(auth.ModeEnabled)); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, "admin", "x"); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{settings: settings, users: users}

	started := make(chan struct{})
	release := make(chan struct{})
	h := api.NewDBIntegrityHandler(conn, path, nil)
	h.SetScanHook(func() {
		close(started)
		<-release
	})

	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		registerDBIntegrityRoutes(r, h)
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})

	v, err := auth.SignSessionWithEpoch([]byte(secret), 1, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: v}
	csrf := auth.MakeCSRFToken([]byte(secret), cookie.Value)
	do := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		req.Host = "bindery:8787"
		req.AddCookie(cookie)
		if method != http.MethodGet {
			req.Header.Set("X-Requested-With", "bindery-ui")
			req.Header.Set("X-CSRF-Token", csrf)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	within := func(d time.Duration, name string, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
		case <-time.After(d):
			t.Fatalf("%s did not complete within %v while a scan was running", name, d)
		}
	}

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- do(http.MethodPost, "/api/v1/system/db-integrity") }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scan never started")
	}
	// From here the scan is in flight. Whatever happens, let it go.
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseOnce()

	within(2*time.Second, "a settings read on the shared pool", func() {
		if _, err := settings.Get(ctx, api.SettingAuthMode); err != nil {
			t.Error(err)
		}
	})
	within(2*time.Second, "an authenticated GET /health", func() {
		if rec := do(http.MethodGet, "/api/v1/health"); rec.Code != http.StatusNoContent {
			t.Errorf("health = %d", rec.Code)
		}
	})
	within(2*time.Second, "a second POST", func() {
		if rec := do(http.MethodPost, "/api/v1/system/db-integrity"); rec.Code != http.StatusConflict {
			t.Errorf("second POST = %d %s, want 409", rec.Code, rec.Body.String())
		}
	})

	releaseOnce()
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("first POST = %d %s", rec.Code, rec.Body.String())
	}
}
