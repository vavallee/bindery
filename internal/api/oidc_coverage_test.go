package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
)

func covOIDCSettings(t *testing.T) (*db.SettingsRepo, func()) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return db.NewSettingsRepo(database), func() { database.Close() }
}

// TestOIDCGetProvidersCoverage: no configuration lists nothing, a stored
// configuration lists every provider with its runtime status and never the
// client secret, and an unparsable setting is a 500 rather than a silent
// empty list.
func TestOIDCGetProvidersCoverage(t *testing.T) {
	settings, closeDB := covOIDCSettings(t)
	h := NewOIDCHandler(oidc.NewManager(), nil, settings, nil, nil)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.GetProviders(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/providers", nil))
		return rec
	}

	rec := get()
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("unconfigured: %d %q, want 200 []", rec.Code, rec.Body.String())
	}

	stored := `[{"id":"authelia","name":"Authelia","issuer":"https://auth.example.invalid","client_id":"bindery","client_secret":"s3cr3t-value","scopes":["openid","email"]}]`
	if err := settings.Set(t.Context(), SettingOIDCProviders, stored); err != nil {
		t.Fatal(err)
	}
	rec = get()
	if rec.Code != http.StatusOK {
		t.Fatalf("configured: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s3cr3t-value") || strings.Contains(rec.Body.String(), "client_secret") {
		t.Fatalf("provider listing leaked the client secret: %s", rec.Body.String())
	}
	var got []providerWithStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "authelia" || got[0].Name != "Authelia" || got[0].ClientID != "bindery" || len(got[0].Scopes) != 2 {
		t.Fatalf("providers = %+v", got)
	}

	if err := settings.Set(t.Context(), SettingOIDCProviders, `{not json`); err != nil {
		t.Fatal(err)
	}
	if rec := get(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt setting: %d, want 500", rec.Code)
	}

	closeDB()
	// A settings read failure is treated as unconfigured.
	if rec := get(); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("settings error: %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

// TestOIDCLoginCoverageRefusals: a malformed provider id and a provider the
// manager does not know are both 400, and neither starts a flow (no cookie,
// no redirect).
func TestOIDCLoginCoverageRefusals(t *testing.T) {
	settings, _ := covOIDCSettings(t)
	h := NewOIDCHandler(oidc.NewManager(), nil, settings, nil, func(*http.Request) string { return "https://bindery.example.invalid" })
	login := func(provider string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", provider)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h.Login(rec, req)
		return rec
	}
	for _, c := range []struct {
		provider, wantErr string
	}{
		{"Bad.ID", "invalid provider id"},
		{strings.Repeat("a", 33), "invalid provider id"},
		{"unknown", ""},
	} {
		rec := login(c.provider)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d %s, want 400", c.provider, rec.Code, rec.Body.String())
		}
		if c.wantErr != "" && !strings.Contains(rec.Body.String(), c.wantErr) {
			t.Fatalf("%q: body %s, want %q", c.provider, rec.Body.String(), c.wantErr)
		}
		if len(rec.Result().Cookies()) != 0 || rec.Header().Get("Location") != "" {
			t.Fatalf("%q: refused login started a flow: cookies=%v location=%q", c.provider, rec.Result().Cookies(), rec.Header().Get("Location"))
		}
	}
}
