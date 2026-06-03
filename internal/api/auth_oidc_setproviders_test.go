package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
)

// These tests cover OIDCHandler.SetProviders (PUT /auth/oidc/providers), which
// was at 0% handler coverage. SetProviders does a full-array replace of the
// configured provider set and persists it via SettingsRepo, preserving secrets
// that the client omits.
//
// AUTHORIZATION NOTE: like the user-management mutations, the admin gate for
// SetProviders is the router's RequireAdmin middleware
// (cmd/bindery/main.go:704), not anything inside the handler. The handler reads
// no user/role from the request context at all. TestSetProviders_NonAdmin
// Forbidden mounts the handler behind RequireAdmin to assert the authorization
// contract holds at the wiring level.

func newOIDCSetProvidersFixture(t *testing.T) (*OIDCHandler, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	mgr := oidc.NewManager()
	h := NewOIDCHandler(mgr, nil, settings, nil, func(_ *http.Request) string {
		return "https://bindery.example.com"
	})
	return h, settings
}

// storedProviders parses the raw persisted SettingOIDCProviders value back into
// configs so tests can assert what actually landed in the DB.
func storedProviders(t *testing.T, settings *db.SettingsRepo) []oidc.ProviderConfig {
	t.Helper()
	s, err := settings.Get(context.Background(), SettingOIDCProviders)
	if err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if s == nil || s.Value == "" {
		return nil
	}
	ps, err := oidc.ParseProviders(s.Value)
	if err != nil {
		t.Fatalf("parse stored providers: %v", err)
	}
	return ps
}

func TestSetProviders_ValidReplacesSet(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)

	body := `[
		{"id":"keycloak","name":"Keycloak","issuer":"https://kc.example.com","client_id":"abc","client_secret":"s3cr3t","scopes":["openid"]},
		{"id":"authentik","name":"Authentik","issuer":"https://ak.example.com","client_id":"def","client_secret":"t0ps3cr3t","scopes":["openid"]}
	]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))

	if rec.Code != http.StatusOK {
		t.Fatalf("SetProviders status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse body: %v (body=%s)", err, rec.Body.String())
	}
	if !got.OK || got.Count != 2 {
		t.Fatalf("ok=%v count=%d, want ok=true count=2", got.OK, got.Count)
	}
	ps := storedProviders(t, settings)
	if len(ps) != 2 {
		t.Fatalf("stored %d providers, want 2", len(ps))
	}
	if ps[0].ID != "keycloak" || ps[1].ID != "authentik" {
		t.Errorf("stored ids = %q,%q; want keycloak,authentik", ps[0].ID, ps[1].ID)
	}
}

// TestSetProviders_FullArrayReplace verifies the second PUT fully replaces the
// first set rather than merging by id — submitting a single provider drops the
// others.
func TestSetProviders_FullArrayReplace(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)
	ctx := context.Background()

	first := `[
		{"id":"a","name":"A","issuer":"https://a.example.com","client_id":"a","client_secret":"sa","scopes":["openid"]},
		{"id":"b","name":"B","issuer":"https://b.example.com","client_id":"b","client_secret":"sb","scopes":["openid"]}
	]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", first, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("first SetProviders status=%d; body=%s", rec.Code, rec.Body.String())
	}

	// Now replace with just "a". "b" must be gone.
	second := `[{"id":"a","name":"A2","issuer":"https://a.example.com","client_id":"a","client_secret":"sa2","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", second, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("second SetProviders status=%d; body=%s", rec.Code, rec.Body.String())
	}

	ps := storedProviders(t, settings)
	if len(ps) != 1 || ps[0].ID != "a" {
		t.Fatalf("after replace got %d providers (%+v), want exactly [a]", len(ps), ps)
	}
	if ps[0].Name != "A2" {
		t.Errorf("name=%q, want A2 (replacement should overwrite fields)", ps[0].Name)
	}
}

// TestSetProviders_PreservesOmittedSecret verifies the secret-preservation
// merge: when an existing provider is re-submitted with an empty client_secret,
// the previously stored secret is retained (the UI never re-sends secrets).
func TestSetProviders_PreservesOmittedSecret(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)
	ctx := context.Background()

	first := `[{"id":"kc","name":"KC","issuer":"https://kc.example.com","client_id":"cid","client_secret":"original-secret","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", first, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("first SetProviders status=%d; body=%s", rec.Code, rec.Body.String())
	}

	// Re-submit the same provider with the secret omitted (empty).
	second := `[{"id":"kc","name":"KC renamed","issuer":"https://kc.example.com","client_id":"cid","client_secret":"","scopes":["openid"]}]`
	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", second, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("second SetProviders status=%d; body=%s", rec.Code, rec.Body.String())
	}

	ps := storedProviders(t, settings)
	if len(ps) != 1 {
		t.Fatalf("stored %d providers, want 1", len(ps))
	}
	if ps[0].ClientSecret != "original-secret" {
		t.Errorf("client_secret=%q, want it preserved as original-secret", ps[0].ClientSecret)
	}
	if ps[0].Name != "KC renamed" {
		t.Errorf("name=%q, want KC renamed (non-secret fields still update)", ps[0].Name)
	}
}

// TestSetProviders_NewProviderRequiresSecret verifies that a brand-new provider
// submitted without a client_secret is rejected with 400 — there's no prior
// secret to preserve.
func TestSetProviders_NewProviderRequiresSecret(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)

	body := `[{"id":"brandnew","name":"New","issuer":"https://new.example.com","client_id":"cid","client_secret":"","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", body, context.Background()))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("new provider without secret status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// Nothing should have been persisted.
	if ps := storedProviders(t, settings); len(ps) != 0 {
		t.Errorf("expected nothing persisted on validation failure, got %+v", ps)
	}
}

func TestSetProviders_InvalidJSON(t *testing.T) {
	h, _ := newOIDCSetProvidersFixture(t)
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", `{not an array`, context.Background()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSetProviders_EmptyArrayClears verifies an empty array is a valid input
// that clears the provider set (count=0).
func TestSetProviders_EmptyArrayClears(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)
	ctx := context.Background()

	first := `[{"id":"a","name":"A","issuer":"https://a.example.com","client_id":"a","client_secret":"sa","scopes":["openid"]}]`
	rec := httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", first, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed SetProviders status=%d; body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.SetProviders(rec, jsonReq(http.MethodPut, "/api/v1/auth/oidc/providers", `[]`, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty-array SetProviders status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Count != 0 {
		t.Errorf("count=%d, want 0", got.Count)
	}
	if ps := storedProviders(t, settings); len(ps) != 0 {
		t.Errorf("provider set not cleared, got %+v", ps)
	}
}

// TestSetProviders_NonAdminForbidden asserts the AUTHORIZATION requirement for
// PUT /auth/oidc/providers. The handler itself has no role check; the gate is
// the RequireAdmin middleware the router wraps it with. Mounting the handler the
// same way main.go does and driving it with a non-admin context proves the
// request is rejected with 403 before SetProviders runs (so nothing is
// persisted).
func TestSetProviders_NonAdminForbidden(t *testing.T) {
	h, settings := newOIDCSetProvidersFixture(t)

	r := chi.NewRouter()
	r.With(auth.RequireAdmin).Put("/auth/oidc/providers", h.SetProviders)

	ctx := withAuthCtx(context.Background(), 42, "user")
	body := `[{"id":"evil","name":"Evil","issuer":"https://evil.example.com","client_id":"x","client_secret":"x","scopes":["openid"]}]`
	req := jsonReq(http.MethodPut, "/auth/oidc/providers", body, ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin SetProviders status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if ps := storedProviders(t, settings); len(ps) != 0 {
		t.Errorf("non-admin caller mutated the provider set despite 403: %+v", ps)
	}
}
