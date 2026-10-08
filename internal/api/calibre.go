package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/pathmap"
)

// Calibre settings keys. Centralised so the handler and main.go agree on
// the exact names and nobody drifts to "calibre_enabled" vs "calibre.enabled".
const (
	SettingCalibreEnabled       = "calibre.enabled"
	SettingCalibreLibraryPath   = "calibre.library_path"
	SettingCalibreBinaryPath    = "calibre.binary_path"
	SettingCalibreMode          = "calibre.mode"
	SettingCalibreSyncOnStartup = "calibre.sync_on_startup"
	// SettingCalibreLibraryImportEnabled is the opt-in master switch for
	// importing a Calibre library into Bindery. Off by default; when off, no
	// startup / scheduled / manual library import runs.
	SettingCalibreLibraryImportEnabled = "calibre.library_import_enabled"
	SettingCalibrePluginURL            = "calibre.plugin_url"
	SettingCalibrePluginAPIKey         = "calibre.plugin_api_key"
	// SettingCalibrePushPathRemap translates Bindery library paths to the
	// prefix the Calibre (Bridge plugin) container sees before a push, in
	// pathmap "from:to[,from:to]" form — e.g. "/books:/mnt/user/media/books".
	// Bindery hands the Bridge the path it stores a book at and the plugin
	// opens it on ITS side; when the two containers mount the library at
	// different points every push fails with "No such file or directory"
	// (#1346, Unraid setups mostly). Empty = no translation.
	SettingCalibrePushPathRemap = "calibre.push_path_remap"
	// SettingCalibrePluginTransport is which side opens the connection in
	// plugin mode (#2833): "push" (Bindery posts to the plugin, the default)
	// or "pull" (the plugin fetches from /bridge/v1 and the push worker
	// stands down).
	SettingCalibrePluginTransport = "calibre.plugin_transport"
)

// LoadCalibreTransport returns the configured plugin transport; anything but
// "pull" reads as push. See LoadCalibreConfig for the ctx policy.
func LoadCalibreTransport(ctx context.Context, settings *db.SettingsRepo) calibre.Transport {
	s, _ := settings.Get(ctx, SettingCalibrePluginTransport)
	if s == nil {
		return calibre.TransportPush
	}
	return calibre.ParseTransport(s.Value)
}

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

	// libraryRoot is the directory Bindery stores imported books under. It
	// is the path a push hands to the Calibre side, so it is what the "can
	// you see this?" probe asks about. Empty disables the probe.
	libraryRoot string

	// ebookPaths supplies recent imported ebooks so Test can probe one real
	// book through the push remap, not just the root (#2831). nil falls back
	// to a file found under libraryRoot.
	ebookPaths recentEbookPathLister
}

// recentEbookPathLister is the slice of db.BookFileRepo the Test probe needs.
type recentEbookPathLister interface {
	RecentEbookPaths(ctx context.Context, limit int) ([]string, error)
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

// WithLibraryRoot attaches the Bindery library directory so Test can ask the
// plugin whether the Calibre process can actually see it. Without it the Test
// button can only report that the plugin answered, which is the gap behind
// every "Test says OK but nothing reaches Calibre" report (#1346, #1355).
func (h *CalibreHandler) WithLibraryRoot(dir string) *CalibreHandler {
	h.libraryRoot = strings.TrimSpace(dir)
	return h
}

// WithBookFiles attaches the source of recent ebook paths Test samples from.
// A nil lister is ignored.
func (h *CalibreHandler) WithBookFiles(l recentEbookPathLister) *CalibreHandler {
	if l != nil {
		h.ebookPaths = l
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
		Enabled:              enabled,
		LibraryPath:          get(SettingCalibreLibraryPath),
		BinaryPath:           get(SettingCalibreBinaryPath),
		LibraryImportEnabled: strings.EqualFold(get(SettingCalibreLibraryImportEnabled), "true"),
		SyncOnStartup:        strings.EqualFold(get(SettingCalibreSyncOnStartup), "true"),
		PluginURL:            get(SettingCalibrePluginURL),
		PluginAPIKey:         get(SettingCalibrePluginAPIKey),
		PushPathRemap:        get(SettingCalibrePushPathRemap),
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
		pc := calibre.NewPluginClient(cfg.PluginURL, cfg.PluginAPIKey).WithPushPathRemap(cfg.PushPathRemap)
		health, err := pc.HealthDetail(r.Context())
		version := health.Version
		if err != nil {
			slog.Warn("calibre test failed: plugin health", "plugin_url", cfg.PluginURL, "error", err)
			// Timeout against a LAN host is likely a VPN container
			// killswitch dropping LAN traffic; name it (#1474).
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": lanTimeoutHint(cfg.PluginURL, err)})
			return
		}
		if health.Degraded {
			// The bridge answers health but refuses every write. Reporting
			// this as reachable is the false green that made the fail closed
			// api_key change look like a network fault.
			slog.Warn("calibre test failed: plugin is degraded", "plugin_url", cfg.PluginURL, "reason", health.Reason)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "the plugin answered but is not serving the API: " + health.Reason,
			})
			return
		}
		// An old bridge is worth saying out loud whether or not the probe
		// passes: the fixes it lacks bite on the push, not on the probe.
		warning := calibre.BridgeUpgradeWarning(health.PluginVersion)
		res := h.probePushPaths(r.Context(), pc)
		if res.failure != "" {
			slog.Warn("calibre test failed: plugin cannot see a pushed path",
				"plugin_url", cfg.PluginURL, "library_root", h.libraryRoot, "sample", res.sample, "error", res.failure)
			body := map[string]string{"error": res.failure}
			if res.sample != "" {
				body["sample"] = res.sample
			}
			if warning != "" {
				body["warning"] = warning
			}
			writeJSON(w, http.StatusBadGateway, body)
			return
		}
		body := map[string]string{
			"ok":      "true",
			"version": version,
			"message": res.message,
		}
		if res.sample != "" {
			body["sample"] = res.sample
		}
		if warning != "" {
			body["warning"] = warning
		}
		writeJSON(w, http.StatusOK, body)
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
		body := map[string]string{"error": err.Error()}
		if calibre.IsCalibredbUnusable(err) {
			// The settings tab keys its Bridge plugin pointer off this (#1940).
			body["code"] = warningCalibredbMissing
		}
		writeJSON(w, http.StatusBadGateway, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"ok":      "true",
		"version": version,
		"message": "calibredb reachable",
	})
}

// pushProbeResult is what probePushPaths found. Exactly one of message and
// failure is set. sample is the wire path of the book that was probed, empty
// when no book was probed.
type pushProbeResult struct {
	message string
	failure string
	sample  string
}

// sampleBookCandidates bounds how many recent ebook rows Test looks through for
// one that still exists on Bindery's side.
const sampleBookCandidates = 25

// sampleDirReadBudget bounds the directory reads the fallback walk may spend
// looking for any file under the library root, so a root full of empty author
// folders cannot turn a button click into a library scan.
const sampleDirReadBudget = 50

// probePushPaths asks the plugin whether the Calibre process can open the
// directory Bindery pushes from, and then one real book under it, using the
// same remap a real push would apply. A plugin that does not advertise
// `path_probe`, or a Bindery with no library root configured, falls back to
// exactly the previous answer.
//
// The root alone is not enough (#2831). A remap whose source is the root
// matches the root exactly, so the probe skips the join that every real push
// goes through, and a remap that produces a broken book path still passes.
// Probing a real book exercises the join.
//
// The plugin's own `library` field from /v1/health is deliberately not used as
// a substitute. It reports where Calibre keeps its library, which is a
// different directory from the one Bindery pushes out of, so it answers a
// question nobody asked. It stays in use where it belongs, in the bulk sync's
// same-library check.
func (h *CalibreHandler) probePushPaths(ctx context.Context, pc *calibre.PluginClient) pushProbeResult {
	if h.libraryRoot == "" || !pc.SupportsPathProbe(ctx) {
		return pushProbeResult{message: "plugin reachable"}
	}
	probe, err := pc.ProbePath(ctx, h.libraryRoot)
	if err != nil {
		// The probe is a diagnostic, not a gate. If it cannot run, say the
		// plugin is reachable, which is the one thing that was proven.
		slog.Debug("calibre test: path probe unavailable", "error", err)
		return pushProbeResult{message: "plugin reachable"}
	}
	wire := pc.PushPath(h.libraryRoot)
	switch {
	case !probe.Exists:
		return pushProbeResult{failure: fmt.Sprintf("plugin reachable, but the Calibre container cannot see %s. Bindery pushes book paths under %s and Calibre opens them on its own side, so set a push path remap in Settings then Calibre, or mount the library at the same path in both containers.", quotePath(wire), quotePath(h.libraryRoot)) + mappedDriveHint(wire)}
	case !probe.IsDir:
		return pushProbeResult{failure: fmt.Sprintf("plugin reachable, but %s is not a directory on the Calibre side. Check the push path remap in Settings then Calibre.", quotePath(wire)) + mappedDriveHint(wire)}
	case !probe.Readable:
		return pushProbeResult{failure: fmt.Sprintf("plugin reachable, but the Calibre container cannot read %s. Check the volume permissions, and that both containers run as a user that can read the library.", quotePath(wire)) + mappedDriveHint(wire)}
	}

	local := h.pickSampleBook(ctx)
	if local == "" {
		return pushProbeResult{message: fmt.Sprintf("plugin reachable, and it can read %s. No imported book was found to test, so only the library root was checked.", wire)}
	}
	sampleWire := pc.PushPath(local)
	sp, err := pc.ProbePath(ctx, local)
	if err != nil {
		slog.Debug("calibre test: sample book probe failed", "path", sampleWire, "error", err)
		return pushProbeResult{
			message: fmt.Sprintf("plugin reachable, and it can read %s. Checking the book at %s did not complete, so only the library root was checked.", wire, quotePath(sampleWire)),
			sample:  sampleWire,
		}
	}
	switch {
	case !sp.Exists || sp.IsDir:
		return pushProbeResult{
			failure: fmt.Sprintf("plugin reachable, and Calibre can see the library root %s, but not the book at %s. The push path remap covers the root but not the book, so check the remap pair in Settings then Calibre, and any other root folders your books are stored under.", quotePath(wire), quotePath(sampleWire)) + mappedDriveHint(sampleWire),
			sample:  sampleWire,
		}
	case !sp.Readable:
		return pushProbeResult{
			failure: fmt.Sprintf("plugin reachable, and Calibre can see the book at %s but cannot read it. Check the file permissions, and that Calibre runs as a user that can read the library.", quotePath(sampleWire)) + mappedDriveHint(sampleWire),
			sample:  sampleWire,
		}
	}
	return pushProbeResult{
		message: fmt.Sprintf("plugin reachable, and it can read %s and the book at %s", wire, quotePath(sampleWire)),
		sample:  sampleWire,
	}
}

// pickSampleBook returns one book file that exists on Bindery's side: the
// newest imported ebook when there is one, otherwise the first file found in
// the first few levels under the library root. "" when neither turns one up.
func (h *CalibreHandler) pickSampleBook(ctx context.Context) string {
	if h.ebookPaths != nil {
		paths, err := h.ebookPaths.RecentEbookPaths(ctx, sampleBookCandidates)
		if err != nil {
			slog.Debug("calibre test: recent ebook lookup failed", "error", err)
		}
		for _, p := range paths {
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
	}
	budget := sampleDirReadBudget
	// Three levels covers the default naming template,
	// {Author}/{Title} ({Year})/{file}.
	return firstFileUnder(h.libraryRoot, 3, &budget)
}

// firstFileUnder walks dir depth first, files before folders, skipping hidden
// entries, and returns the first regular file within depth levels.
func firstFileUnder(dir string, depth int, budget *int) string {
	if depth <= 0 || *budget <= 0 {
		return ""
	}
	*budget--
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type().IsRegular() {
			return filepath.Join(dir, e.Name())
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
			continue
		}
		if found := firstFileUnder(filepath.Join(dir, e.Name()), depth-1, budget); found != "" {
			return found
		}
	}
	return ""
}

// quotePath wraps a path in plain double quotes. %q would escape every
// backslash, so a share path would read as \\\\nas\\share in the message.
func quotePath(p string) string {
	return `"` + p + `"`
}

// mappedDriveHint explains the mapped drive trap when the path Calibre was
// asked to open starts with a drive letter: a drive mapped in one Windows
// logon session is invisible to a Calibre running in another, such as a
// service or a different user. Empty for any other path.
func mappedDriveHint(wire string) string {
	if !pathmap.IsWindowsPath(wire) {
		return ""
	}
	return fmt.Sprintf(` %s is a drive letter. A mapped drive belongs to one Windows logon session, so the running Calibre may not see it. Use the share address in the remap instead, like \\nas\share\books.`, strings.TrimSpace(wire)[:2])
}
