package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestABSSetConfigCoverage covers the refusals (bad body, bad base URL, a key
// with control characters; none may write) and the defaults on a valid save:
// a blank label falls back to "Audiobookshelf" and a single libraryId becomes
// the one-entry library list.
func TestABSSetConfigCoverage(t *testing.T) {
	h, repo, ctx := absFixture(t, nil)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/abs/config", strings.NewReader(body)))
		return rec
	}
	for _, c := range []struct {
		name, body, wantErr string
	}{
		{"bad body", `{"baseUrl":`, "invalid request body"},
		{"schemeless url", `{"baseUrl":"abs.local:13378"}`, "missing a scheme"},
		{"wrong scheme", `{"baseUrl":"ftp://abs.local"}`, "must use http or https"},
		{"control chars in key", `{"baseUrl":"http://abs.local","apiKey":"ab\u0001cd"}`, "invalid control characters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := put(c.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.wantErr) {
				t.Fatalf("%d %s, want 400 %q", rec.Code, rec.Body.String(), c.wantErr)
			}
			if s, _ := repo.Get(ctx, SettingABSBaseURL); s != nil && s.Value != "" {
				t.Fatalf("a refused save stored base url %q", s.Value)
			}
		})
	}

	rec := put(`{"baseUrl":"http://abs.local:13378/","label":"  ","libraryId":" lib-1 ","apiKey":"tok","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"tok"`) {
		t.Fatalf("response leaked the api key: %s", rec.Body.String())
	}
	var got ABSConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Label != "Audiobookshelf" || got.LibraryID != "lib-1" || len(got.LibraryIDs) != 1 || got.LibraryIDs[0] != "lib-1" || !got.APIKeyConfigured || !got.Enabled {
		t.Fatalf("response = %+v", got)
	}
	assertSettingValue(t, repo, ctx, SettingABSLabel, "Audiobookshelf")
	assertSettingValue(t, repo, ctx, SettingABSLibraryID, "lib-1")
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "tok")

	// Omitting the key on a later save keeps the stored one.
	if rec := put(`{"label":"Shelf"}`); rec.Code != http.StatusOK {
		t.Fatalf("relabel: %d %s", rec.Code, rec.Body.String())
	}
	assertSettingValue(t, repo, ctx, SettingABSAPIKey, "tok")
	assertSettingValue(t, repo, ctx, SettingABSLabel, "Shelf")
}
