package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/grimmory"
)

const (
	SettingGrimmoryEnabled = "grimmory.enabled"
	SettingGrimmoryBaseURL = "grimmory.base_url"
	SettingGrimmoryAPIKey  = "grimmory.api_key" //nolint:gosec // #nosec G101 -- settings key name, not a credential value
)

// GrimmoryStoredConfig is the flat settings representation used by LoadGrimmoryConfig.
type GrimmoryStoredConfig struct {
	Enabled bool
	BaseURL string
	APIKey  string
}

// GrimmoryConfigResponse is the JSON response for GET /grimmory/config.
type GrimmoryConfigResponse struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"baseUrl"`
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
	ServerVersion    string `json:"serverVersion,omitempty"`
}

type grimmoryConfigRequest struct {
	Enabled *bool   `json:"enabled"`
	BaseURL *string `json:"baseUrl"`
	APIKey  *string `json:"apiKey"`
}

type grimmoryTestResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Version string `json:"version,omitempty"`
}

type grimmoryClientFactory func(baseURL, apiKey string) (*grimmory.Client, error)

// GrimmoryHandler handles /api/grimmory/* endpoints.
type GrimmoryHandler struct {
	settings  *db.SettingsRepo
	newFn     grimmoryClientFactory
	userAgent string
}

// NewGrimmoryHandler returns a handler backed by the given settings repo.
func NewGrimmoryHandler(settings *db.SettingsRepo) *GrimmoryHandler {
	h := &GrimmoryHandler{
		settings:  settings,
		userAgent: grimmory.UserAgent(""),
	}
	h.newFn = func(baseURL, apiKey string) (*grimmory.Client, error) {
		c, err := grimmory.NewClient(baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		return c.WithUserAgent(h.userAgent), nil
	}
	return h
}

// WithVersion sets the Bindery version in the User-Agent.
func (h *GrimmoryHandler) WithVersion(version string) *GrimmoryHandler {
	h.userAgent = grimmory.UserAgent(version)
	return h
}

// GetConfig returns the current Grimmory configuration (api key is redacted).
func (h *GrimmoryHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := LoadGrimmoryConfig(h.settings)
	writeJSON(w, http.StatusOK, GrimmoryConfigResponse{
		Enabled:          cfg.Enabled,
		BaseURL:          cfg.BaseURL,
		APIKeyConfigured: cfg.APIKey != "",
	})
}

// SetConfig saves Grimmory connection settings.
func (h *GrimmoryHandler) SetConfig(w http.ResponseWriter, r *http.Request) {
	var req grimmoryConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	ctx := r.Context()
	set := func(key, val string) {
		_ = h.settings.Set(ctx, key, val)
	}

	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		set(SettingGrimmoryEnabled, val)
	}
	if req.BaseURL != nil {
		u, err := grimmory.NormalizeBaseURL(*req.BaseURL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		set(SettingGrimmoryBaseURL, u)
	}
	if req.APIKey != nil {
		k, err := grimmory.NormalizeAPIKey(*req.APIKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if k != "" {
			set(SettingGrimmoryAPIKey, k)
		}
	}

	cfg := LoadGrimmoryConfig(h.settings)
	writeJSON(w, http.StatusOK, GrimmoryConfigResponse{
		Enabled:          cfg.Enabled,
		BaseURL:          cfg.BaseURL,
		APIKeyConfigured: cfg.APIKey != "",
	})
}

// Test probes the configured Grimmory server and returns version info.
func (h *GrimmoryHandler) Test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	cfg := LoadGrimmoryConfig(h.settings)
	baseURL := req.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = cfg.APIKey
	}

	client, err := h.newFn(baseURL, apiKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, grimmoryTestResponse{OK: false, Message: err.Error()})
		return
	}

	status, err := client.Ping(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, grimmoryTestResponse{OK: false, Message: err.Error()})
		return
	}

	msg := "Connected to Grimmory"
	if status.Version != "" {
		msg = "Connected to Grimmory " + status.Version
	}
	writeJSON(w, http.StatusOK, grimmoryTestResponse{OK: true, Message: msg, Version: status.Version})
}

// LoadGrimmoryConfig materialises a GrimmoryStoredConfig from the settings table.
func LoadGrimmoryConfig(settings *db.SettingsRepo) GrimmoryStoredConfig {
	ctx := context.Background()
	get := func(key string) string {
		s, _ := settings.Get(ctx, key)
		if s == nil {
			return ""
		}
		return s.Value
	}
	return GrimmoryStoredConfig{
		Enabled: strings.EqualFold(get(SettingGrimmoryEnabled), "true"),
		BaseURL: get(SettingGrimmoryBaseURL),
		APIKey:  get(SettingGrimmoryAPIKey),
	}
}
