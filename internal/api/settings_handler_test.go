package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestSettings_AuthorMonitorDefaultsValidation(t *testing.T) {
	h, repo, ctx := settingsFixture(t)

	validReq := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingAuthorDefaultMonitorMode, bytes.NewBufferString(`{"value":"future"}`)), SettingAuthorDefaultMonitorMode)
	validRec := httptest.NewRecorder()
	h.Set(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("expected valid mode 200, got %d: %s", validRec.Code, validRec.Body.String())
	}
	got, _ := repo.Get(ctx, SettingAuthorDefaultMonitorMode)
	if got == nil || got.Value != models.AuthorMonitorModeFuture {
		t.Fatalf("expected future setting persisted, got %+v", got)
	}

	badModeReq := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingAuthorDefaultMonitorMode, bytes.NewBufferString(`{"value":"yesterday"}`)), SettingAuthorDefaultMonitorMode)
	badModeRec := httptest.NewRecorder()
	h.Set(badModeRec, badModeReq)
	if badModeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid mode 400, got %d", badModeRec.Code)
	}

	validCountReq := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingAuthorDefaultMonitorLatestCount, bytes.NewBufferString(`{"value":"5"}`)), SettingAuthorDefaultMonitorLatestCount)
	validCountRec := httptest.NewRecorder()
	h.Set(validCountRec, validCountReq)
	if validCountRec.Code != http.StatusOK {
		t.Fatalf("expected valid count 200, got %d: %s", validCountRec.Code, validCountRec.Body.String())
	}
	got, _ = repo.Get(ctx, SettingAuthorDefaultMonitorLatestCount)
	if got == nil || got.Value != "5" {
		t.Fatalf("expected latest count persisted, got %+v", got)
	}

	badCountReq := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingAuthorDefaultMonitorLatestCount, bytes.NewBufferString(`{"value":"0"}`)), SettingAuthorDefaultMonitorLatestCount)
	badCountRec := httptest.NewRecorder()
	h.Set(badCountRec, badCountReq)
	if badCountRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid count 400, got %d", badCountRec.Code)
	}

	// "series" is a per-author mode only — rejecting it as a global default
	// prevents an install from booting every new author into a mode that has
	// no global selection (#810).
	seriesReq := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingAuthorDefaultMonitorMode, bytes.NewBufferString(`{"value":"series"}`)), SettingAuthorDefaultMonitorMode)
	seriesRec := httptest.NewRecorder()
	h.Set(seriesRec, seriesReq)
	if seriesRec.Code != http.StatusBadRequest {
		t.Fatalf("expected series-as-default 400, got %d: %s", seriesRec.Code, seriesRec.Body.String())
	}
}

// TestSettings_SecretLeakRegression pins down the keys that the deep-audit
// pass discovered were missing from isSecretSetting. Any of them surfacing
// through the generic settings endpoint leaks credentials to every
// authenticated user.
//
// `auth.oidc.providers` is the worst of these: the value is a JSON blob with
// one entry per configured IdP, each containing the `client_secret`. Leaking
// it lets any logged-in user mint tokens against the IdP from outside Bindery.
func TestSettings_SecretLeakRegression(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	leaky := map[string]string{
		SettingOIDCProviders:             `[{"id":"okta","clientSecret":"S3CRET"}]`,
		SettingAuthSessionSecretPrevious: "previous-hmac-key",
		SettingGrimmoryAPIKey:            "grimmory-api-key",
		SettingCalibrePluginAPIKey:       "calibre-plugin-key",
		// Generic pattern coverage: keys that aren't enumerated but follow
		// the suffix/prefix conventions must still be filtered.
		"some_new_provider.api_key":   "secret-by-pattern",
		"some_new_provider.api_token": "secret-by-pattern-token",
		"some_section.client_secret":  "secret-by-suffix",
		"foo.bar_secret":              "secret-by-suffix",
		"db.password":                 "secret-by-password-suffix",
		"auth.oidc.future_field":      "secret-by-oidc-prefix",
	}
	for k, v := range leaky {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	// And a key that LOOKS adjacent but is safe — must still be readable.
	if err := repo.Set(ctx, "grimmory.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List status = %d", rec.Code)
	}
	body := rec.Body.String()
	for k, v := range leaky {
		if bytes.Contains([]byte(body), []byte(v)) {
			t.Errorf("List leaked secret value for %q: %q present in response", k, v)
		}
		if bytes.Contains([]byte(body), []byte(`"key":"`+k+`"`)) {
			t.Errorf("List leaked secret key %q via key field", k)
		}
	}
	if !bytes.Contains([]byte(body), []byte("grimmory.enabled")) {
		t.Errorf("non-secret grimmory.enabled missing from List output: %s", body)
	}

	// Direct GET on each leaky key must 404.
	for k := range leaky {
		req := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/setting/"+k, nil), k)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("Get %q expected 404 (secret), got %d body=%s", k, rec.Code, rec.Body.String())
		}
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

// TestSettings_SetCalibrePluginKeyIsWriteOnly is the regression for #1036:
// the Calibre Bridge plugin API key is a secret, but it's set through the
// generic settings endpoint (no dedicated handler), so it must be writable
// there while staying hidden from reads. Before the fix, Set returned 403
// "use /auth/* endpoints for auth settings" and the key was unsettable.
func TestSettings_SetCalibrePluginKeyIsWriteOnly(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+SettingCalibrePluginAPIKey, bytes.NewBufferString(`{"value":"plugin-secret"}`)), SettingCalibrePluginAPIKey)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("plugin-secret")) {
		t.Fatalf("plugin key leaked through Set response: %s", rec.Body.String())
	}
	got, _ := repo.Get(ctx, SettingCalibrePluginAPIKey)
	if got == nil || got.Value != "plugin-secret" {
		t.Fatalf("expected plugin key persisted, got %+v", got)
	}
	// Still hidden from reads.
	getReq := withKey(httptest.NewRequest(http.MethodGet, "/api/v1/settings/"+SettingCalibrePluginAPIKey, nil), SettingCalibrePluginAPIKey)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for plugin key Get, got %d", getRec.Code)
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

// TestSettings_ImportDropValidation covers the three drop-folder handoff keys
// (#941): the folder must be an existing directory (empty disables), and the
// layout / link-mode enums reject typos.
func TestSettings_ImportDropValidation(t *testing.T) {
	h, repo, ctx := settingsFixture(t)

	put := func(key, value string) int {
		body := bytes.NewBufferString(`{"value":` + mustJSON(value) + `}`)
		req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/settings/"+key, body), key)
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		return rec.Code
	}

	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// drop_folder: empty ok (disabled), existing dir ok, missing/file rejected.
	if code := put(SettingImportDropFolder, ""); code != http.StatusOK {
		t.Errorf("empty drop_folder: got %d, want 200", code)
	}
	if code := put(SettingImportDropFolder, dir); code != http.StatusOK {
		t.Errorf("valid drop_folder dir: got %d, want 200", code)
	}
	got, _ := repo.Get(ctx, SettingImportDropFolder)
	if got == nil || got.Value != dir {
		t.Errorf("drop_folder not persisted: %+v", got)
	}
	if code := put(SettingImportDropFolder, filepath.Join(dir, "does-not-exist")); code != http.StatusBadRequest {
		t.Errorf("missing drop_folder: got %d, want 400", code)
	}
	if code := put(SettingImportDropFolder, notADir); code != http.StatusBadRequest {
		t.Errorf("drop_folder pointing at a file: got %d, want 400", code)
	}

	// layout enum.
	if code := put(SettingImportDropLayout, "templated"); code != http.StatusOK {
		t.Errorf("valid layout: got %d, want 200", code)
	}
	if code := put(SettingImportDropLayout, "sideways"); code != http.StatusBadRequest {
		t.Errorf("bad layout: got %d, want 400", code)
	}

	// link-mode enum (move is intentionally rejected — source must keep seeding).
	if code := put(SettingImportDropLinkMode, "hardlink"); code != http.StatusOK {
		t.Errorf("valid link mode: got %d, want 200", code)
	}
	if code := put(SettingImportDropLinkMode, "move"); code != http.StatusBadRequest {
		t.Errorf("link mode 'move': got %d, want 400", code)
	}
}

// TestValidateSettingValue_SearchInterval exercises the search.interval bounds
// the validator enforces: empty is allowed (scheduler falls back to default),
// any duration in [1h, 168h] is accepted, and values below 1h, above 168h, or
// that don't parse as a Go duration are rejected. These bounds are mirrored in
// the scheduler (resolveSearchInterval) so an out-of-band DB value can't slip
// through; keep the two in sync.
func TestValidateSettingValue_SearchInterval(t *testing.T) {
	// Empty = unset, accepted.
	if err := validateSettingValue(SettingSearchInterval, ""); err != nil {
		t.Errorf("empty should be accepted: %v", err)
	}
	// Valid in-range values, including the inclusive bounds.
	for _, v := range []string{"1h", "12h", "24h", "168h"} {
		if err := validateSettingValue(SettingSearchInterval, v); err != nil {
			t.Errorf("%q should be accepted: %v", v, err)
		}
	}
	// Below the 1h minimum: rejected.
	if err := validateSettingValue(SettingSearchInterval, "30m"); err == nil {
		t.Error("30m should be rejected (below 1h minimum)")
	}
	// Above the 168h maximum: rejected.
	if err := validateSettingValue(SettingSearchInterval, "200h"); err == nil {
		t.Error("200h should be rejected (above 168h maximum)")
	}
	// Unparseable duration string: rejected.
	if err := validateSettingValue(SettingSearchInterval, "soon"); err == nil {
		t.Error("'soon' should be rejected (not a valid duration)")
	}
}

func TestValidateSettingValue_ImportMode(t *testing.T) {
	// Empty = auto (same as the explicit "auto"), accepted.
	if err := validateSettingValue(SettingImportMode, ""); err != nil {
		t.Errorf("empty should be accepted (auto): %v", err)
	}
	// All recognised modes, including the explicit "auto" the UI now writes for
	// the default (#1444).
	for _, v := range []string{"auto", "move", "copy", "hardlink", "external"} {
		if err := validateSettingValue(SettingImportMode, v); err != nil {
			t.Errorf("%q should be accepted: %v", v, err)
		}
	}
	// A typo must fail loudly rather than silently fall through to auto at
	// import time, where the operator would think Move/External was in effect.
	if err := validateSettingValue(SettingImportMode, "moove"); err == nil {
		t.Error("'moove' should be rejected (not a valid import mode)")
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
