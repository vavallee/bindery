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
		"abs.api_key":         "supersecret-abs",
		"hardcover.api_token": "supersecret-hardcover",
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
		if s.Key == "auth.api_key" || s.Key == "auth.session_secret" || s.Key == "auth.mode" || s.Key == "abs.api_key" || s.Key == "hardcover.api_token" {
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

func TestSettings_GetABSSecretReturns404(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, SettingABSAPIKey, "supersecret"); err != nil {
		t.Fatal(err)
	}
	req := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/settings/"+SettingABSAPIKey, nil), SettingABSAPIKey)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for ABS secret Get, got %d", rec.Code)
	}
}

func TestSettings_SetHardcoverTokenIsWriteOnly(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingHardcoverAPIToken, bytes.NewBufferString(`{"value":"  Authorization: Bearer Bearer hc-secret  "}`)), SettingHardcoverAPIToken)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("hc-secret")) {
		t.Fatalf("hardcover token leaked through Set response: %s", rec.Body.String())
	}
	got, _ := repo.Get(ctx, SettingHardcoverAPIToken)
	if got == nil || got.Value != "hc-secret" {
		t.Fatalf("expected normalized token persisted, got %+v", got)
	}

	getReq := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/settings/"+SettingHardcoverAPIToken, nil), SettingHardcoverAPIToken)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for Hardcover token Get, got %d", getRec.Code)
	}
}

func TestGetHardcoverAPITokenNormalizesLegacyStoredValue(t *testing.T) {
	_, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, SettingHardcoverAPIToken, "Authorization: Bearer hc-secret"); err != nil {
		t.Fatal(err)
	}
	if got := GetHardcoverAPIToken(ctx, repo); got != "hc-secret" {
		t.Fatalf("GetHardcoverAPIToken = %q, want hc-secret", got)
	}
}

func TestSettings_TestHardcoverReportsMissingToken(t *testing.T) {
	h, _, _ := settingsFixture(t)
	rec := httptest.NewRecorder()
	h.TestHardcover(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hardcover/test", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got HardcoverTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.TokenConfigured || got.Error == "" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHardcoverFeatureState(t *testing.T) {
	_, repo, ctx := settingsFixture(t)

	state := HardcoverFeatureStateFor(ctx, repo, false)
	if state.EnhancedHardcoverAPI || state.EnhancedHardcoverDisabledReason != HardcoverDisabledReasonEnvDisabled {
		t.Fatalf("env disabled state = %+v", state)
	}

	state = HardcoverFeatureStateFor(ctx, repo, true)
	if state.EnhancedHardcoverAPI || state.EnhancedHardcoverDisabledReason != HardcoverDisabledReasonMissingToken {
		t.Fatalf("missing token state = %+v", state)
	}

	if err := repo.Set(ctx, SettingHardcoverAPIToken, "hc-secret"); err != nil {
		t.Fatal(err)
	}
	state = HardcoverFeatureStateFor(ctx, repo, true)
	if state.EnhancedHardcoverAPI || !state.HardcoverTokenConfigured || state.EnhancedHardcoverDisabledReason != HardcoverDisabledReasonAdminDisabled {
		t.Fatalf("admin disabled state = %+v", state)
	}

	if err := repo.Set(ctx, SettingHardcoverEnhancedSeriesEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	state = HardcoverFeatureStateFor(ctx, repo, true)
	if !state.EnhancedHardcoverAPI || !state.HardcoverTokenConfigured || state.EnhancedHardcoverDisabledReason != "" {
		t.Fatalf("enabled state = %+v", state)
	}
}

func TestSettings_DefaultLibraryRootFolderID(t *testing.T) {
	cases := []struct {
		value  string
		wantOK bool
	}{
		{"", true},
		{"1", true},
		{"42", true},
		{"0", false},
		{"-1", false},
		{"abc", false},
		{"1.5", false},
	}
	for _, tc := range cases {
		h, _, _ := settingsFixture(t)
		body := bytes.NewBufferString(`{"value":"` + tc.value + `"}`)
		req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingDefaultLibraryRootFolderID, body), SettingDefaultLibraryRootFolderID)
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		if tc.wantOK && rec.Code != http.StatusOK {
			t.Errorf("value %q: expected 200, got %d: %s", tc.value, rec.Code, rec.Body.String())
		}
		if !tc.wantOK && rec.Code != http.StatusBadRequest {
			t.Errorf("value %q: expected 400, got %d", tc.value, rec.Code)
		}
	}
}

// TestSettings_MetadataPrimaryProvider validates that only "openlibrary", "dnb",
// and "" (empty = default) are accepted, and that unknown values are rejected.
func TestSettings_MetadataPrimaryProvider(t *testing.T) {
	cases := []struct {
		value  string
		wantOK bool
	}{
		{"", true},
		{"openlibrary", true},
		{"dnb", true},
		{"hardcover", false},
		{"googlebooks", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		h, _, _ := settingsFixture(t)
		body := bytes.NewBufferString(`{"value":"` + tc.value + `"}`)
		req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingMetadataPrimaryProvider, body), SettingMetadataPrimaryProvider)
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		if tc.wantOK && rec.Code != http.StatusOK {
			t.Errorf("value %q: expected 200, got %d: %s", tc.value, rec.Code, rec.Body.String())
		}
		if !tc.wantOK && rec.Code != http.StatusBadRequest {
			t.Errorf("value %q: expected 400, got %d", tc.value, rec.Code)
		}
	}
}
