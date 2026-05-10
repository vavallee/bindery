package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// SettingDefaultMediaType is the KV key for the global media-type default
// applied to authors created without an explicit mediaType. Value is one of
// models.MediaTypeEbook / MediaTypeAudiobook / MediaTypeBoth; empty or
// unset falls back to ebook for backwards compatibility.
const SettingDefaultMediaType = "default.media_type"

// SettingDefaultLibraryRootFolderID is the KV key that stores the ID of the
// root folder used as the fallback library path when an author has no
// per-author RootFolderID. Value is a decimal integer (the root_folder.id);
// empty or unset means fall back to cfg.LibraryDir (the env-var default).
const SettingDefaultLibraryRootFolderID = "library.defaultRootFolderId"

// SettingMetadataPrimaryProvider is the KV key that selects the primary
// metadata provider used for author/book search and lookup. Valid values are
// "openlibrary" (default) and "dnb". Empty or unset falls back to
// "openlibrary" for backwards compatibility.
const SettingMetadataPrimaryProvider = "metadata.primary_provider"

type SettingsHandler struct {
	settings *db.SettingsRepo
}

func NewSettingsHandler(settings *db.SettingsRepo) *SettingsHandler {
	return &SettingsHandler{settings: settings}
}

// isSecretSetting reports whether a settings key holds sensitive material
// that must not leak through the generic settings endpoints. The auth.*
// values are surfaced through the dedicated /auth/* endpoints instead.
func isSecretSetting(key string) bool {
	switch key {
	case "auth.api_key", "auth.session_secret", "auth.mode", SettingABSAPIKey, SettingHardcoverAPIToken:
		return true
	}
	return false
}

func isWritableSecretSetting(key string) bool {
	return key == SettingHardcoverAPIToken
}

func (h *SettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.settings.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	filtered := make([]models.Setting, 0, len(settings))
	for _, s := range settings {
		if !isSecretSetting(s.Key) {
			filtered = append(filtered, s)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if isSecretSetting(key) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "setting not found"})
		return
	}
	s, err := h.settings.Get(r.Context(), key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "setting not found"})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *SettingsHandler) Set(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if isSecretSetting(key) && !isWritableSecretSetting(key) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "use /auth/* endpoints for auth settings"})
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	value := normalizeSettingValue(key, req.Value)
	if err := validateSettingValue(key, value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.settings.Set(r.Context(), key, value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if isSecretSetting(key) {
		writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": ""})
		return
	}
	s, err := h.settings.Get(r.Context(), key)
	if err != nil || s == nil {
		writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func normalizeSettingValue(key, value string) string {
	if key == SettingHardcoverAPIToken {
		return hardcover.NormalizeAPIToken(value)
	}
	return value
}

type HardcoverTestResponse struct {
	OK               bool   `json:"ok"`
	TokenConfigured  bool   `json:"tokenConfigured"`
	SearchResults    int    `json:"searchResults"`
	SampleSeriesID   string `json:"sampleSeriesId,omitempty"`
	SampleTitle      string `json:"sampleTitle,omitempty"`
	CatalogOK        bool   `json:"catalogOk"`
	CatalogBookCount int    `json:"catalogBookCount,omitempty"`
	Message          string `json:"message,omitempty"`
	Error            string `json:"error,omitempty"`
}

func (h *SettingsHandler) TestHardcover(w http.ResponseWriter, r *http.Request) {
	token := GetHardcoverAPIToken(r.Context(), h.settings)
	result := HardcoverTestResponse{TokenConfigured: token != ""}
	if token == "" {
		result.Error = "hardcover API token is not configured"
		writeJSON(w, http.StatusOK, result)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	client := hardcover.New().WithToken(token)
	series, err := client.SearchSeries(ctx, "Dune", 3)
	if err != nil {
		result.Error = err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.SearchResults = len(series)
	if len(series) == 0 {
		result.Error = "Hardcover search returned no series for Dune"
		writeJSON(w, http.StatusOK, result)
		return
	}

	result.SampleSeriesID = series[0].ForeignID
	result.SampleTitle = series[0].Title
	catalog, err := client.GetSeriesCatalog(ctx, series[0].ForeignID)
	if err != nil {
		result.Error = err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	if catalog == nil {
		result.Error = "Hardcover catalog lookup returned no series"
		writeJSON(w, http.StatusOK, result)
		return
	}

	result.CatalogOK = true
	result.CatalogBookCount = len(catalog.Books)
	result.OK = true
	result.Message = fmt.Sprintf("Found %d series; catalog %q has %d books", result.SearchResults, catalog.Title, len(catalog.Books))
	writeJSON(w, http.StatusOK, result)
}

// validateSettingValue enforces per-key invariants on writes. We run this
// inline with Set (rather than a separate middleware) because the settings
// endpoint is the single place every non-auth key flows through and the
// validations are both few and cheap. Keys not listed here pass through
// unchanged — the settings table stays schema-less for anything else.
func validateSettingValue(key, value string) error {
	switch key {
	case SettingHardcoverAPIToken:
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("hardcover.api_token contains invalid control characters")
			}
		}
	case SettingHardcoverEnhancedSeriesEnabled:
		if value == "" {
			return nil
		}
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			return fmt.Errorf("hardcover.enhanced_series_enabled %q is not one of: true, false", value)
		}
	case SettingCalibreLibraryPath:
		// Empty = disabled / unset; reject only when the caller provided
		// a non-empty string that doesn't resolve to an existing dir.
		if value == "" {
			return nil
		}
		info, err := os.Stat(value)
		if err != nil {
			return fmt.Errorf("library_path %q: %w", value, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("library_path %q is not a directory", value)
		}
	case SettingCalibreBinaryPath:
		if value == "" {
			return nil
		}
		info, err := os.Stat(value)
		if err != nil {
			return fmt.Errorf("binary_path %q: %w", value, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("binary_path %q is not a regular file", value)
		}
		if info.Mode()&0o111 == 0 {
			return fmt.Errorf("binary_path %q is not executable", value)
		}
	case SettingCWAIngestPath:
		// Empty = disabled. Non-empty must resolve to an existing writable
		// directory so a typo in the UI fails loudly here, not silently at
		// import time when the post-import push tries to copy.
		if value == "" {
			return nil
		}
		info, err := os.Stat(value)
		if err != nil {
			return fmt.Errorf("cwa.ingest_path %q: %w (ensure the path is accessible inside the bindery container, check volume mounts)", value, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("cwa.ingest_path %q is not a directory", value)
		}
	case SettingCalibreMode:
		// Canonical values only. An empty string falls through to the
		// default (off) handled by LoadCalibreMode; anything else must
		// parse to a known mode so a typo in the UI cannot silently
		// disable the integration.
		if value == "" {
			return nil
		}
		if !calibre.Mode(value).Valid() {
			return fmt.Errorf("calibre.mode %q is not one of: off, calibredb, plugin", value)
		}
	case SettingDefaultMediaType:
		// Empty falls back to ebook at read time; only validate non-empty
		// writes so a typo in the UI can't silently disable the default.
		if value == "" {
			return nil
		}
		switch value {
		case models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
			return nil
		default:
			return fmt.Errorf("default.media_type %q is not one of: ebook, audiobook, both", value)
		}
	case SettingDefaultLibraryRootFolderID:
		// Empty = unset (fall back to env-var default); non-empty must be a
		// positive integer representing an existing root_folder.id.
		if value == "" {
			return nil
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("library.defaultRootFolderId %q must be a positive integer or empty", value)
		}
	case SettingMetadataPrimaryProvider:
		// Empty falls back to "openlibrary"; non-empty must be a known provider.
		if value == "" {
			return nil
		}
		switch value {
		case "openlibrary", "dnb":
			return nil
		default:
			return fmt.Errorf("metadata.primary_provider %q is not one of: openlibrary, dnb", value)
		}
	case SettingCalibrePluginURL:
		if value == "" {
			return nil
		}
		u, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("plugin_url %q: %w", value, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("plugin_url %q must use http or https scheme", value)
		}
		if u.Host == "" {
			return fmt.Errorf("plugin_url %q is missing a host", value)
		}
	case SettingABSBaseURL:
		if value == "" {
			return nil
		}
		if _, err := abs.NormalizeBaseURL(value); err != nil {
			return err
		}
	case SettingABSEnabled:
		if value == "" {
			return nil
		}
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			return fmt.Errorf("abs.enabled %q is not one of: true, false", value)
		}
	}
	return nil
}

func (h *SettingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if isSecretSetting(key) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "use /auth/* endpoints for auth settings"})
		return
	}
	if err := h.settings.Delete(r.Context(), key); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
