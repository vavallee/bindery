package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// TestAuthCSRFCoverage pins the CSRF bootstrap: no session cookie means an
// empty token and no cookie, a session cookie yields the token derived from
// the session secret, mirrored into a JS readable cookie whose Secure flag
// follows BINDERY_COOKIE_SECURE (or the request scheme on auto).
func TestAuthCSRFCoverage(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	want := auth.MakeCSRFToken([]byte("test-secret-32-bytes-long-enough"), "session-value") // gitleaks:allow

	csrf := func(req *http.Request) (*httptest.ResponseRecorder, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.CSRF(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return rec, body.CSRFToken
	}
	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session-value"})
		return req
	}
	csrfCookie := func(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.CSRFCookieName {
				return c
			}
		}
		t.Fatal("no csrf cookie set")
		return nil
	}

	t.Run("no session", func(t *testing.T) {
		rec, token := csrf(httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil))
		if token != "" {
			t.Fatalf("token = %q without a session, want empty", token)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatalf("cookies set without a session: %v", rec.Result().Cookies())
		}
	})

	cases := []struct {
		name       string
		env        string
		mutate     func(*http.Request)
		wantSecure bool
	}{
		{name: "auto over plain http", env: "", wantSecure: false},
		{name: "auto behind https proxy", env: "", mutate: func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, wantSecure: true},
		{name: "auto over tls", env: "", mutate: func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, wantSecure: true},
		{name: "always", env: "always", wantSecure: true},
		{name: "never even over tls", env: "never", mutate: func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, wantSecure: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BINDERY_COOKIE_SECURE", c.env)
			req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil))
			if c.mutate != nil {
				c.mutate(req)
			}
			rec, token := csrf(req)
			if token != want {
				t.Fatalf("token = %q, want %q", token, want)
			}
			ck := csrfCookie(t, rec)
			if ck.Value != want || ck.HttpOnly || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode {
				t.Fatalf("csrf cookie = %+v", ck)
			}
			if ck.Secure != c.wantSecure {
				t.Fatalf("Secure = %v, want %v", ck.Secure, c.wantSecure)
			}
		})
	}
}

// TestAuthChangePasswordCoverageRefusals covers the refusals in front of the
// hash update. Each one must leave the stored hash untouched.
func TestAuthChangePasswordCoverageRefusals(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, err := auth.HashPassword("original-password")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := users.Create(ctx, "alice", hash)
	if err != nil {
		t.Fatal(err)
	}

	change := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ChangePassword(rec, req)
		return rec
	}
	post := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(body))
	}
	asAlice := func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUserID(r.Context(), alice.ID))
	}
	good := `{"currentPassword":"original-password","newPassword":"brand-new-password"}`

	if rec := change(asAlice(post(`{`))); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d, want 400", rec.Code)
	}
	if rec := change(asAlice(post(`{"currentPassword":"original-password","newPassword":"short"}`))); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d, want 400", rec.Code)
	}
	// A session for a user id that no longer exists.
	rec := change(post(good).WithContext(auth.WithUserID(post(good).Context(), alice.ID+100)))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "user not found") {
		t.Fatalf("ghost user: %d %s, want 401 user not found", rec.Code, rec.Body.String())
	}

	// With no identity the handler only acts when exactly one user exists.
	if _, err := users.Create(ctx, "bob", hash); err != nil {
		t.Fatal(err)
	}
	rec = change(post(good))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "not logged in") {
		t.Fatalf("anonymous with two users: %d %s, want 401 not logged in", rec.Code, rec.Body.String())
	}

	after, err := users.GetByID(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != hash {
		t.Fatal("a refused change rewrote the password hash")
	}
}
