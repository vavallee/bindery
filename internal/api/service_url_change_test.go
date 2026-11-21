package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request with a new URL and an invalid key is refused whole: nothing is
// saved, so the old URL keeps its old key.
func TestGrimmorySetConfig_InvalidKeyWithNewURLSavesNothing(t *testing.T) {
	h, repo := grimmoryFixture(t)
	ctx := context.Background()
	_ = repo.Set(ctx, SettingGrimmoryBaseURL, "http://localhost")
	_ = repo.Set(ctx, SettingGrimmoryAPIKey, "old-secret")

	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/grimmory/config",
		bytes.NewBufferString(`{"baseUrl":"http://127.0.0.1:9999","apiKey":"bad\u0001key"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	assertSettingValue(t, repo, ctx, SettingGrimmoryBaseURL, "http://localhost")
	assertSettingValue(t, repo, ctx, SettingGrimmoryAPIKey, "old-secret")
}

// The response tells the admin which credentials were dropped, by name only.
func TestGrimmorySetConfig_ReportsClearedCredentials(t *testing.T) {
	h, repo := grimmoryFixture(t)
	ctx := context.Background()
	_ = repo.Set(ctx, SettingGrimmoryBaseURL, "http://localhost")
	_ = repo.Set(ctx, SettingGrimmoryAPIKey, "old-secret")
	_ = repo.Set(ctx, SettingGrimmoryPassword, "old-pass")

	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/grimmory/config",
		bytes.NewBufferString(`{"baseUrl":"http://127.0.0.1:9999"}`)))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"credentialsCleared":["apiKey","password"]`) ||
		!strings.Contains(body, `"apiKeyConfigured":false`) || !strings.Contains(body, `"passwordConfigured":false`) {
		t.Fatalf("response = %d %s", rec.Code, body)
	}
	if strings.Contains(body, "old-secret") || strings.Contains(body, "old-pass") {
		t.Fatalf("response echoed a credential: %s", body)
	}
}

// Deleting a service URL through the generic route unbinds its credentials, so
// a later PUT of a new URL cannot find the old key still saved.
func TestSettingsDeleteThenSet_ServiceURLDropsBoundCredentials(t *testing.T) {
	for _, tc := range []struct {
		urlKey string
		creds  []string
		oldURL string
		newURL string
	}{
		{SettingGrimmoryBaseURL, []string{SettingGrimmoryAPIKey, SettingGrimmoryPassword}, "http://localhost", "http://127.0.0.1:9999"},
		{SettingABSBaseURL, []string{SettingABSAPIKey}, "http://localhost:13378", "http://127.0.0.1:9999"},
		{SettingBookOrbitBaseURL, []string{SettingBookOrbitAPIKey}, "http://localhost:3000", "http://10.9.9.9:3000"},
		{SettingCalibrePluginURL, []string{SettingCalibrePluginAPIKey}, "http://localhost:8099", "http://10.9.9.9:8099"},
	} {
		t.Run(tc.urlKey, func(t *testing.T) {
			h, repo, ctx := settingsFixture(t)
			_ = repo.Set(ctx, tc.urlKey, tc.oldURL)
			for _, c := range tc.creds {
				_ = repo.Set(ctx, c, "secret-"+c)
			}
			rec := httptest.NewRecorder()
			h.Delete(rec, withKey(httptest.NewRequest(http.MethodDelete, "/api/v1/setting/"+tc.urlKey, nil), tc.urlKey))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("delete code = %d: %s", rec.Code, rec.Body.String())
			}
			rec = httptest.NewRecorder()
			h.Set(rec, withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+tc.urlKey, bytes.NewBufferString(`{"value":"`+tc.newURL+`"}`)), tc.urlKey))
			if rec.Code != http.StatusOK {
				t.Fatalf("put code = %d: %s", rec.Code, rec.Body.String())
			}
			for _, c := range tc.creds {
				assertSettingValue(t, repo, ctx, c, "")
			}
		})
	}
}

// Setting a URL where none was, with a credential still stored, is a change.
func TestSettingsSet_URLWhereNoneWasDropsStoredCredential(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	_ = repo.Set(ctx, SettingABSAPIKey, "orphan-secret")
	rec := httptest.NewRecorder()
	h.Set(rec, withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingABSBaseURL, bytes.NewBufferString(`{"value":"http://localhost:13378"}`)), SettingABSBaseURL))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), SettingABSAPIKey) || strings.Contains(rec.Body.String(), "orphan-secret") {
		t.Fatalf("response = %s", rec.Body.String())
	}
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "")
}
