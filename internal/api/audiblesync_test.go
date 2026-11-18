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

	"github.com/vavallee/bindery/internal/audiblesync"
	"github.com/vavallee/bindery/internal/db"
)

type fakeAudibleSyncProber struct {
	health   *audiblesync.Health
	healthEr error
	accounts []audiblesync.Account
	accErr   error
}

func (f *fakeAudibleSyncProber) Health(context.Context) (*audiblesync.Health, error) {
	return f.health, f.healthEr
}
func (f *fakeAudibleSyncProber) Accounts(context.Context) ([]audiblesync.Account, error) {
	return f.accounts, f.accErr
}

func audibleSyncFixture(t *testing.T) (*AudibleSyncHandler, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	return NewAudibleSyncHandler(settings), settings
}

func putAudibleSyncConfig(t *testing.T, h *AudibleSyncHandler, body string) (*httptest.ResponseRecorder, AudibleSyncConfigResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/audiblesync/config", bytes.NewBufferString(body)))
	var cfg AudibleSyncConfigResponse
	_ = json.NewDecoder(rec.Body).Decode(&cfg)
	return rec, cfg
}

func TestAudibleSyncConfigRoundTripRedactsToken(t *testing.T) {
	h, settings := audibleSyncFixture(t)

	rec, cfg := putAudibleSyncConfig(t, h, `{"enabled":true,"baseUrl":"http://10.0.0.5:8080/","apiToken":" secret "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	if !cfg.Enabled || cfg.BaseURL != "http://10.0.0.5:8080" || !cfg.APITokenConfigured {
		t.Fatalf("cfg = %+v", cfg)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("the token leaked into the response")
	}
	if got, _ := settings.Get(context.Background(), SettingAudibleSyncAPIToken); got == nil || got.Value != "secret" {
		t.Fatalf("stored token = %+v", got)
	}

	// An empty token keeps the stored one.
	_, cfg = putAudibleSyncConfig(t, h, `{"apiToken":"","enabled":false}`)
	if !cfg.APITokenConfigured || cfg.Enabled {
		t.Fatalf("after partial save cfg = %+v", cfg)
	}
}

func TestAudibleSyncSetConfigValidatesBeforeWriting(t *testing.T) {
	h, settings := audibleSyncFixture(t)
	for _, body := range []string{
		`{"enabled":true,"baseUrl":"http://169.254.169.254/"}`,
		`{"enabled":true,"baseUrl":"10.0.0.5:8080"}`,
		`{"enabled":true,"apiToken":"a\nb"}`,
	} {
		rec, _ := putAudibleSyncConfig(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", body, rec.Code)
		}
	}
	// Nothing from the refused requests was stored, not even enabled.
	if got, _ := settings.Get(context.Background(), SettingAudibleSyncEnabled); got != nil && got.Value != "" {
		t.Fatalf("enabled was written by a refused request: %q", got.Value)
	}
}

func TestAudibleSyncTestNeedsSavedURL(t *testing.T) {
	h, _ := audibleSyncFixture(t)
	h.newFn = func(string, string) (audibleSyncProber, error) {
		t.Error("no client should be built without a saved URL")
		return nil, errors.New("unreachable")
	}
	rec := httptest.NewRecorder()
	// A URL in the body is ignored: Test only ever probes the saved one.
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/audiblesync/test", bytes.NewBufferString(`{"baseUrl":"http://10.9.9.9","apiToken":"x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAudibleSyncTestReportsAccountsAndBadToken(t *testing.T) {
	h, _ := audibleSyncFixture(t)
	putAudibleSyncConfig(t, h, `{"enabled":true,"baseUrl":"http://10.0.0.5:8080","apiToken":"stored"}`)

	var gotToken string
	prober := &fakeAudibleSyncProber{
		health:   &audiblesync.Health{Status: "ok", Version: "1.4.0"},
		accounts: []audiblesync.Account{{ID: 1, Name: "Main", CountryCode: "uk", Enabled: true}},
	}
	h.newFn = func(_ string, token string) (audibleSyncProber, error) {
		gotToken = token
		return prober, nil
	}

	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/audiblesync/test", bytes.NewBufferString(`{"apiToken":"typed"}`)))
	var resp audibleSyncTestResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if rec.Code != http.StatusOK || !resp.OK || resp.Version != "1.4.0" || len(resp.Accounts) != 1 || resp.Accounts[0].CountryCode != "uk" {
		t.Fatalf("%d %+v", rec.Code, resp)
	}
	if gotToken != "typed" {
		t.Fatalf("a typed token should be tried in place of the stored one, got %q", gotToken)
	}

	prober.accErr = audiblesync.ErrUnauthorized
	rec = httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/audiblesync/test", nil))
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if rec.Code != http.StatusBadGateway || resp.OK || !strings.Contains(resp.Message, "rejected the API token") {
		t.Fatalf("%d %+v", rec.Code, resp)
	}
	if gotToken != "stored" {
		t.Fatalf("with no typed token the stored one is used, got %q", gotToken)
	}
}

func TestLoadAudibleSyncConfigUsable(t *testing.T) {
	_, settings := audibleSyncFixture(t)
	ctx := context.Background()
	if cfg, err := LoadAudibleSyncConfig(ctx, settings); err != nil || cfg.Usable() {
		t.Fatalf("empty config = %+v, %v; must load and not be usable", cfg, err)
	}
	_ = settings.SetMany(ctx, []db.SettingKV{
		{Key: SettingAudibleSyncEnabled, Value: "true"},
		{Key: SettingAudibleSyncBaseURL, Value: "http://10.0.0.5:8080"},
		{Key: SettingAudibleSyncAPIToken, Value: "t"},
	})
	cfg, err := LoadAudibleSyncConfig(ctx, settings)
	if err != nil || !cfg.Usable() {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

// A failed read is an error, not an empty setting that would pass for "not
// configured".
func TestLoadAudibleSyncConfigReportsAFailedRead(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	database.Close()
	if _, err := LoadAudibleSyncConfig(context.Background(), settings); err == nil {
		t.Fatal("a failed settings read must be reported")
	}
}

func TestAudibleSyncTokenIsSecret(t *testing.T) {
	if !isSecretSetting(SettingAudibleSyncAPIToken) {
		t.Fatal("the Audible Sync token must never be returned by the settings API")
	}
	if isWritableSecretSetting(SettingAudibleSyncAPIToken) {
		t.Fatal("the token is written by the Audible Sync screen, not the generic PUT")
	}
	if !isAdminOnlySetting(SettingAudibleSyncBaseURL) {
		t.Fatal("the instance URL is operator detail")
	}
}
