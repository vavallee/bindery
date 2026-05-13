package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/vavallee/bindery/internal/pathmap"
)

// Validate checks the configuration for known conflict patterns and invalid
// values. It runs before the HTTP server starts so operators learn about
// problems at startup rather than through unexpected runtime behavior.
//
// Warnings are logged but do not prevent the server from starting — the server
// may still be functional with the resolved effective values. Only clearly
// broken configurations (e.g. an unparseable OIDC redirect URL) cause a
// non-nil error return, which the caller should treat as fatal.
func (c *Config) Validate(logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	// --- Known precedence conflicts ---

	// Per-author root folder overrides BINDERY_AUDIOBOOK_DIR for audiobook
	// routing: when an author's RootFolderID is set, audiobooks land under
	// that folder rather than AudiobookDir. Log a prominent warning so
	// operators know to set per-author audiobook overrides when needed.
	if c.AudiobookDir != "" {
		logger.Warn("config: BINDERY_AUDIOBOOK_DIR is set — note that a per-author root folder override takes precedence for audiobook routing; audiobooks for authors with an explicit root folder will route there instead",
			"audiobookDir", c.AudiobookDir,
			"effectiveDefault", c.AudiobookDir,
		)
	}

	// When BINDERY_AUDIOBOOK_DIR is unset, audiobooks fall back to
	// BINDERY_LIBRARY_DIR. Log the resolved effective value so operators
	// know which directory is actually used.
	if c.AudiobookDir == "" {
		logger.Info("config: BINDERY_AUDIOBOOK_DIR not set — audiobooks will route to BINDERY_LIBRARY_DIR",
			"effectiveAudiobookDir", c.LibraryDir,
		)
	}

	// --- Directory existence / writability checks ---

	if err := checkDir(logger, "BINDERY_LIBRARY_DIR", c.LibraryDir); err != nil {
		// Non-fatal: the directory may be created on first import.
		logger.Warn("config: library directory does not exist or is not writable — it may be created on first import",
			"libraryDir", c.LibraryDir, "error", err)
	}

	if c.AudiobookDir != "" && c.AudiobookDir != c.LibraryDir {
		if err := checkDir(logger, "BINDERY_AUDIOBOOK_DIR", c.AudiobookDir); err != nil {
			logger.Warn("config: audiobook directory does not exist or is not writable — it may be created on first import",
				"audiobookDir", c.AudiobookDir, "error", err)
		}
	}

	// BINDERY_AUDIOBOOK_DOWNLOAD_DIR is optional; when set it must be a
	// reachable directory (the download client is expected to place audiobook
	// files there). Falls back to BINDERY_DOWNLOAD_DIR when unset.
	if c.AudiobookDownloadDir != "" {
		if err := checkDir(logger, "BINDERY_AUDIOBOOK_DOWNLOAD_DIR", c.AudiobookDownloadDir); err != nil {
			logger.Warn("config: audiobook download directory does not exist or is not readable — check BINDERY_AUDIOBOOK_DOWNLOAD_DIR",
				"audiobookDownloadDir", c.AudiobookDownloadDir, "error", err)
		}
	} else {
		logger.Info("config: BINDERY_AUDIOBOOK_DOWNLOAD_DIR not set — audiobook downloads will use BINDERY_DOWNLOAD_DIR",
			"effectiveAudiobookDownloadDir", c.DownloadDir,
		)
	}

	// --- Invalid value checks (fatal) ---

	// BINDERY_OIDC_REDIRECT_BASE_URL must be a valid absolute HTTP/HTTPS URL
	// when set, because it is concatenated with a path to build the OAuth2
	// redirect URI. A malformed value will silently produce broken OIDC flows
	// that are very hard to debug.
	if c.OIDCRedirectBaseURL != "" {
		if err := validateAbsURL("BINDERY_OIDC_REDIRECT_BASE_URL", c.OIDCRedirectBaseURL); err != nil {
			return err
		}
	}

	// BINDERY_URL_BASE must be a clean path prefix after normalisation.
	if c.URLBase != "" {
		if strings.Contains(c.URLBase, "://") {
			return fmt.Errorf("BINDERY_URL_BASE: value must be a path prefix (e.g. /bindery), not a full URL %q", c.URLBase)
		}
		logger.Info("config: BINDERY_URL_BASE set — app will be served under path prefix",
			"urlBase", c.URLBase)
	}

	// --- Download path remap sanity check (warn) ---

	if c.DownloadPathRemap != "" {
		if err := validateDownloadPathRemap(c.DownloadPathRemap); err != nil {
			logger.Warn("config: BINDERY_DOWNLOAD_PATH_REMAP has an invalid entry — path remapping may not work correctly",
				"remap", c.DownloadPathRemap, "error", err)
		}
	}

	return nil
}

// checkDir returns an error if path does not exist or the process cannot
// write to it. It tries os.MkdirTemp inside the target directory as the
// most reliable cross-platform writability probe.
func checkDir(logger *slog.Logger, envVar, path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist")
	}
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path exists but is not a directory")
	}

	// Probe writability by creating and immediately removing a temporary file.
	f, err := os.CreateTemp(path, ".bindery-write-check-*")
	if err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	_ = f.Close()
	_ = os.Remove(f.Name())

	_ = logger // logger parameter reserved for future structured context
	_ = envVar
	return nil
}

// validateAbsURL returns an error if raw is not a valid absolute HTTP or
// HTTPS URL. It is intentionally strict: a relative URL or a bare hostname
// will both be rejected because they would produce broken OAuth2 redirect
// URIs.
func validateAbsURL(envVar, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: not a valid URL %q: %w", envVar, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: URL must use http or https scheme, got %q in %q", envVar, u.Scheme, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: URL has no host: %q", envVar, raw)
	}
	return nil
}

// validateDownloadPathRemap checks that BINDERY_DOWNLOAD_PATH_REMAP is a
// comma-separated list of "from:to" pairs. It only validates the format —
// whether the paths actually resolve is left to the importer.
func validateDownloadPathRemap(raw string) error {
	return pathmap.Validate(raw)
}
