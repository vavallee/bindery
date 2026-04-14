package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// newAuthFixture spins up an in-memory DB, the auth repos, a seeded session
// secret, and an AuthHandler wired with a fresh rate limiter. All tests share
// this shape — the auth handler's surface is wide enough that we'd otherwise
// re-inline the scaffolding in every test.
func newAuthFixture(t *testing.T) (*AuthHandler, *db.UserRepo, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)
	ctx := context.Background()
	// Session secret must exist before any issueSession call — production
	// seeds this at bootstrap, tests must do the same or verification fails.
	if err := settings.Set(ctx, SettingAuthSessionSecret, "test-secret-32-bytes-long-enough"); err != nil {
		t.Fatal(err)
	}
	lim := auth.NewLoginLimiter(5, 15*time.Minute)
	return NewAuthHandler(users, settings, lim), users, settings, ctx
}

func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

// TestStatus_SetupRequired confirms the first-load contract: zero users →
// setupRequired=true so the UI redirects to /setup. The auth mode falls back
// to "enabled" when unset.
func TestStatus_SetupRequired(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.SetupRequired {
		t.Error("expected setupRequired=true")
	}
	if resp.Authenticated {
		t.Error("expected authenticated=false before any user exists")
	}
	if resp.Mode != string(auth.ModeEnabled) {
		t.Errorf("expected mode=enabled default, got %q", resp.Mode)
	}
}

// TestSetup_CreatesFirstAdmin verifies the /setup flow: creates a user,
// issues a session cookie, and rejects subsequent setup attempts.
func TestSetup_CreatesFirstAdmin(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/setup",
		jsonBody(t, setupRequest{Username: "admin", Password: "hunter2hunter2"}))
	rec := httptest.NewRecorder()
	h.Setup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// User exists in DB.
	u, err := users.GetByUsername(ctx, "admin")
	if err != nil || u == nil {
		t.Fatalf("expected user created, got u=%v err=%v", u, err)
	}
	// Session cookie issued.
	var haveCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			haveCookie = true
		}
	}
	if !haveCookie {
		t.Error("expected session cookie on setup response")
	}

	// Second attempt must 409 — setup is one-shot.
	rec2 := httptest.NewRecorder()
	h.Setup(rec2, httptest.NewRequest(http.MethodPost, "/auth/setup",
		jsonBody(t, setupRequest{Username: "other", Password: "password1"})))
	if rec2.Code != http.StatusConflict {
		t.Errorf("expected 409 on second setup, got %d", rec2.Code)
	}
}

// TestSetup_RejectsShortPassword enforces the 8-char minimum that the frontend
// also checks. A server-side backstop matters because the UI rule is advisory.
func TestSetup_RejectsShortPassword(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.Setup(rec, httptest.NewRequest(http.MethodPost, "/auth/setup",
		jsonBody(t, setupRequest{Username: "admin", Password: "short"})))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for short password, got %d", rec.Code)
	}
}

// TestLogin_Success verifies the happy path: valid creds → 200 + signed cookie.
func TestLogin_Success(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	// Seed a user manually (skipping Setup to isolate Login's logic).
	hash, _ := auth.HashPassword("hunter2hunter2")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "hunter2hunter2"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var haveCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			haveCookie = true
		}
	}
	if !haveCookie {
		t.Error("expected session cookie")
	}
}

// TestLogin_WrongPassword must 401 and not leak whether the username exists.
// The rate limiter should also record this failure.
func TestLogin_WrongPassword(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("correct-password")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "wrong"})))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestLogin_UnknownUser must also 401 with the same message as WrongPassword
// to avoid username enumeration.
func TestLogin_UnknownUser(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "nope", Password: "whatever"})))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestLogin_RateLimit confirms the limiter blocks after 5 failures. This is
// the credential-stuffing defense — regression here opens the login endpoint
// to brute force on internet-exposed deployments.
func TestLogin_RateLimit(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("correct-password")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	// 5 wrong-password attempts from the same IP.
	for range 5 {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			jsonBody(t, loginRequest{Username: "admin", Password: "wrong"}))
		req.RemoteAddr = "203.0.113.9:1234"
		h.Login(httptest.NewRecorder(), req)
	}
	// 6th attempt → 429, even with the correct password.
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "correct-password"}))
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after 5 failures, got %d", rec.Code)
	}
}

// TestLogout clears the session cookie via MaxAge=-1.
func TestLogout(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.Logout(rec, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("expected cookie cleared with MaxAge<0")
	}
}

// TestChangePassword_SingleUserFallback exercises the bypass path for
// ModeDisabled / ModeLocalOnly: no context uid, but exactly one user exists,
// so the handler acts on that user. The *current* password must still verify.
func TestChangePassword_SingleUserFallback(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("old-password")
	u, err := users.Create(ctx, "admin", hash)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ChangePassword(rec, httptest.NewRequest(http.MethodPost, "/auth/password",
		jsonBody(t, changePasswordRequest{
			CurrentPassword: "old-password",
			NewPassword:     "new-password-2026",
		})))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify the new hash actually took.
	got, err := users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.VerifyPassword("new-password-2026", got.PasswordHash) {
		t.Error("new password does not verify after change")
	}
	if auth.VerifyPassword("old-password", got.PasswordHash) {
		t.Error("old password still verifies — expected replacement")
	}
}

// TestChangePassword_WrongCurrent keeps the old hash and returns 401.
func TestChangePassword_WrongCurrent(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("old-password")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, httptest.NewRequest(http.MethodPost, "/auth/password",
		jsonBody(t, changePasswordRequest{
			CurrentPassword: "WRONG",
			NewPassword:     "new-password-2026",
		})))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestSetMode persists the chosen mode and coerces unknown values to enabled.
func TestSetMode(t *testing.T) {
	h, _, settings, ctx := newAuthFixture(t)
	cases := []struct {
		in   string
		want auth.Mode
	}{
		{"disabled", auth.ModeDisabled},
		{"local-only", auth.ModeLocalOnly},
		{"enabled", auth.ModeEnabled},
		{"bogus", auth.ModeEnabled}, // fail-safe coercion
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.SetMode(rec, httptest.NewRequest(http.MethodPut, "/auth/mode",
			jsonBody(t, modeRequest{Mode: c.in})))
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: expected 200, got %d: %s", c.in, rec.Code, rec.Body.String())
		}
		s, err := settings.Get(ctx, SettingAuthMode)
		if err != nil {
			t.Fatal(err)
		}
		if s == nil || auth.Mode(s.Value) != c.want {
			t.Errorf("%q: stored mode=%v, want %v", c.in, s, c.want)
		}
	}
}

// TestRegenerateAPIKey rolls the stored key and returns the new value. A
// second call must produce a different value — if it didn't, rotating the
// key wouldn't invalidate compromised integrations.
func TestRegenerateAPIKey(t *testing.T) {
	h, _, settings, ctx := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.RegenerateAPIKey(rec, httptest.NewRequest(http.MethodPost, "/auth/apikey/regenerate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp1 map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp1); err != nil {
		t.Fatal(err)
	}
	k1 := resp1["apiKey"]
	if len(k1) < 32 {
		t.Errorf("expected ≥32-char hex key, got %q", k1)
	}
	// Stored in settings.
	s, _ := settings.Get(ctx, SettingAuthAPIKey)
	if s == nil || s.Value != k1 {
		t.Errorf("stored key mismatch: stored=%v returned=%q", s, k1)
	}

	// Second regen must differ.
	rec2 := httptest.NewRecorder()
	h.RegenerateAPIKey(rec2, httptest.NewRequest(http.MethodPost, "/auth/apikey/regenerate", nil))
	var resp2 map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["apiKey"] == k1 {
		t.Error("two regens produced the same key")
	}
}

// TestSetup_RejectsEmptyBody just confirms the 400 path for malformed JSON;
// a silent failure here would let a misbehaving client soft-crash setup.
func TestSetup_RejectsEmptyBody(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	rec := httptest.NewRecorder()
	h.Setup(rec, httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
