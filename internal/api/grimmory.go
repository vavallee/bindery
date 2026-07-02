package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/grimmory"
)

const (
	SettingGrimmoryEnabled  = "grimmory.enabled"
	SettingGrimmoryBaseURL  = "grimmory.base_url"
	SettingGrimmoryAPIKey   = "grimmory.api_key" //nolint:gosec // #nosec G101 -- settings key name, not a credential value
	SettingGrimmoryUsername = "grimmory.username"
	SettingGrimmoryPassword = "grimmory.password" //nolint:gosec // #nosec G101 -- settings key name, not a credential value
)

// GrimmoryStoredConfig is the flat settings representation used by LoadGrimmoryConfig.
type GrimmoryStoredConfig struct {
	Enabled  bool
	BaseURL  string
	APIKey   string
	Username string
	Password string
}

// PushConfig converts the stored settings into the grimmory package's live
// push configuration.
func (c GrimmoryStoredConfig) PushConfig() grimmory.PushConfig {
	return grimmory.PushConfig{
		Enabled:  c.Enabled,
		BaseURL:  c.BaseURL,
		APIKey:   c.APIKey,
		Username: c.Username,
		Password: c.Password,
	}
}

// GrimmoryConfigResponse is the JSON response for GET /grimmory/config.
// The username is echoed (it is not a secret); the API key and password are
// redacted to configured/not-configured booleans.
type GrimmoryConfigResponse struct {
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"baseUrl"`
	APIKeyConfigured   bool   `json:"apiKeyConfigured"`
	Username           string `json:"username"`
	PasswordConfigured bool   `json:"passwordConfigured"`
	ServerVersion      string `json:"serverVersion,omitempty"`
}

type grimmoryConfigRequest struct {
	Enabled  *bool   `json:"enabled"`
	BaseURL  *string `json:"baseUrl"`
	APIKey   *string `json:"apiKey"`
	Username *string `json:"username"`
	Password *string `json:"password"`
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
	writeJSON(w, http.StatusOK, grimmoryConfigResponse(cfg))
}

func grimmoryConfigResponse(cfg GrimmoryStoredConfig) GrimmoryConfigResponse {
	return GrimmoryConfigResponse{
		Enabled:            cfg.Enabled,
		BaseURL:            cfg.BaseURL,
		APIKeyConfigured:   cfg.APIKey != "",
		Username:           cfg.Username,
		PasswordConfigured: cfg.Password != "",
	}
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
		if err := h.settings.Set(ctx, key, val); err != nil {
			// The handler returns the re-read config either way, so a failed
			// write is otherwise invisible: the user saves and nothing persists.
			slog.Warn("grimmory: failed to persist setting", "key", key, "error", err)
		}
	}

	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		set(SettingGrimmoryEnabled, val)
	}
	if req.BaseURL != nil {
		u, err := grimmory.ValidateBaseURLSecure(*req.BaseURL)
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
	if req.Username != nil {
		set(SettingGrimmoryUsername, strings.TrimSpace(*req.Username))
	}
	// Password is write-only: a non-empty value replaces the stored one, an
	// empty value is "leave unchanged" (matching the API-key semantics above,
	// so the UI can round-trip the form without wiping credentials).
	if req.Password != nil && *req.Password != "" {
		set(SettingGrimmoryPassword, *req.Password)
	}

	cfg := LoadGrimmoryConfig(h.settings)
	writeJSON(w, http.StatusOK, grimmoryConfigResponse(cfg))
}

// Test probes the configured Grimmory server and returns version info. When
// credentials are configured (or supplied in the request), it also performs a
// real login so "Test" proves the push pipeline can authenticate, not just
// that the host answers (#826).
func (h *GrimmoryHandler) Test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL  string `json:"baseUrl"`
		APIKey   string `json:"apiKey"`
		Username string `json:"username"`
		Password string `json:"password"`
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
	username := req.Username
	if username == "" {
		username = cfg.Username
	}
	password := req.Password
	if password == "" {
		password = cfg.Password
	}

	client, err := h.newFn(baseURL, apiKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, grimmoryTestResponse{OK: false, Message: err.Error()})
		return
	}
	client.WithCredentials(username, password)

	status, err := client.Ping(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, grimmoryTestResponse{OK: false, Message: err.Error()})
		return
	}

	msg := "Connected to Grimmory"
	if status.Version != "" {
		msg = "Connected to Grimmory " + status.Version
	}
	if client.HasCredentials() {
		if err := client.VerifyAuth(r.Context()); err != nil {
			writeJSON(w, http.StatusBadGateway, grimmoryTestResponse{
				OK: false, Message: msg + ", but login failed: " + err.Error(), Version: status.Version,
			})
			return
		}
		msg += ", login OK"
	} else {
		msg += " (no credentials configured — pushes will fail until a username/password is set)"
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
		Enabled:  strings.EqualFold(get(SettingGrimmoryEnabled), "true"),
		BaseURL:  get(SettingGrimmoryBaseURL),
		APIKey:   get(SettingGrimmoryAPIKey),
		Username: get(SettingGrimmoryUsername),
		Password: get(SettingGrimmoryPassword),
	}
}
