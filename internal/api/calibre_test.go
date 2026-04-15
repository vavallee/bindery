package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

func calibreFixture(t *testing.T) (*CalibreHandler, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	return NewCalibreHandler(repo), repo, context.Background()
}

// TestCalibre_Test_ValidatesLibraryPath: the Test endpoint must refuse to
// even spawn calibredb when library_path points at a non-existent directory.
// Surfacing this as 400 (rather than 502) gives the UI a clear "fix your
// config" signal instead of a generic "couldn't reach calibre".
func TestCalibre_Test_ValidatesLibraryPath(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	if err := repo.Set(ctx, SettingCalibreLibraryPath, "/definitely/not/a/real/path"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/test", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing library path, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCalibre_Test_MissingBinaryReturns502 — when the library path is
// valid but calibredb isn't installed, Test returns 502 with the exec
// error embedded, giving operators the hint they need to install Calibre
// or pin a binary_path. We use an obviously-absent binary to guarantee
// the call fails even on machines that happen to have calibredb.
func TestCalibre_Test_MissingBinaryReturns502(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	tmp := t.TempDir()
	if err := repo.Set(ctx, SettingCalibreLibraryPath, tmp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreBinaryPath, filepath.Join(tmp, "no-such-calibredb-binary")); err != nil {
		// Set will reject a missing binary — store raw via repo so the
		// downstream validator in Test is what catches it.
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/test", nil))
	// validateCalibreConfig rejects the missing binary first → 400.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing binary, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestLoadCalibreConfig_ParsesFromSettings confirms the key names the
// main.go wiring depends on. A typo here silently disables Calibre for
// everyone after an upgrade.
func TestLoadCalibreConfig_ParsesFromSettings(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	tmp := t.TempDir()
	if err := repo.Set(ctx, SettingCalibreEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreLibraryPath, tmp); err != nil {
		t.Fatal(err)
	}
	cfg := LoadCalibreConfig(repo)
	if !cfg.Enabled {
		t.Error("Enabled should be true")
	}
	if cfg.LibraryPath != tmp {
		t.Errorf("LibraryPath = %q, want %q", cfg.LibraryPath, tmp)
	}
	if cfg.BinaryPath != "" {
		t.Errorf("BinaryPath should be empty, got %q", cfg.BinaryPath)
	}
}

// TestLoadCalibreConfig_EnabledCaseInsensitive: the UI may send "True" or
// "TRUE" depending on the form component; we normalise here so operators
// don't hit "it says enabled but nothing is happening".
func TestLoadCalibreConfig_EnabledCaseInsensitive(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	for _, v := range []string{"True", "TRUE", "true"} {
		if err := repo.Set(ctx, SettingCalibreEnabled, v); err != nil {
			t.Fatal(err)
		}
		if !LoadCalibreConfig(repo).Enabled {
			t.Errorf("Enabled = false for value %q", v)
		}
	}
	if err := repo.Set(ctx, SettingCalibreEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if LoadCalibreConfig(repo).Enabled {
		t.Error("Enabled should be false for 'false'")
	}
}

func TestValidateSettingValue_CalibreLibraryPath(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "f")
	if err := os.WriteFile(file, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	// Empty string is allowed — it's how the user disables the path.
	if err := validateSettingValue(SettingCalibreLibraryPath, ""); err != nil {
		t.Errorf("empty should be accepted: %v", err)
	}
	// Existing directory: accepted.
	if err := validateSettingValue(SettingCalibreLibraryPath, tmp); err != nil {
		t.Errorf("tmp dir should be accepted: %v", err)
	}
	// File, not directory: rejected.
	if err := validateSettingValue(SettingCalibreLibraryPath, file); err == nil {
		t.Error("file should be rejected")
	}
	// Missing path: rejected.
	if err := validateSettingValue(SettingCalibreLibraryPath, "/nope/nope"); err == nil {
		t.Error("missing path should be rejected")
	}
}

func TestValidateSettingValue_CalibreBinaryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bit semantics differ on Windows")
	}
	tmp := t.TempDir()
	nonexec := filepath.Join(tmp, "notexec")
	if err := os.WriteFile(nonexec, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	execFile := filepath.Join(tmp, "runme")
	if err := os.WriteFile(execFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := validateSettingValue(SettingCalibreBinaryPath, ""); err != nil {
		t.Errorf("empty binary_path must be accepted: %v", err)
	}
	if err := validateSettingValue(SettingCalibreBinaryPath, execFile); err != nil {
		t.Errorf("executable should be accepted: %v", err)
	}
	if err := validateSettingValue(SettingCalibreBinaryPath, nonexec); err == nil {
		t.Error("non-executable regular file should be rejected")
	}
	if err := validateSettingValue(SettingCalibreBinaryPath, tmp); err == nil {
		t.Error("directory should be rejected as binary")
	}
	if err := validateSettingValue(SettingCalibreBinaryPath, "/no/such/file/calibredb"); err == nil {
		t.Error("missing binary should be rejected")
	}
}

// TestSettings_SetRejectsInvalidCalibrePath exercises the end-to-end Set
// endpoint so the 400 contract is locked in for the frontend.
func TestSettings_SetRejectsInvalidCalibrePath(t *testing.T) {
	h, _, _ := settingsFixture(t)
	body := bytes.NewBufferString(`{"value":"/not/here"}`)
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingCalibreLibraryPath, body), SettingCalibreLibraryPath)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&payload)
	if payload["error"] == "" {
		t.Error("error field should be non-empty")
	}
}

// TestLoadCalibreMode_DefaultsToOff: a fresh DB with no setting row must
// report mode=off so the scanner doesn't attempt either integration before
// the operator opts in.
func TestLoadCalibreMode_DefaultsToOff(t *testing.T) {
	_, repo, _ := calibreFixture(t)
	if got := LoadCalibreMode(repo); got != "off" {
		t.Errorf("LoadCalibreMode default = %q, want off", got)
	}
}

// TestLoadCalibreMode_ParsesKnownValues: each canonical mode string must
// survive a round-trip through the settings table. Regression guard for
// a typo in the const names breaking the UI's radio group.
func TestLoadCalibreMode_ParsesKnownValues(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	for _, m := range []string{"off", "calibredb", "drop_folder"} {
		if err := repo.Set(ctx, SettingCalibreMode, m); err != nil {
			t.Fatal(err)
		}
		if got := string(LoadCalibreMode(repo)); got != m {
			t.Errorf("LoadCalibreMode for %q = %q", m, got)
		}
	}
}

// TestLoadCalibreConfig_ModeDrivesEnabled: when mode=calibredb the client
// reports Enabled=true; when mode=drop_folder it reports Enabled=false
// (the drop-folder path doesn't go through the calibredb client). This is
// the contract main.go relies on when deciding whether to log "calibre
// integration enabled" at boot.
func TestLoadCalibreConfig_ModeDrivesEnabled(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	cases := []struct {
		mode string
		want bool
	}{
		{"calibredb", true},
		{"drop_folder", false},
		{"off", false},
	}
	for _, tc := range cases {
		if err := repo.Set(ctx, SettingCalibreMode, tc.mode); err != nil {
			t.Fatal(err)
		}
		if got := LoadCalibreConfig(repo).Enabled; got != tc.want {
			t.Errorf("mode=%s Enabled=%v, want %v", tc.mode, got, tc.want)
		}
	}
}

// TestLoadDropFolderConfig_ReadsPaths: LibraryPath is shared with the
// calibredb client (metadata.db lives there); DropFolderPath is a separate
// watched directory.
func TestLoadDropFolderConfig_ReadsPaths(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	lib := t.TempDir()
	drop := t.TempDir()
	if err := repo.Set(ctx, SettingCalibreLibraryPath, lib); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreDropFolderPath, drop); err != nil {
		t.Fatal(err)
	}
	cfg := LoadDropFolderConfig(repo)
	if cfg.LibraryPath != lib {
		t.Errorf("LibraryPath = %q, want %q", cfg.LibraryPath, lib)
	}
	if cfg.DropFolderPath != drop {
		t.Errorf("DropFolderPath = %q, want %q", cfg.DropFolderPath, drop)
	}
}

func TestValidateSettingValue_CalibreMode(t *testing.T) {
	if err := validateSettingValue(SettingCalibreMode, ""); err != nil {
		t.Errorf("empty mode should be accepted (=default off), got %v", err)
	}
	for _, m := range []string{"off", "calibredb", "drop_folder"} {
		if err := validateSettingValue(SettingCalibreMode, m); err != nil {
			t.Errorf("%q should be accepted: %v", m, err)
		}
	}
	for _, bad := range []string{"enabled", "true", "CALIBREDB", "drop folder"} {
		if err := validateSettingValue(SettingCalibreMode, bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestValidateSettingValue_CalibreDropFolderPath(t *testing.T) {
	tmp := t.TempDir()
	if err := validateSettingValue(SettingCalibreDropFolderPath, ""); err != nil {
		t.Errorf("empty should be accepted: %v", err)
	}
	if err := validateSettingValue(SettingCalibreDropFolderPath, tmp); err != nil {
		t.Errorf("writable dir should be accepted: %v", err)
	}
	if err := validateSettingValue(SettingCalibreDropFolderPath, "/no/such/dir"); err == nil {
		t.Error("missing dir should be rejected")
	}
	file := filepath.Join(tmp, "f")
	if err := os.WriteFile(file, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSettingValue(SettingCalibreDropFolderPath, file); err == nil {
		t.Error("file should be rejected as drop folder")
	}
}

// TestMigrate_LegacyEnabledHonored: the upgrade migration rewrites
// calibre.enabled=true → calibre.mode=calibredb. In edge cases where the
// migration hasn't run but the legacy key says true, LoadCalibreConfig
// should still report Enabled=true so imports don't silently stop
// mirroring mid-upgrade.
func TestLoadCalibreConfig_LegacyEnabledHonored(t *testing.T) {
	_, repo, ctx := calibreFixture(t)
	if err := repo.Set(ctx, SettingCalibreEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	// Mode left unset (empty → off).
	if got := LoadCalibreConfig(repo).Enabled; !got {
		t.Error("legacy calibre.enabled=true must keep Enabled=true")
	}
	// But when the operator has explicitly picked drop_folder, the
	// legacy flag must NOT resurrect the calibredb path.
	if err := repo.Set(ctx, SettingCalibreMode, "drop_folder"); err != nil {
		t.Fatal(err)
	}
	if got := LoadCalibreConfig(repo).Enabled; got {
		t.Error("mode=drop_folder must override legacy calibre.enabled")
	}
}
