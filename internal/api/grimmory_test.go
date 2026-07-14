package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

func grimmoryFixture(t *testing.T) (*GrimmoryHandler, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	return NewGrimmoryHandler(settings), settings
}

// TestGrimmoryConfigRoundTrip exercises SetConfig -> GetConfig and asserts the
// values actually persist to the settings repo (api key is stored but only the
// "configured" boolean is returned).
func TestGrimmoryConfigRoundTrip(t *testing.T) {
	h, settings := grimmoryFixture(t)
	ctx := context.Background()

	// Initially empty.
	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/grimmory/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}
	var cfg GrimmoryConfigResponse
	json.NewDecoder(rec.Body).Decode(&cfg)
	if cfg.Enabled || cfg.BaseURL != "" || cfg.APIKeyConfigured {
		t.Fatalf("expected empty config, got %+v", cfg)
	}

	// Save a full config. Use an RFC1918 literal so the SSRF LAN policy passes.
	body := `{"enabled":true,"baseUrl":"http://10.20.30.40:8080/","apiKey":"  topsecret  "}`
	rec = httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPost, "/grimmory/config", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("set: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	json.NewDecoder(rec.Body).Decode(&cfg)
	if !cfg.Enabled {
		t.Errorf("enabled not echoed back")
	}
	// NormalizeBaseURL strips the trailing slash.
	if cfg.BaseURL != "http://10.20.30.40:8080" {
		t.Errorf("baseUrl = %q, want normalized http://10.20.30.40:8080", cfg.BaseURL)
	}
	if !cfg.APIKeyConfigured {
		t.Errorf("apiKeyConfigured should be true after saving a key")
	}

	// Verify persistence directly in the settings table.
	stored := LoadGrimmoryConfig(settings)
	if !stored.Enabled {
		t.Errorf("enabled not persisted")
	}
	if stored.BaseURL != "http://10.20.30.40:8080" {
		t.Errorf("baseUrl not persisted: %q", stored.BaseURL)
	}
	// NormalizeAPIKey trims surrounding whitespace.
	if stored.APIKey != "topsecret" {
		t.Errorf("apiKey not persisted/trimmed: %q", stored.APIKey)
	}

	// GetConfig must redact the key (only the boolean is exposed).
	rec = httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/grimmory/config", nil))
	rawBody := rec.Body.String()
	if contains := bytes.Contains([]byte(rawBody), []byte("topsecret")); contains {
		t.Errorf("GetConfig leaked the api key: %s", rawBody)
	}

	_ = ctx
}

// TestGrimmorySetConfig_Partial confirms a partial update only touches the
// supplied fields and leaves the rest intact.
func TestGrimmorySetConfig_Partial(t *testing.T) {
	h, settings := grimmoryFixture(t)
	ctx := context.Background()
	if err := settings.Set(ctx, SettingGrimmoryBaseURL, "http://10.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, SettingGrimmoryEnabled, "true"); err != nil {
		t.Fatal(err)
	}

	// Only toggle enabled off; baseUrl must survive.
	body := `{"enabled":false}`
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPost, "/grimmory/config", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	stored := LoadGrimmoryConfig(settings)
	if stored.Enabled {
		t.Errorf("enabled should be false")
	}
	if stored.BaseURL != "http://10.0.0.1:8080" {
		t.Errorf("baseUrl should be untouched, got %q", stored.BaseURL)
	}
}

func TestGrimmorySetConfig_BadJSON(t *testing.T) {
	h, _ := grimmoryFixture(t)
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPost, "/grimmory/config", bytes.NewBufferString(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestGrimmorySetConfig_InvalidBaseURL(t *testing.T) {
	h, _ := grimmoryFixture(t)
	// ftp scheme is rejected by ValidateBaseURLSecure.
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPost, "/grimmory/config", bytes.NewBufferString(`{"baseUrl":"ftp://example.com"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid base url, got %d", rec.Code)
	}
}

// TestGrimmoryTest_Success points the handler at a fake Grimmory
// /api/v1/healthcheck endpoint and asserts the version is reported.
func TestGrimmoryTest_Success(t *testing.T) {
	h, _ := grimmoryFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/healthcheck" {
			_, _ = w.Write([]byte(`{"data":{"status":"UP","version":"3.4.1"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// Override credentials in the request body (loopback URL via NormalizeBaseURL).
	body := `{"baseUrl":"` + srv.URL + `","apiKey":"k"}`
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/grimmory/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out grimmoryTestResponse
	json.NewDecoder(rec.Body).Decode(&out)
	if !out.OK {
		t.Errorf("expected ok=true, got %+v", out)
	}
	if out.Version != "3.4.1" {
		t.Errorf("version not surfaced: %+v", out)
	}
}

// TestGrimmoryTest_UpstreamFailure asserts a 5xx from Grimmory yields a 502 with
// ok=false.
func TestGrimmoryTest_UpstreamFailure(t *testing.T) {
	h, _ := grimmoryFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	body := `{"baseUrl":"` + srv.URL + `"}`
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/grimmory/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	var out grimmoryTestResponse
	json.NewDecoder(rec.Body).Decode(&out)
	if out.OK {
		t.Errorf("expected ok=false on upstream failure, got %+v", out)
	}
	if out.Message == "" {
		t.Errorf("expected a non-empty failure message")
	}
}

// TestGrimmoryTest_InvalidBaseURL: a base url that fails client construction
// (empty / bad scheme) yields a 400 before any network call.
func TestGrimmoryTest_InvalidBaseURL(t *testing.T) {
	h, _ := grimmoryFixture(t)
	// No stored config and no override -> empty base url -> NewClient errors.
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/grimmory/test", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty base url, got %d: %s", rec.Code, rec.Body.String())
	}
	var out grimmoryTestResponse
	json.NewDecoder(rec.Body).Decode(&out)
	if out.OK {
		t.Errorf("expected ok=false, got %+v", out)
	}
}

// TestGrimmoryTest_UsesStoredConfig confirms Test falls back to the persisted
// base url when the request body omits it.
func TestGrimmoryTest_UsesStoredConfig(t *testing.T) {
	h, settings := grimmoryFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/healthcheck" {
			_, _ = w.Write([]byte(`{"data":{"status":"UP","version":"9.9.9"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	if err := settings.Set(context.Background(), SettingGrimmoryBaseURL, srv.URL); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/grimmory/test", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 using stored config, got %d: %s", rec.Code, rec.Body.String())
	}
	var out grimmoryTestResponse
	json.NewDecoder(rec.Body).Decode(&out)
	if !out.OK || out.Version != "9.9.9" {
		t.Errorf("stored-config probe wrong: %+v", out)
	}
}
