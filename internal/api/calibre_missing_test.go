package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withoutCalibredb points PATH at an empty folder, which is what the
// official distroless image looks like to a calibredb lookup (#1940).
func withoutCalibredb(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// TestCalibre_Test_NoCalibredbNamesTheBridge replays #1940 at the Test
// button. With calibredb mode on the official image, Test answered the raw
// exec error, which says nothing about the image or the way out.
func TestCalibre_Test_NoCalibredbNamesTheBridge(t *testing.T) {
	withoutCalibredb(t)
	h, repo, ctx := calibreFixture(t)
	if err := repo.Set(ctx, SettingCalibreMode, "calibredb"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreLibraryPath, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/test", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "calibredb_missing" {
		t.Errorf("#1940: the failure must carry code %q for the settings tab, got %q", "calibredb_missing", body["code"])
	}
	for _, want := range []string{"does not include Calibre", "Calibre Bridge plugin"} {
		if !strings.Contains(body["error"], want) {
			t.Errorf("#1940: the failure must say %q, got %q", want, body["error"])
		}
	}
}

// TestSettings_SetCalibredbModeWarnsWhenMissing replays #1940 at save time.
// Choosing calibredb mode with no calibredb saved silently, and from then on
// every import looked successful while nothing reached Calibre. The mode
// still saves, so a user about to install Calibre is not blocked, but the
// save must say it cannot work as things are.
func TestSettings_SetCalibredbModeWarnsWhenMissing(t *testing.T) {
	withoutCalibredb(t)
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, SettingCalibreLibraryPath, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	set := func(key, value string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+key, bytes.NewBufferString(`{"value":"`+value+`"}`)), key)
		h.Set(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s=%s: got %d: %s", key, value, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	body := set(SettingCalibreMode, "calibredb")
	if body["key"] != SettingCalibreMode || body["value"] != "calibredb" {
		t.Errorf("the response must still carry the saved setting, got %v", body)
	}
	if body["warningCode"] != "calibredb_missing" {
		t.Errorf("#1940: saving calibredb mode with no calibredb must warn, got %v", body)
	}
	if w, _ := body["warning"].(string); !strings.Contains(w, "Calibre Bridge plugin") {
		t.Errorf("#1940: the warning must point at the Bridge plugin, got %q", w)
	}
	if got, _ := repo.Get(ctx, SettingCalibreMode); got == nil || got.Value != "calibredb" {
		t.Errorf("the mode must still be saved, got %v", got)
	}

	if body := set(SettingCalibreMode, "plugin"); body["warning"] != nil || body["warningCode"] != nil {
		t.Errorf("plugin mode does not need calibredb and must not warn, got %v", body)
	}
}
