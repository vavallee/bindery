package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
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
	if err := settings.Set(ctx, SettingAuthSessionSecret, "test-secret-32-bytes-long-enough"); err != nil { // gitleaks:allow
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
	// User exists in DB and is promoted to admin (regression: #321 — first-run
	// setup left the user with role="user", locking the operator out of every
	// admin-gated config page until they manually edited the database).
	u, err := users.GetByUsername(ctx, "admin")
	if err != nil || u == nil {
		t.Fatalf("expected user created, got u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("expected first-run user promoted to admin, got role=%q", u.Role)
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

// TestLogin_CookieMaxAge_Short verifies the regression fix for mobile session
// eviction: when rememberMe is false (the default), the response cookie must
// carry an explicit Max-Age matching SessionDurationShort. Without Max-Age the
// cookie reverts to a browser-session cookie, which iOS Safari and Android
// Chrome drop when the tab is backgrounded — users got logged out on app switch.
func TestLogin_CookieMaxAge_Short(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "hunter2hunter2", RememberMe: false})))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			got = c
		}
	}
	if got == nil {
		t.Fatal("expected session cookie")
		return
	}
	want := int(auth.SessionDurationShort.Seconds())
	if got.MaxAge != want {
		t.Errorf("MaxAge=%d, want %d (SessionDurationShort)", got.MaxAge, want)
	}
}

// TestLogin_CookieMaxAge_RememberMe verifies the long-lived branch sets both
// Max-Age (RFC 6265 preferred) and Expires (compat for stale clients) so a
// user who ticks "Remember me" gets the full 30-day window.
func TestLogin_CookieMaxAge_RememberMe(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "hunter2hunter2", RememberMe: true})))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			got = c
		}
	}
	if got == nil {
		t.Fatal("expected session cookie")
		return
	}
	want := int(auth.SessionDuration.Seconds())
	if got.MaxAge != want {
		t.Errorf("MaxAge=%d, want %d (SessionDuration)", got.MaxAge, want)
	}
	// Expires should be roughly now + SessionDuration (allow 1 minute slack
	// for slow CI). Belt-and-suspenders for clients that ignore Max-Age.
	wantExp := before.Add(auth.SessionDuration)
	if got.Expires.IsZero() {
		t.Error("expected Expires set on remember-me cookie")
	} else if delta := got.Expires.Sub(wantExp); delta < -time.Minute || delta > time.Minute {
		t.Errorf("Expires=%v, want ~%v (delta %v)", got.Expires, wantExp, delta)
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

// TestRotateSessionSecret_PersistsBothKeys verifies the rotation action moves
// the current secret into the previous slot and writes a fresh current secret,
// persisting both atomically.
func TestRotateSessionSecret_PersistsBothKeys(t *testing.T) {
	h, _, settings, ctx := newAuthFixture(t)

	before, _ := settings.Get(ctx, SettingAuthSessionSecret)
	if before == nil || before.Value == "" {
		t.Fatal("fixture should have seeded a current session secret")
	}

	rec := httptest.NewRecorder()
	h.RotateSessionSecret(rec, httptest.NewRequest(http.MethodPost, "/auth/session-secret/rotate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	cur, _ := settings.Get(ctx, SettingAuthSessionSecret)
	prev, _ := settings.Get(ctx, SettingAuthSessionSecretPrevious)
	if cur == nil || cur.Value == "" {
		t.Fatal("current secret must be set after rotation")
	}
	if prev == nil || prev.Value != before.Value {
		t.Fatalf("previous secret must equal the pre-rotation current; got %v want %q", prev, before.Value)
	}
	if cur.Value == before.Value {
		t.Fatal("current secret must change on rotation")
	}
	// The new current secret must clear the fail-closed minimum-length guard.
	if len(cur.Value) < 32 {
		t.Errorf("new current secret too short for minSecretLen: %d bytes", len(cur.Value))
	}
}

// TestRotateSessionSecret_OldCookieStillVerifies proves the rotation window:
// a session cookie signed under the just-rotated-out secret still verifies via
// the {current, previous} candidate set, while signing always uses the new
// current secret.
func TestRotateSessionSecret_OldCookieStillVerifies(t *testing.T) {
	h, _, _, ctx := newAuthFixture(t)

	oldSecret := h.sessionSecret(ctx)
	oldCookie, err := auth.SignSession(oldSecret, 7, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign old cookie: %v", err)
	}

	rec := httptest.NewRecorder()
	h.RotateSessionSecret(rec, httptest.NewRequest(http.MethodPost, "/auth/session-secret/rotate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d", rec.Code)
	}

	// Old cookie still verifies during the rotation window.
	if uid, err := auth.VerifySessionMulti(h.sessionSecrets(ctx), oldCookie); err != nil || uid != 7 {
		t.Fatalf("rotated-out cookie: uid=%d err=%v; want 7,nil", uid, err)
	}

	// A cookie freshly signed under the new current secret also verifies.
	newCookie, err := auth.SignSession(h.sessionSecret(ctx), 8, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign new cookie: %v", err)
	}
	if uid, err := auth.VerifySessionMulti(h.sessionSecrets(ctx), newCookie); err != nil || uid != 8 {
		t.Fatalf("post-rotation cookie: uid=%d err=%v; want 8,nil", uid, err)
	}
}

// TestRotateSessionSecret_StaleSecretRejected confirms a cookie signed with a
// secret that is neither current nor previous — e.g. one rotated out two
// rotations ago — is rejected. Verification must not be weakened.
func TestRotateSessionSecret_StaleSecretRejected(t *testing.T) {
	h, _, _, ctx := newAuthFixture(t)

	// A secret never installed on this server.
	staleSecret := []byte("stale-secret-never-installed-32b!")
	staleCookie, err := auth.SignSession(staleSecret, 9, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign stale cookie: %v", err)
	}

	// One rotation: candidate set is {new, original}. The stale secret is
	// neither, so its cookie must fail.
	rec := httptest.NewRecorder()
	h.RotateSessionSecret(rec, httptest.NewRequest(http.MethodPost, "/auth/session-secret/rotate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d", rec.Code)
	}
	if _, err := auth.VerifySessionMulti(h.sessionSecrets(ctx), staleCookie); err == nil {
		t.Fatal("cookie signed with a never-installed secret must be rejected")
	}

	// A second rotation drops the original secret out of the window entirely:
	// a cookie signed under the original is now also rejected.
	originalSecret := []byte("test-secret-32-bytes-long-enough") // gitleaks:allow — matches fixture seed
	originalCookie, err := auth.SignSession(originalSecret, 10, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sign original cookie: %v", err)
	}
	rec2 := httptest.NewRecorder()
	h.RotateSessionSecret(rec2, httptest.NewRequest(http.MethodPost, "/auth/session-secret/rotate", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second rotate: expected 200, got %d", rec2.Code)
	}
	if _, err := auth.VerifySessionMulti(h.sessionSecrets(ctx), originalCookie); err == nil {
		t.Fatal("cookie signed with the twice-rotated-out secret must be rejected")
	}
}

// TestSessionSecrets_SingleSecretWhenNoPrevious confirms that with no previous
// secret configured the candidate set is exactly {current} — behavior is
// identical to single-secret verification.
func TestSessionSecrets_SingleSecretWhenNoPrevious(t *testing.T) {
	h, _, _, ctx := newAuthFixture(t)
	got := h.sessionSecrets(ctx)
	if len(got) != 1 {
		t.Fatalf("with no previous secret, candidate set must have 1 entry, got %d", len(got))
	}
	if string(got[0]) != string(h.sessionSecret(ctx)) {
		t.Fatal("the single candidate must be the current secret")
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

// TestLogin_LocalAuthDisabled confirms POST /auth/login returns 403 when
// local auth is disabled — before even touching the rate limiter or DB.
func TestLogin_LocalAuthDisabled(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	h = h.WithLocalAuthEnabled(false)

	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "whatever"})))

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "local login is disabled" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

// TestStatus_LocalAuthEnabled_True verifies that a default handler reports
// localAuthEnabled=true in the /auth/status response.
func TestStatus_LocalAuthEnabled_True(t *testing.T) {
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
	if !resp.LocalAuthEnabled {
		t.Error("expected localAuthEnabled=true by default")
	}
}

// TestStatus_LocalAuthEnabled_False verifies that WithLocalAuthEnabled(false)
// propagates through to the /auth/status JSON response.
func TestStatus_LocalAuthEnabled_False(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	h = h.WithLocalAuthEnabled(false)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LocalAuthEnabled {
		t.Error("expected localAuthEnabled=false after WithLocalAuthEnabled(false)")
	}
}

// --- Wave 1 / Bundle C: session invalidation on password change --------------
//
// These tests pin the "log everyone out after a password change" contract
// (audit finding #893 sibling). The mechanism is the per-user session_epoch
// column: cookies carry the epoch under which they were minted, the
// middleware compares against the live column on every request, and
// UpdatePassword bumps the column. Verifying through the real auth.Middleware
// chain is the only way to assert the audit-blocker is closed end-to-end —
// poking ChangePassword in isolation would miss the cookie/epoch comparison.

// epochProvider is a real auth.Provider that defers to a UserRepo for the
// session_epoch (and role) so the middleware behaves exactly as in prod.
type epochProvider struct {
	users  *db.UserRepo
	secret []byte
}

func (p *epochProvider) Mode() auth.Mode                       { return auth.ModeEnabled }
func (p *epochProvider) APIKey() string                        { return "" }
func (p *epochProvider) SessionSecret() []byte                 { return p.secret }
func (p *epochProvider) SessionSecrets() [][]byte              { return [][]byte{p.secret} }
func (p *epochProvider) SetupRequired() bool                   { return false }
func (p *epochProvider) ProxyAuthHeader() string               { return "" }
func (p *epochProvider) ProxyAutoProvision() bool              { return false }
func (p *epochProvider) TrustedProxyCIDRs() []*net.IPNet       { return nil }
func (p *epochProvider) UserProvisioner() auth.UserProvisioner { return nil }
func (p *epochProvider) UserRole(ctx context.Context, id int64) string {
	u, _ := p.users.GetByID(ctx, id)
	if u == nil {
		return ""
	}
	return u.Role
}
func (p *epochProvider) UserSessionEpoch(ctx context.Context, id int64) int64 {
	e, _ := p.users.GetSessionEpoch(ctx, id)
	return e
}

// extractSessionCookie returns the session cookie set on the response, or nil
// if none. The auth handlers always reset the cookie on Login / password
// change, so a missing cookie there is a test-relevant failure.
func extractSessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

// loginAndGetCookie issues a Login through the handler and returns the
// session cookie. Fails the test if login does not succeed or no cookie is
// set. Tightly bound to TestSession_InvalidatedOn* helpers below.
func loginAndGetCookie(t *testing.T, h *AuthHandler, username, password string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: username, Password: password, RememberMe: true})))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status=%d body=%s", rec.Code, rec.Body.String())
	}
	c := extractSessionCookie(rec.Result())
	if c == nil || c.Value == "" {
		t.Fatal("login: no session cookie set")
	}
	return c
}

// TestSession_InvalidatedOnPasswordChange is the headline audit-fix test:
// a user logs in, captures their cookie, changes their own password, then
// the OLD cookie must no longer authenticate. Without the per-user epoch
// bump, a stolen pre-change cookie would keep working for the full 30-day
// TTL — exactly the failure mode this PR closes.
func TestSession_InvalidatedOnPasswordChange(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("old-password-1234")
	u, err := users.Create(ctx, "alice", hash)
	if err != nil {
		t.Fatal(err)
	}

	oldCookie := loginAndGetCookie(t, h, "alice", "old-password-1234")

	// Change the password while authenticated as alice. The handler reads
	// the uid from context, so drive it the same way the middleware would.
	cpReq := httptest.NewRequest(http.MethodPost, "/auth/password",
		jsonBody(t, changePasswordRequest{
			CurrentPassword: "old-password-1234",
			NewPassword:     "new-password-9876",
		}))
	cpReq = cpReq.WithContext(auth.WithUserID(cpReq.Context(), u.ID))
	cpRec := httptest.NewRecorder()
	h.ChangePassword(cpRec, cpReq)
	if cpRec.Code != http.StatusOK {
		t.Fatalf("change password: status=%d body=%s", cpRec.Code, cpRec.Body.String())
	}

	// Build the real middleware chain with a provider that reads the live
	// epoch — this mirrors production exactly. The handler the chain wraps
	// records whether the cookie authenticated.
	provider := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value)}
	var reached bool
	chain := auth.Middleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserIDFromContext(r.Context()) != 0 {
			reached = true
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Re-use the OLD cookie on a fresh request — must NOT authenticate.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author", nil)
	req.AddCookie(oldCookie)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if reached {
		t.Fatal("pre-password-change cookie still authenticated — the epoch bump did not invalidate it")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d; want 401 for stale cookie", rec.Code)
	}
}

// TestSession_InvalidatedOnAdminPasswordReset covers the admin-reset path:
// userB's own cookie must be rejected after an admin resets B's password.
// This is the multi-user case the audit specifically flagged — the admin
// reset is what an operator runs when responding to a suspected compromise.
func TestSession_InvalidatedOnAdminPasswordReset(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)

	// userA is the admin doing the reset; userB is the target.
	hashA, _ := auth.HashPassword("admin-password-12")
	if _, err := users.Create(ctx, "admin", hashA); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	hashB, _ := auth.HashPassword("user-b-password-1")
	userB, err := users.Create(ctx, "bob", hashB)
	if err != nil {
		t.Fatal(err)
	}

	// userB logs in and captures their cookie.
	bobCookie := loginAndGetCookie(t, h, "bob", "user-b-password-1")

	// Admin resets userB's password via the user-management handler. That
	// handler calls users.UpdatePassword, which bumps session_epoch in the
	// same UPDATE — no separate API call needed.
	umh := NewUserManagementHandler(users)
	resetReq := httptest.NewRequest(http.MethodPut,
		"/api/v1/auth/users/"+strconv.FormatInt(userB.ID, 10)+"/reset-password",
		jsonBody(t, map[string]string{"password": "reset-by-admin99"}))
	resetReq = withChiURLParam(resetReq, "id", strconv.FormatInt(userB.ID, 10))
	resetRec := httptest.NewRecorder()
	umh.ResetPassword(resetRec, resetReq)
	if resetRec.Code != http.StatusOK {
		t.Fatalf("admin reset: status=%d body=%s", resetRec.Code, resetRec.Body.String())
	}

	// Bob's old cookie must now be rejected by the real middleware chain.
	provider := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value)}
	var reached bool
	chain := auth.Middleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserIDFromContext(r.Context()) != 0 {
			reached = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author", nil)
	req.AddCookie(bobCookie)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if reached {
		t.Fatal("bob's pre-reset cookie still authenticated — admin reset must invalidate sessions")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d; want 401", rec.Code)
	}
}

// TestSession_NewCookieValidAfterPasswordChange confirms the partner contract:
// the browser that performed the password change is NOT logged out. The
// /auth/password response sets a fresh cookie carrying the post-bump epoch,
// and that cookie must authenticate on the next request. Without this the
// user who just changed their password would land on the login screen — bad
// UX and a regression that would silently revert the audit-fix in practice.
func TestSession_NewCookieValidAfterPasswordChange(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("old-password-1234")
	u, err := users.Create(ctx, "alice", hash)
	if err != nil {
		t.Fatal(err)
	}
	_ = loginAndGetCookie(t, h, "alice", "old-password-1234")

	// Drive ChangePassword as alice and capture the response's new cookie.
	cpReq := httptest.NewRequest(http.MethodPost, "/auth/password",
		jsonBody(t, changePasswordRequest{
			CurrentPassword: "old-password-1234",
			NewPassword:     "new-password-9876",
		}))
	cpReq = cpReq.WithContext(auth.WithUserID(cpReq.Context(), u.ID))
	cpRec := httptest.NewRecorder()
	h.ChangePassword(cpRec, cpReq)
	if cpRec.Code != http.StatusOK {
		t.Fatalf("change password: status=%d body=%s", cpRec.Code, cpRec.Body.String())
	}
	newCookie := extractSessionCookie(cpRec.Result())
	if newCookie == nil || newCookie.Value == "" {
		t.Fatal("password-change response must set a fresh session cookie so the caller stays logged in")
	}

	// The fresh cookie must authenticate against the real middleware chain.
	provider := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value)}
	var gotUID int64
	chain := auth.Middleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUID = auth.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author", nil)
	req.AddCookie(newCookie)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if gotUID != u.ID {
		t.Errorf("freshly-issued cookie failed to authenticate: gotUID=%d, want %d", gotUID, u.ID)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status=%d; want 200 for valid fresh cookie", rec.Code)
	}
}

// must panics if the SettingsRepo lookup returned an error or nil. Keeps the
// epoch tests above readable without losing the safety net.
func must(s *models.Setting, err error) *models.Setting {
	if err != nil {
		panic(err)
	}
	if s == nil {
		panic("nil setting")
	}
	return s
}

// withChiURLParam attaches a chi URL parameter to the request context. The
// user-management handlers parse the user id via chi.URLParam, which expects
// the chi RouteContext to be present even outside a router.
func withChiURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
