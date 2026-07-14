package api

import (
	"context"
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
	SettingCalibrePluginURL     = "calibre.plugin_url"
	SettingCalibrePluginAPIKey  = "calibre.plugin_api_key"
	// SettingCalibrePushPathRemap translates Bindery library paths to the
	// prefix the Calibre (Bridge plugin) container sees before a push, in
	// pathmap "from:to[,from:to]" form — e.g. "/books:/mnt/user/media/books".
	// Bindery hands the Bridge the path it stores a book at and the plugin
	// opens it on ITS side; when the two containers mount the library at
	// different points every push fails with "No such file or directory"
	// (#1346, Unraid setups mostly). Empty = no translation.
	SettingCalibrePushPathRemap = "calibre.push_path_remap"
)

// SettingCWAIngestPath is the directory bindery copies finished ebook
// imports into so a sibling Calibre-Web-Automated container can pick them
// up via its own auto-ingest watcher. Empty disables the integration.
// CWA reference: https://github.com/crocodilestick/Calibre-Web-Automated
const SettingCWAIngestPath = "cwa.ingest_path"

// CalibreHandler exposes the "test connection" endpoint for the Calibre
// settings UI. Read/write of the calibre.* keys themselves go through the
// generic /setting endpoints so the UI can reuse its existing plumbing;
// this handler just validates and probes.
type CalibreHandler struct {
	settings *db.SettingsRepo

	// lifetimeCtx is the process-lifecycle context. The settings reads in
	// LoadCalibreConfig/LoadCalibreMode are short-lived but must observe
	// shutdown when called from scheduler closures so a server-stop does
	// not block on SQLite. Falls back to context.Background() when not
	// set; see #846 and recommendations.go.
	lifetimeCtx context.Context
}

func NewCalibreHandler(settings *db.SettingsRepo) *CalibreHandler {
	return &CalibreHandler{settings: settings}
}

// WithLifetimeCtx attaches the process-lifecycle context. A nil ctx is
// tolerated and ignored. See #846.
func (h *CalibreHandler) WithLifetimeCtx(ctx context.Context) *CalibreHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
func (h *CalibreHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

// LoadCalibreConfig materialises a calibre.Config from the settings table.
// Exported so main.go can build the importer's Calibre client at boot and
// refresh it on each scheduler tick. ctx is the read-lifetime: pass
// the process-lifecycle context from scheduler closures so a shutdown
// cancels in-flight reads; pass r.Context() from handlers; pass
// context.Background() at boot when no other context exists. See #846.
func LoadCalibreConfig(ctx context.Context, settings *db.SettingsRepo) calibre.Config {
	get := func(key string) string {
		s, _ := settings.Get(ctx, key)
		if s == nil {
			return ""
		}
		return s.Value
	}
	mode := LoadCalibreMode(ctx, settings)
	enabled := mode == calibre.ModeCalibredb || mode == calibre.ModePlugin
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
		PluginURL:     get(SettingCalibrePluginURL),
		PluginAPIKey:  get(SettingCalibrePluginAPIKey),
		PushPathRemap: get(SettingCalibrePushPathRemap),
	}
}

// LoadCalibreMode returns the currently-configured integration mode. The
// scanner calls this on every import so toggling the radio in Settings
// takes effect without a restart. ctx scopes the underlying settings read;
// see LoadCalibreConfig for the call-site policy.
func LoadCalibreMode(ctx context.Context, settings *db.SettingsRepo) calibre.Mode {
	s, _ := settings.Get(ctx, SettingCalibreMode)
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
	cfg := LoadCalibreConfig(r.Context(), h.settings)
	// Force-enable for the duration of this probe. The Test button on the
	// settings page is explicitly "does this work?", and requiring the user
	// to save calibre.enabled=true before clicking Test would be a
	// surprising extra step.
	cfg.Enabled = true

	if LoadCalibreMode(r.Context(), h.settings) == calibre.ModePlugin {
		if cfg.PluginURL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plugin_url is not configured"})
			return
		}
		pc := calibre.NewPluginClient(cfg.PluginURL, cfg.PluginAPIKey)
		version, err := pc.Health(r.Context())
		if err != nil {
			slog.Warn("calibre test failed: plugin health", "plugin_url", cfg.PluginURL, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"ok":      "true",
			"version": version,
			"message": "plugin reachable",
		})
		return
	}

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
