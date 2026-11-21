package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func grimmoryPut(t *testing.T, h *GrimmoryHandler, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/grimmory/config", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGrimmorySetConfig_URLChangeDropsSavedCredentials(t *testing.T) {
	h, repo := grimmoryFixture(t)
	ctx := context.Background()
	_ = repo.Set(ctx, SettingGrimmoryBaseURL, "http://localhost")
	_ = repo.Set(ctx, SettingGrimmoryAPIKey, "old-secret")
	_ = repo.Set(ctx, SettingGrimmoryPassword, "old-pass")

	// Same server spelled differently (slash, host case, default port): kept.
	for _, u := range []string{
		"http://localhost/",
		"http://LOCALHOST",
		"http://localhost:80",
	} {
		grimmoryPut(t, h, `{"baseUrl":"`+u+`"}`)
		assertSettingValue(t, repo, ctx, SettingGrimmoryAPIKey, "old-secret")
		assertSettingValue(t, repo, ctx, SettingGrimmoryPassword, "old-pass")
		// Put the canonical spelling back so each case compares against it.
		_ = repo.Set(ctx, SettingGrimmoryBaseURL, "http://localhost")
	}

	// New URL, nothing supplied: both credentials cleared.
	grimmoryPut(t, h, `{"baseUrl":"http://127.0.0.1:9999"}`)
	assertSettingValue(t, repo, ctx, SettingGrimmoryAPIKey, "")
	assertSettingValue(t, repo, ctx, SettingGrimmoryPassword, "")

	// New URL with a new key and password: stored as given.
	grimmoryPut(t, h, `{"baseUrl":"http://127.0.0.1:9998","apiKey":"fresh","password":"fresh-pass"}`)
	assertSettingValue(t, repo, ctx, SettingGrimmoryAPIKey, "fresh")
	assertSettingValue(t, repo, ctx, SettingGrimmoryPassword, "fresh-pass")

	// New URL with only a key: the key is stored, the old password is not kept.
	grimmoryPut(t, h, `{"baseUrl":"http://127.0.0.1:9997","apiKey":"k4"}`)
	assertSettingValue(t, repo, ctx, SettingGrimmoryAPIKey, "k4")
	assertSettingValue(t, repo, ctx, SettingGrimmoryPassword, "")
}

// The probe must not hand saved credentials to an address the admin has not
// saved.
func TestGrimmoryTest_DoesNotSendSavedCredentialsToOtherURL(t *testing.T) {
	h, repo := grimmoryFixture(t)
	ctx := context.Background()
	var seen []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.Header {
			seen = append(seen, k+"="+strings.Join(v, ","))
		}
		seen = append(seen, r.URL.String())
		_, _ = w.Write([]byte(`{"data":{"status":"UP","version":"1"}}`))
	}))
	t.Cleanup(other.Close)
	_ = repo.Set(ctx, SettingGrimmoryBaseURL, "http://localhost")
	_ = repo.Set(ctx, SettingGrimmoryAPIKey, "old-secret")
	_ = repo.Set(ctx, SettingGrimmoryUsername, "admin")
	_ = repo.Set(ctx, SettingGrimmoryPassword, "old-pass")

	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/grimmory/test", bytes.NewBufferString(`{"baseUrl":"`+other.URL+`"}`)))
	if len(seen) == 0 {
		t.Fatalf("probe never reached the other server: %d %s", rec.Code, rec.Body.String())
	}
	all := strings.Join(seen, "\n")
	for _, secret := range []string{"old-secret", "old-pass"} {
		if strings.Contains(all, secret) {
			t.Fatalf("saved credential %q reached the other URL:\n%s", secret, all)
		}
	}
}

func TestSettingsSet_ServiceURLChangeDropsBoundCredentials(t *testing.T) {
	cases := []struct {
		urlKey string
		creds  []string
		oldURL string
		same   []string
		moved  string
	}{
		{SettingGrimmoryBaseURL, []string{SettingGrimmoryAPIKey, SettingGrimmoryPassword}, "http://localhost", []string{"http://localhost/", "http://LOCALHOST:80"}, "http://127.0.0.1:9999"},
		{SettingABSBaseURL, []string{SettingABSAPIKey}, "http://localhost:13378", []string{"http://localhost:13378/"}, "http://127.0.0.1:9999"},
		{SettingBookOrbitBaseURL, []string{SettingBookOrbitAPIKey}, "http://localhost:3000", []string{"http://localhost:3000/"}, "http://10.9.9.9:3000"},
		{SettingCalibrePluginURL, []string{SettingCalibrePluginAPIKey}, "http://localhost:8099", []string{"http://LOCALHOST:8099/"}, "http://10.9.9.9:8099"},
	}
	for _, tc := range cases {
		t.Run(tc.urlKey, func(t *testing.T) {
			h, repo, ctx := settingsFixture(t)
			_ = repo.Set(ctx, tc.urlKey, tc.oldURL)
			for _, c := range tc.creds {
				_ = repo.Set(ctx, c, "secret-"+c)
			}
			put := func(v string) {
				t.Helper()
				rec := httptest.NewRecorder()
				req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+tc.urlKey, bytes.NewBufferString(`{"value":"`+v+`"}`)), tc.urlKey)
				h.Set(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
				}
			}
			for _, u := range tc.same {
				put(u)
				for _, c := range tc.creds {
					assertSettingValue(t, repo, ctx, c, "secret-"+c)
				}
				put(tc.oldURL)
			}
			put(tc.moved)
			for _, c := range tc.creds {
				assertSettingValue(t, repo, ctx, c, "")
			}
		})
	}
}
