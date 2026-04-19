// Package config loads Bindery's runtime configuration from environment
// variables with sensible defaults.
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config holds the application configuration loaded from environment variables.
type Config struct {
	Port              string
	DBPath            string
	DataDir           string
	LogLevel          string
	APIKey            string
	DownloadDir       string
	LibraryDir        string
	AudiobookDir      string
	DownloadPathRemap string
	// Proxy SSO settings (Phase 1).
	ProxyAuthHeader    string // BINDERY_PROXY_AUTH_HEADER
	ProxyAutoProvision bool   // BINDERY_PROXY_AUTO_PROVISION
	// OIDC settings (Phase 2).
	OIDCRedirectBaseURL string // BINDERY_OIDC_REDIRECT_BASE_URL
}

// Load reads configuration from environment variables with sensible defaults.
// BINDERY_AUDIOBOOK_DIR falls back to BINDERY_LIBRARY_DIR when unset so
// ebook-only installs continue to work unchanged.
// BINDERY_DOWNLOAD_PATH_REMAP is a comma-separated list of `from:to` pairs
// applied to paths returned by the download client before bindery reads
// them, for cases where SAB and bindery run in separate containers that
// mount the same storage at different paths.
// BINDERY_API_KEY is honoured as a one-time seed for the persisted API key
// on first boot only. Once saved to the DB, rotating the key in the UI
// takes precedence and the env var becomes a no-op.
func Load() *Config {
	return &Config{
		Port:                envOr("BINDERY_PORT", "8787"),
		DBPath:              envOr("BINDERY_DB_PATH", defaultDBPath(runtime.GOOS, os.UserConfigDir)),
		DataDir:             envOr("BINDERY_DATA_DIR", defaultDataDir(runtime.GOOS, os.UserConfigDir)),
		LogLevel:            envOr("BINDERY_LOG_LEVEL", "info"),
		APIKey:              envOr("BINDERY_API_KEY", ""),
		DownloadDir:         envOr("BINDERY_DOWNLOAD_DIR", "/downloads"),
		LibraryDir:          envOr("BINDERY_LIBRARY_DIR", "/books"),
		AudiobookDir:        envOr("BINDERY_AUDIOBOOK_DIR", ""),
		DownloadPathRemap:   envOr("BINDERY_DOWNLOAD_PATH_REMAP", ""),
		ProxyAuthHeader:     envOr("BINDERY_PROXY_AUTH_HEADER", "X-Forwarded-User"),
		ProxyAutoProvision:  envBool("BINDERY_PROXY_AUTO_PROVISION", true),
		OIDCRedirectBaseURL: envOr("BINDERY_OIDC_REDIRECT_BASE_URL", ""),
	}
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
