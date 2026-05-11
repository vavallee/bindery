// Package config loads Bindery's runtime configuration from environment
// variables with sensible defaults.
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Config holds the application configuration loaded from environment variables.
type Config struct {
	Port                 string
	DBPath               string
	DataDir              string
	LogLevel             string
	APIKey               string
	DownloadDir          string
	AudiobookDownloadDir string
	LibraryDir           string
	AudiobookDir         string
	// Enhanced Hardcover series API (BINDERY_ENHANCED_HARDCOVER_API, default true).
	EnhancedHardcoverAPI bool
	DownloadPathRemap    string
	// Proxy SSO settings (Phase 1).
	ProxyAuthHeader    string // BINDERY_PROXY_AUTH_HEADER
	ProxyAutoProvision bool   // BINDERY_PROXY_AUTO_PROVISION
	// OIDC settings (Phase 2).
	OIDCRedirectBaseURL string // BINDERY_OIDC_REDIRECT_BASE_URL
	// Log retention in days (BINDERY_LOG_RETENTION_DAYS, default 14).
	LogRetentionDays int
	// Login rate limit (per-IP sliding window).
	// BINDERY_RATE_LIMIT_MAX_FAILURES  (default: 5)
	// BINDERY_RATE_LIMIT_WINDOW_MINUTES (default: 15)
	RateLimitMaxFailures   int
	RateLimitWindowMinutes int
	// URLBase is an optional path prefix under which the entire app is served
	// (e.g. "/bindery"). Default "" mounts at the root. Set via
	// BINDERY_URL_BASE. Automatically normalised: leading slash added, trailing
	// slash removed, full URLs truncated to their path component.
	URLBase string
}

// Load reads configuration from environment variables with sensible defaults.
// BINDERY_AUDIOBOOK_DIR falls back to BINDERY_LIBRARY_DIR when unset so
// ebook-only installs continue to work unchanged.
// BINDERY_AUDIOBOOK_DOWNLOAD_DIR falls back to BINDERY_DOWNLOAD_DIR when
// unset so existing single-download-dir installs continue to work unchanged.
// BINDERY_DOWNLOAD_PATH_REMAP is a comma-separated list of `from:to` pairs
// applied to paths returned by the download client before bindery reads
// them, for cases where SAB and bindery run in separate containers that
// mount the same storage at different paths.
// BINDERY_API_KEY is honoured as a one-time seed for the persisted API key
// on first boot only. Once saved to the DB, rotating the key in the UI
// takes precedence and the env var becomes a no-op.
func Load() *Config {
	return &Config{
		Port:                   envOr("BINDERY_PORT", "8787"),
		DBPath:                 envOr("BINDERY_DB_PATH", defaultDBPath(runtime.GOOS, os.UserConfigDir)),
		DataDir:                envOr("BINDERY_DATA_DIR", defaultDataDir(runtime.GOOS, os.UserConfigDir)),
		LogLevel:               envOr("BINDERY_LOG_LEVEL", "info"),
		APIKey:                 envOr("BINDERY_API_KEY", ""),
		DownloadDir:            envOr("BINDERY_DOWNLOAD_DIR", "/downloads"),
		AudiobookDownloadDir:   envOr("BINDERY_AUDIOBOOK_DOWNLOAD_DIR", ""),
		LibraryDir:             envOr("BINDERY_LIBRARY_DIR", "/books"),
		AudiobookDir:           envOr("BINDERY_AUDIOBOOK_DIR", ""),
		EnhancedHardcoverAPI:   envBool("BINDERY_ENHANCED_HARDCOVER_API", true),
		DownloadPathRemap:      envOr("BINDERY_DOWNLOAD_PATH_REMAP", ""),
		ProxyAuthHeader:        envOr("BINDERY_PROXY_AUTH_HEADER", "X-Forwarded-User"),
		ProxyAutoProvision:     envBool("BINDERY_PROXY_AUTO_PROVISION", true),
		OIDCRedirectBaseURL:    envOr("BINDERY_OIDC_REDIRECT_BASE_URL", ""),
		LogRetentionDays:       envInt("BINDERY_LOG_RETENTION_DAYS", 14),
		RateLimitMaxFailures:   envInt("BINDERY_RATE_LIMIT_MAX_FAILURES", 5),
		RateLimitWindowMinutes: envInt("BINDERY_RATE_LIMIT_WINDOW_MINUTES", 15),
		URLBase:                normalizeURLBase(envOr("BINDERY_URL_BASE", "")),
	}
}

// normalizeURLBase ensures the prefix always starts with "/" and never ends
// with one. Full URLs are reduced to their path component so that copy-paste
// of a full origin URL (e.g. "https://example.com/bindery") still works.
// Returns "" for an empty or root-only value so callers can use a simple
// `if cfg.URLBase != ""` check.
func normalizeURLBase(raw string) string {
	s := strings.TrimSpace(raw)
	// Drop scheme + host if someone passed a full URL.
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			s = rest[j:]
		} else {
			s = ""
		}
	}
	s = strings.TrimRight(s, "/")
	if s == "" || s == "/" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

// defaultDBPath resolves the platform-appropriate SQLite path. Linux keeps the
// historical `/config/bindery.db` so existing Docker / Helm / bare-metal
// deployments that bind-mount `/config` are unchanged. Windows and macOS
// resolve under os.UserConfigDir so double-clicking the published binary
// works without setting env vars. Falls back to `/config/bindery.db` if
// UserConfigDir errors (vanishingly rare; cmd/bindery's db.Open preflight
// catches the resulting write failure with a clear message).
func defaultDBPath(goos string, userConfigDir func() (string, error)) string {
	if goos != "linux" {
		if d, err := userConfigDir(); err == nil {
			return filepath.Join(d, "Bindery", "bindery.db")
		}
	}
	return "/config/bindery.db"
}

// defaultDataDir mirrors defaultDBPath for BINDERY_DATA_DIR (where backups
// land). Same linux-preserving logic so the two stay in the same directory
// on every platform.
func defaultDataDir(goos string, userConfigDir func() (string, error)) string {
	if goos != "linux" {
		if d, err := userConfigDir(); err == nil {
			return filepath.Join(d, "Bindery")
		}
	}
	return "/config"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "false", "0", "no":
		return false
	case "true", "1", "yes":
		return true
	}
	return fallback
}
