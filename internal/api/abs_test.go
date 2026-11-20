package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
)

type stubABSClient struct {
	authorizeResp *abs.AuthorizeResponse
	authorizeErr  error
	librariesResp []abs.Library
	librariesErr  error
	libraryResp   *abs.Library
	libraryErr    error
	lastLibraryID string
}

type absClientFactoryCall struct {
	baseURL string
	apiKey  string
}

func (s *stubABSClient) Authorize(context.Context) (*abs.AuthorizeResponse, error) {
	return s.authorizeResp, s.authorizeErr
}

func (s *stubABSClient) ListLibraries(context.Context) ([]abs.Library, error) {
	return s.librariesResp, s.librariesErr
}

func (s *stubABSClient) GetLibrary(_ context.Context, id string) (*abs.Library, error) {
	s.lastLibraryID = id
	return s.libraryResp, s.libraryErr
}

func absFixture(t *testing.T, client absClient) (*ABSHandler, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	h := NewABSHandler(repo)
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		if client == nil {
			return nil, errors.New("no client")
		}
		return client, nil
	}
	return h, repo, context.Background()
}

func assertSettingValue(t *testing.T, repo *db.SettingsRepo, ctx context.Context, key, want string) {
	t.Helper()
	got, err := repo.Get(ctx, key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if got == nil || got.Value != want {
		t.Fatalf("%s = %+v, want %q", key, got, want)
	}
}

func TestABSConfigGetRedactsAPIKey(t *testing.T) {
	h, repo, ctx := absFixture(t, &stubABSClient{})
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "secret"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if bytes.Contains([]byte(body), []byte("secret")) {
		t.Fatalf("api key leaked in response: %s", body)
	}
	if !bytes.Contains([]byte(body), []byte(`"apiKeyConfigured":true`)) {
		t.Fatalf("expected apiKeyConfigured=true, got %s", body)
	}
}

func TestABSConfigGetIncludesFeatureFlag(t *testing.T) {
	h, _, _ := absFixture(t, &stubABSClient{})
	h.WithFeatureEnabled(false)

	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"featureEnabled":false`)) {
		t.Fatalf("expected featureEnabled=false, got %s", rec.Body.String())
	}
}

func TestABSSetConfigSavesLibraryIDWithoutLiveProbe(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)
	called := false
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		called = true
		return nil, errors.New("unexpected live probe")
	}

	body := bytes.NewBufferString(`{"baseUrl":"https://abs.example.com/","apiKey":"secret","libraryId":"lib_books","enabled":true,"label":"Shelf"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("client factory was called during config save")
	}
	got, _ := repo.Get(ctx, SettingABSLibraryID)
	if got == nil || got.Value != "lib_books" {
		t.Fatalf("library id not persisted: %+v", got)
	}
	got, _ = repo.Get(ctx, SettingABSLibraryIDs)
	if got == nil || got.Value != `["lib_books"]` {
		t.Fatalf("library ids not persisted: %+v", got)
	}
	got, _ = repo.Get(ctx, SettingABSAPIKey)
	if got == nil || got.Value != "secret" {
		t.Fatalf("api key not persisted: %+v", got)
	}
}

func TestABSSetConfigPersistsLibraryIDs(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)

	body := bytes.NewBufferString(`{"baseUrl":"https://abs.example.com/","apiKey":"secret","libraryIds":["lib_books","lib_audio","lib_books"],"enabled":true,"label":"Shelf"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	assertSettingValue(t, repo, ctx, SettingABSLibraryID, "lib_books")
	assertSettingValue(t, repo, ctx, SettingABSLibraryIDs, `["lib_books","lib_audio"]`)

	var cfg ABSConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if cfg.LibraryID != "lib_books" {
		t.Fatalf("libraryId = %q, want lib_books", cfg.LibraryID)
	}
	if got, want := strings.Join(cfg.LibraryIDs, ","), "lib_books,lib_audio"; got != want {
		t.Fatalf("libraryIds = %q, want %q", got, want)
	}
}

func TestABSGetConfigFallsBackToLegacyLibraryID(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)
	if err := repo.Set(ctx, SettingABSLibraryID, "lib_legacy"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var cfg ABSConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if cfg.LibraryID != "lib_legacy" {
		t.Fatalf("libraryId = %q, want lib_legacy", cfg.LibraryID)
	}
	if got, want := strings.Join(cfg.LibraryIDs, ","), "lib_legacy"; got != want {
		t.Fatalf("libraryIds = %q, want %q", got, want)
	}
}

func TestABSGetConfigFallsBackToLegacyLibraryIDWhenLibraryIDsEmpty(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)
	if err := repo.Set(ctx, SettingABSLibraryID, "lib_legacy"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSLibraryIDs, "[]"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var cfg ABSConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if cfg.LibraryID != "lib_legacy" {
		t.Fatalf("libraryId = %q, want lib_legacy", cfg.LibraryID)
	}
	if got, want := strings.Join(cfg.LibraryIDs, ","), "lib_legacy"; got != want {
		t.Fatalf("libraryIds = %q, want %q", got, want)
	}
}

func TestABSSetConfigAllowsEmptyLibraryIDs(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)

	body := bytes.NewBufferString(`{"baseUrl":"https://abs.example.com/","apiKey":"secret","libraryIds":[],"enabled":true,"label":"Shelf"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	assertSettingValue(t, repo, ctx, SettingABSLibraryID, "")
	assertSettingValue(t, repo, ctx, SettingABSLibraryIDs, `[]`)
	cfg := LoadABSConfig(ctx, repo)
	if cfg.LibraryID != "" || len(cfg.LibraryIDs) != 0 {
		t.Fatalf("cfg libraries = %q/%v, want empty", cfg.LibraryID, cfg.LibraryIDs)
	}
}

func TestABSSetConfigSavesEditableFieldsWhenABSUnreachable(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "stored-secret"); err != nil {
		t.Fatal(err)
	}
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		return nil, errors.New("abs is unreachable")
	}

	body := bytes.NewBufferString(`{"label":"Offline Shelf","enabled":true,"libraryId":"lib_offline"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	assertSettingValue(t, repo, ctx, SettingABSLabel, "Offline Shelf")
	assertSettingValue(t, repo, ctx, SettingABSEnabled, "true")
	assertSettingValue(t, repo, ctx, SettingABSLibraryID, "lib_offline")
}

func TestABSSetConfig_OmittedBaseURLPreservesStoredValue(t *testing.T) {
	client := &stubABSClient{
		libraryResp: &abs.Library{ID: "lib_books", Name: "Books", MediaType: "book"},
	}
	h, repo, ctx := absFixture(t, client)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "old-secret"); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"apiKey":"new-secret","libraryId":"lib_books","enabled":true}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := repo.Get(ctx, SettingABSBaseURL)
	if got == nil || got.Value != "https://abs.example.com" {
		t.Fatalf("base URL = %+v, want preserved value", got)
	}
}

func TestABSSetConfig_URLChangeDropsSavedKey(t *testing.T) {
	h, repo, ctx := absFixture(t, &stubABSClient{})
	_ = repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com")
	_ = repo.Set(ctx, SettingABSAPIKey, "old-secret")
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", bytes.NewBufferString(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
		}
	}
	// Same URL: key stays.
	put(`{"baseUrl":"https://abs.example.com/"}`)
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "old-secret")
	// New URL, no key: key cleared.
	put(`{"baseUrl":"https://other.example.net"}`)
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "")
	// New URL with a key: saved as given.
	put(`{"baseUrl":"https://third.example.net","apiKey":"fresh-secret"}`)
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "fresh-secret")
}

func TestABSSetConfig_PersistsPathRemap(t *testing.T) {
	client := &stubABSClient{
		libraryResp: &abs.Library{ID: "lib_books", Name: "Books", MediaType: "book"},
	}
	h, repo, _ := absFixture(t, client)

	body := bytes.NewBufferString(`{"baseUrl":"https://abs.example.com/","apiKey":"secret","libraryId":"lib_books","enabled":true,"label":"Shelf","pathRemap":"/audiobookshelf:/books/audiobookshelf"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.Get(context.Background(), SettingABSPathRemap)
	if got == nil || got.Value != "/audiobookshelf:/books/audiobookshelf" {
		t.Fatalf("path remap not persisted: %+v", got)
	}

	rec = httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GetConfig code = %d", rec.Code)
	}
	var cfg ABSConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.PathRemap != "/audiobookshelf:/books/audiobookshelf" {
		t.Fatalf("pathRemap = %q", cfg.PathRemap)
	}
}

// TestABSSetConfig_PersistsAllRowsAtomically verifies a successful SetConfig
// writes every settings row via the atomic SetMany batch. A partial write
// would leave a half-applied config (issue #715 finding 6).
func TestABSSetConfig_PersistsAllRowsAtomically(t *testing.T) {
	client := &stubABSClient{
		libraryResp: &abs.Library{ID: "lib_books", Name: "Books", MediaType: "book"},
	}
	h, repo, ctx := absFixture(t, client)

	body := bytes.NewBufferString(`{"baseUrl":"https://abs.example.com/","apiKey":"secret","libraryId":"lib_books","enabled":true,"label":"Shelf","pathRemap":"/abs:/books"}`)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}

	assertSettingValue(t, repo, ctx, SettingABSBaseURL, "https://abs.example.com")
	assertSettingValue(t, repo, ctx, SettingABSLabel, "Shelf")
	assertSettingValue(t, repo, ctx, SettingABSEnabled, "true")
	assertSettingValue(t, repo, ctx, SettingABSLibraryID, "lib_books")
	assertSettingValue(t, repo, ctx, SettingABSPathRemap, "/abs:/books")
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "secret")
}

func TestABSTestMapsUnauthorizedTo502(t *testing.T) {
	client := &stubABSClient{
		authorizeErr: &abs.APIError{StatusCode: http.StatusUnauthorized, Message: "bad key"},
	}
	h, repo, ctx := absFixture(t, client)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "secret"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/test", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestABSLibrariesMapsRemoteFailureTo502(t *testing.T) {
	client := &stubABSClient{
		librariesErr: errors.New("connect: connection refused"),
	}
	h, repo, ctx := absFixture(t, client)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "secret"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Libraries(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/libraries", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("body = %s, want remote failure", rec.Body.String())
	}
}

func TestABSTestUsesStoredBaseURLAndAllowsAPIKeyOverride(t *testing.T) {
	client := &stubABSClient{
		authorizeResp: &abs.AuthorizeResponse{
			User: abs.User{Username: "root", Type: "root"},
		},
	}
	h, repo, ctx := absFixture(t, client)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://stored.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "stored-secret"); err != nil {
		t.Fatal(err)
	}
	var calls []absClientFactoryCall
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		calls = append(calls, absClientFactoryCall{baseURL: baseURL, apiKey: apiKey})
		return client, nil
	}

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"baseUrl":"http://127.0.0.1:9","apiKey":"draft!#$%&'*+.^_|~?=;:,/[]{}()"}`)
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/test", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 {
		t.Fatalf("factory calls = %d, want 1", len(calls))
	}
	if calls[0].baseURL != "https://stored.example.com" {
		t.Fatalf("baseURL = %q, want stored config value", calls[0].baseURL)
	}
	if calls[0].apiKey != "draft!#$%&'*+.^_|~?=;:,/[]{}()" {
		t.Fatalf("apiKey = %q, want request override", calls[0].apiKey)
	}
}

func TestABSProbeRejectsAPIKeyControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		body string
		leak string
	}{
		{
			name: "crlf injection",
			body: `{"apiKey":"bad\r\nX-Test: injected"}`,
			leak: "X-Test: injected",
		},
		{
			name: "nul",
			body: `{"apiKey":"bad\u0000secret"}`,
			leak: "bad\x00secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo, ctx := absFixture(t, &stubABSClient{})
			if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
				t.Fatal(err)
			}
			called := false
			h.newFn = func(baseURL, apiKey string) (absClient, error) {
				called = true
				return &stubABSClient{}, nil
			}

			rec := httptest.NewRecorder()
			h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/test", bytes.NewBufferString(tt.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "control characters") {
				t.Fatalf("body = %s, want control character error", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tt.leak) {
				t.Fatalf("response leaked api key: %q", rec.Body.String())
			}
			if called {
				t.Fatal("client factory was called with an invalid api key")
			}
		})
	}
}

func TestABSProbeRejectsStoredAPIKeyControlCharacters(t *testing.T) {
	h, repo, ctx := absFixture(t, &stubABSClient{})
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "stored\r\nX-Test: injected"); err != nil {
		t.Fatal(err)
	}
	called := false
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		called = true
		return &stubABSClient{}, nil
	}

	rec := httptest.NewRecorder()
	h.Libraries(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/libraries", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "X-Test: injected") {
		t.Fatalf("response leaked api key: %q", rec.Body.String())
	}
	if called {
		t.Fatal("client factory was called with an invalid stored api key")
	}
}

func TestABSProbeRequiresSavedBaseURL(t *testing.T) {
	tests := []struct {
		name string
		call func(*ABSHandler, http.ResponseWriter, *http.Request)
		path string
	}{
		{
			name: "test",
			call: (*ABSHandler).Test,
			path: "/api/v1/abs/test",
		},
		{
			name: "libraries",
			call: (*ABSHandler).Libraries,
			path: "/api/v1/abs/libraries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := absFixture(t, &stubABSClient{})
			called := false
			h.newFn = func(baseURL, apiKey string) (absClient, error) {
				called = true
				return &stubABSClient{}, nil
			}

			rec := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"baseUrl":"http://127.0.0.1:9","apiKey":"draft-secret"}`)
			tt.call(h, rec, httptest.NewRequest(http.MethodPost, tt.path, body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "base URL must be saved") {
				t.Fatalf("body = %s, want clear saved base URL error", rec.Body.String())
			}
			if called {
				t.Fatal("client factory was called without a saved base URL")
			}
		})
	}
}

func TestABSLibrariesFiltersToBookLibraries(t *testing.T) {
	client := &stubABSClient{
		librariesResp: []abs.Library{
			{ID: "lib_books", Name: "Books", MediaType: "book"},
			{ID: "lib_podcasts", Name: "Podcasts", MediaType: "podcast"},
		},
	}
	h, repo, ctx := absFixture(t, client)
	if err := repo.Set(ctx, SettingABSBaseURL, "https://abs.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingABSAPIKey, "secret"); err != nil {
		t.Fatal(err)
	}
	var calls []absClientFactoryCall
	h.newFn = func(baseURL, apiKey string) (absClient, error) {
		calls = append(calls, absClientFactoryCall{baseURL: baseURL, apiKey: apiKey})
		return client, nil
	}

	rec := httptest.NewRecorder()
	h.Libraries(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/libraries", bytes.NewBufferString(`{"baseUrl":"http://127.0.0.1:9"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 {
		t.Fatalf("factory calls = %d, want 1", len(calls))
	}
	if calls[0].baseURL != "https://abs.example.com" {
		t.Fatalf("baseURL = %q, want stored config value", calls[0].baseURL)
	}
	if calls[0].apiKey != "secret" {
		t.Fatalf("apiKey = %q, want stored config value", calls[0].apiKey)
	}
	var got []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0]["id"] != "lib_books" {
		t.Fatalf("id = %v", got[0]["id"])
	}
}
