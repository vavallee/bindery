package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
