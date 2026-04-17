package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

// Calibre settings keys. Centralised so the handler and main.go agree on
// the exact names and nobody drifts to "calibre_enabled" vs "calibre.enabled".
const (
	SettingCalibreEnabled       = "calibre.enabled"
	SettingCalibreLibraryPath   = "calibre.library_path"
	SettingCalibreBinaryPath    = "calibre.binary_path"
	SettingCalibreMode          = "calibre.mode"
	SettingCalibreSyncOnStartup = "calibre.sync_on_startup"
)

// CalibreHandler exposes the "test connection" endpoint for the Calibre
// settings UI. Read/write of the calibre.* keys themselves go through the
// generic /setting endpoints so the UI can reuse its existing plumbing;
// this handler just validates and probes.
type CalibreHandler struct {
	settings *db.SettingsRepo
}

func NewCalibreHandler(settings *db.SettingsRepo) *CalibreHandler {
	return &CalibreHandler{settings: settings}
}

// LoadCalibreConfig materialises a calibre.Config from the settings table.
// Exported so main.go can build the importer's Calibre client at boot and
// refresh it on each scheduler tick.
func LoadCalibreConfig(settings *db.SettingsRepo) calibre.Config {
	ctx := contextBackground()
	get := func(key string) string {
		s, _ := settings.Get(ctx, key)
		if s == nil {
			return ""
		}
		return s.Value
	}
	mode := LoadCalibreMode(settings)
	enabled := mode == calibre.ModeCalibredb
	// Back-compat: if the operator still has the v0.8.0 `calibre.enabled`
	// boolean set to true but the migration hasn't run yet (e.g. someone
	// restored an old DB), honour it so the first import doesn't silently
	// downgrade to off.
	if !enabled && strings.EqualFold(get(SettingCalibreEnabled), "true") {
		enabled = true
	}
	return calibre.Config{
		Enabled:       enabled,
		LibraryPath:   get(SettingCalibreLibraryPath),
		BinaryPath:    get(SettingCalibreBinaryPath),
		SyncOnStartup: strings.EqualFold(get(SettingCalibreSyncOnStartup), "true"),
	}
}

// LoadCalibreMode returns the currently-configured integration mode. The
// scanner calls this on every import so toggling the radio in Settings
// takes effect without a restart.
func LoadCalibreMode(settings *db.SettingsRepo) calibre.Mode {
	s, _ := settings.Get(contextBackground(), SettingCalibreMode)
	if s == nil {
		return calibre.ModeOff
	}
	return calibre.ParseMode(s.Value)
}

// validateCalibreConfig enforces the minimum preconditions for a usable
// integration: library_path must exist and be a directory, and the binary
// (if pinned) must be executable. These checks are cheap and run both in
// the generic settings Set path (see settings_handler.go) and in Test.
func validateCalibreConfig(cfg calibre.Config) error {
	if cfg.LibraryPath != "" {
		info, err := os.Stat(cfg.LibraryPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("library_path %q not found — ensure the path is accessible inside the Bindery container/process (check volume mounts)", cfg.LibraryPath)
			}
			return fmt.Errorf("library_path %q: %w", cfg.LibraryPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("library_path %q exists but is not a directory", cfg.LibraryPath)
		}
	}
	if cfg.BinaryPath != "" {
		info, err := os.Stat(cfg.BinaryPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("binary_path %q not found — calibredb must be accessible inside the Bindery container/process (check volume mounts, or leave blank to resolve from PATH)", cfg.BinaryPath)
			}
			return fmt.Errorf("binary_path %q: %w", cfg.BinaryPath, err)
		}
		// On Unix the executable bit lives in Mode&0o111. Windows has no
		// concept of exec bits, so we only assert file-ness there.
		if info.Mode()&0o111 == 0 && info.Mode().IsRegular() {
			return fmt.Errorf("binary_path %q exists but is not executable", cfg.BinaryPath)
		}
	}
	return nil
}

// Test probes the configured calibredb install. Returns the version on
// success so the UI can display "calibredb v7.3.0 — OK" and confirms the
// library path at the same time.
func (h *CalibreHandler) Test(w http.ResponseWriter, r *http.Request) {
	cfg := LoadCalibreConfig(h.settings)
	// Force-enable for the duration of this probe. The Test button on the
	// settings page is explicitly "does this work?", and requiring the user
	// to save calibre.enabled=true before clicking Test would be a
	// surprising extra step.
	cfg.Enabled = true
	if err := validateCalibreConfig(cfg); err != nil {
		slog.Warn("calibre test failed: config invalid", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	client := calibre.New(cfg)
	version, err := client.Test(r.Context())
	if err != nil {
		slog.Warn("calibre test failed: probe error", "binary_path", cfg.BinaryPath, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"ok":      "true",
		"version": version,
		"message": "calibredb reachable",
	})
}
