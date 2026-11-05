package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// setupJSONReq builds a POST /auth/setup request the way the UI sends it.
func setupJSONReq(body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/auth/setup", body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

const setupBody = `{"username":"admin","password":"long-enough-password"}`

// A cross-site form can post text/plain without a preflight. Setup must not
// accept it, and must not create the account.
func TestSetup_RejectsNonJSONContentType(t *testing.T) {
	for _, ct := range []string{"text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		h, users, _, ctx := newAuthFixture(t)
		req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(setupBody))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.Setup(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: got %d, want 415", ct, rec.Code)
		}
		if n, _ := users.Count(ctx); n != 0 {
			t.Errorf("Content-Type %q: %d users created, want 0", ct, n)
		}
	}
}

func TestSetup_AcceptsJSONWithCharset(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(setupBody))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	h.Setup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func setupStatus(t *testing.T, h *AuthHandler) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Setup(rec, setupJSONReq(strings.NewReader(setupBody)))
	return rec.Code
}

// With local accounts disabled and OIDC configured, setup must not hand out
// an admin: the first SSO login owns that role.
func TestSetup_RefusedWhenLocalAuthDisabledAndOIDCConfigured(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	h.WithLocalAuthEnabled(false)
	providers := `[{"id":"idp","name":"IdP","issuer":"https://idp.example.com","client_id":"c","client_secret":"s","scopes":["openid"]}]`
	if err := settings.Set(ctx, SettingOIDCProviders, providers); err != nil {
		t.Fatal(err)
	}
	if got := setupStatus(t, h); got != http.StatusForbidden {
		t.Fatalf("got %d, want 403", got)
	}
	if n, _ := users.Count(ctx); n != 0 {
		t.Fatalf("%d users created, want 0", n)
	}
}

func TestSetup_RefusedWhenLocalAuthDisabledInProxyMode(t *testing.T) {
	h, _, settings, ctx := newAuthFixture(t)
	h.WithLocalAuthEnabled(false)
	if err := settings.Set(ctx, SettingAuthMode, string(auth.ModeProxy)); err != nil {
		t.Fatal(err)
	}
	if got := setupStatus(t, h); got != http.StatusForbidden {
		t.Fatalf("got %d, want 403", got)
	}
}

// A fresh install with local accounts disabled and no other way in must still
// be able to run the wizard, or it is unreachable.
func TestSetup_AllowedWhenLocalAuthDisabledButNoAlternative(t *testing.T) {
	h, _, _, _ := newAuthFixture(t)
	h.WithLocalAuthEnabled(false)
	if got := setupStatus(t, h); got != http.StatusOK {
		t.Fatalf("got %d, want 200", got)
	}
}

func TestSetup_AllowedWithLocalAuthEnabledAndOIDCConfigured(t *testing.T) {
	h, _, settings, ctx := newAuthFixture(t)
	providers := `[{"id":"idp","name":"IdP","issuer":"https://idp.example.com","client_id":"c","client_secret":"s","scopes":["openid"]}]`
	if err := settings.Set(ctx, SettingOIDCProviders, providers); err != nil {
		t.Fatal(err)
	}
	if got := setupStatus(t, h); got != http.StatusOK {
		t.Fatalf("got %d, want 200", got)
	}
}

// Status must report setupRequired=false exactly when Setup would be refused,
// or the UI redirects to a setup form that can never succeed.
func TestStatus_SetupRequiredMatchesSetupOffered(t *testing.T) {
	oidcJSON := `[{"id":"idp","name":"IdP","issuer":"https://idp.example.com","client_id":"c","client_secret":"s","scopes":["openid"]}]`
	cases := []struct {
		name      string
		localAuth bool
		oidc      bool
		proxy     bool
		want      bool
	}{
		{"fresh, local enabled", true, false, false, true},
		{"fresh, local enabled, oidc", true, true, false, true},
		{"fresh, local off, oidc", false, true, false, false},
		{"fresh, local off, proxy", false, false, true, false},
		{"fresh, local off, no alternative", false, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, settings, ctx := newAuthFixture(t)
			h.WithLocalAuthEnabled(c.localAuth)
			if c.oidc {
				if err := settings.Set(ctx, SettingOIDCProviders, oidcJSON); err != nil {
					t.Fatal(err)
				}
			}
			if c.proxy {
				if err := settings.Set(ctx, SettingAuthMode, string(auth.ModeProxy)); err != nil {
					t.Fatal(err)
				}
			}
			rec := httptest.NewRecorder()
			h.Status(rec, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
			var resp statusResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if resp.SetupRequired != c.want {
				t.Errorf("setupRequired=%v, want %v", resp.SetupRequired, c.want)
			}
			if offered := setupStatus(t, h) != http.StatusForbidden; offered != resp.SetupRequired {
				t.Errorf("setup offered=%v but setupRequired=%v", offered, resp.SetupRequired)
			}
		})
	}
}
