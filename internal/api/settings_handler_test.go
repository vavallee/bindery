package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func settingsFixture(t *testing.T) (*SettingsHandler, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	return NewSettingsHandler(repo), repo, context.Background()
}

func withKey(req *http.Request, key string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestSettings_ListFiltersSecrets is the critical test: auth.* values live
// in the same settings table but must never surface via the generic
// /settings endpoint. A regression here leaks the HMAC session secret and
// the API key to any authenticated browser tab.
func TestSettings_ListFiltersSecrets(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	for k, v := range map[string]string{
		"auth.api_key":        "supersecret-apikey",
		"auth.session_secret": "supersecret-hmac",
		"auth.mode":           "enabled",
		"ui.theme":            "dark",
		"importer.library":    "/books",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []models.Setting
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.Key == "auth.api_key" || s.Key == "auth.session_secret" || s.Key == "auth.mode" {
			t.Errorf("secret leaked through List: %s", s.Key)
		}
	}
	// Non-secrets must still come through.
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("ui.theme")) || !bytes.Contains([]byte(body), []byte("importer.library")) {
		t.Errorf("non-secret keys missing from List output: %s", body)
	}
}

// TestSettings_GetSecretReturns404 — callers must see "not found" rather
// than a 403, to match the List contract (secrets are invisible, not guarded).
func TestSettings_GetSecretReturns404(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, "auth.api_key", "supersecret"); err != nil {
		t.Fatal(err)
	}
	req := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/settings/auth.api_key", nil), "auth.api_key")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for secret Get, got %d", rec.Code)
	}
}

// TestSettings_SetSecretReturns403 — explicit 403 because we want callers
// to know the endpoint exists but is disallowed; use /auth/* instead.
func TestSettings_SetSecretReturns403(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, "auth.api_key", "original"); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"value":"overwritten"}`)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/auth.api_key", body), "auth.api_key")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	// Underlying value must not have changed.
	got, _ := repo.Get(ctx, "auth.api_key")
	if got == nil || got.Value != "original" {
		t.Errorf("secret should be unchanged, got %v", got)
	}
}

func TestSettings_DeleteSecretReturns403(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, "auth.session_secret", "keep-me"); err != nil {
		t.Fatal(err)
	}
	req := withKey(httptest.NewRequest(http.MethodDelete, "/api/v1/settings/auth.session_secret", nil), "auth.session_secret")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	got, _ := repo.Get(ctx, "auth.session_secret")
	if got == nil {
		t.Error("secret should survive forbidden delete")
	}
}

// TestSettings_SetRoundTrip confirms the happy-path CRUD works for a
// non-secret key.
func TestSettings_SetRoundTrip(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	body := bytes.NewBufferString(`{"value":"dark"}`)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/ui.theme", body), "ui.theme")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.Get(ctx, "ui.theme")
	if got == nil || got.Value != "dark" {
		t.Errorf("value not persisted, got %v", got)
	}
}

func TestSettings_GetUnknown404(t *testing.T) {
	h, _, _ := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/settings/nope", nil), "nope")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestSettings_DeleteNonSecret — the underlying repo is idempotent, so
// deleting a missing key still 204s.
func TestSettings_DeleteNonSecret(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, "ui.theme", "dark"); err != nil {
		t.Fatal(err)
	}
	req := withKey(httptest.NewRequest(http.MethodDelete, "/api/v1/settings/ui.theme", nil), "ui.theme")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	got, _ := repo.Get(ctx, "ui.theme")
	if got != nil {
		t.Errorf("expected key removed, got %v", got)
	}
}

// TestSettings_SetBadBody catches the JSON-parse error path.
func TestSettings_SetBadBody(t *testing.T) {
	h, _, _ := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/ui.theme", bytes.NewBufferString("not-json")), "ui.theme")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
