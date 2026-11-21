package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/grimmory"
	"github.com/vavallee/bindery/internal/httpsec"
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
	// CredentialsCleared names the saved credentials ("apiKey", "password")
	// the save dropped because the base URL moved. Never their values.
	CredentialsCleared []string `json:"credentialsCleared,omitempty"`
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

	// Every field is validated before anything is written, and the writes land
	// in one transaction, so a rejected request saves nothing and a failed
	// write cannot leave a new URL next to the old credentials.
	ctx := r.Context()
	stored := LoadGrimmoryConfig(h.settings)
	var kvs []db.SettingKV

	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryEnabled, Value: val})
	}

	newKey := ""
	if req.APIKey != nil {
		k, err := httpsec.NormalizeAPIKey(*req.APIKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newKey = k
	}
	// Password is write-only: a non-empty value replaces the stored one, an
	// empty value is "leave unchanged" (matching the API-key semantics, so the
	// UI can round-trip the form without wiping credentials).
	newPassword := ""
	if req.Password != nil {
		newPassword = *req.Password
	}

	var cleared []string
	if req.BaseURL != nil {
		u, err := grimmory.ValidateBaseURLSecure(*req.BaseURL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryBaseURL, Value: u})
		// Stored credentials belong to the server they were issued for. When
		// the URL moves and no replacement came with it, drop them rather
		// than send them to the new address.
		if !sameServiceURL(u, stored.BaseURL) {
			if newKey == "" && stored.APIKey != "" {
				kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryAPIKey, Value: ""})
				cleared = append(cleared, "apiKey")
			}
			if newPassword == "" && stored.Password != "" {
				kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryPassword, Value: ""})
				cleared = append(cleared, "password")
			}
		}
	}
	if newKey != "" {
		kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryAPIKey, Value: newKey})
	}
	if req.Username != nil {
		kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryUsername, Value: strings.TrimSpace(*req.Username)})
	}
	if newPassword != "" {
		kvs = append(kvs, db.SettingKV{Key: SettingGrimmoryPassword, Value: newPassword})
	}

	if len(kvs) > 0 {
		if err := h.settings.SetMany(ctx, kvs); err != nil {
			slog.Warn("grimmory: failed to persist settings", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save Grimmory settings"})
			return
		}
	}
	if len(cleared) > 0 {
		slog.Info("grimmory: cleared saved credentials because the base URL changed", "cleared", cleared)
	}

	resp := grimmoryConfigResponse(LoadGrimmoryConfig(h.settings))
	resp.CredentialsCleared = cleared
	writeJSON(w, http.StatusOK, resp)
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
	// Saved credentials are only offered to the saved server. A probe aimed at
	// a different address has to carry its own.
	sameServer := sameServiceURL(baseURL, cfg.BaseURL)
	apiKey := req.APIKey
	if apiKey == "" && sameServer {
		apiKey = cfg.APIKey
	}
	username := req.Username
	if username == "" && sameServer {
		username = cfg.Username
	}
	password := req.Password
	if password == "" && sameServer {
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
		slog.Warn("grimmory: test connection failed", "baseURL", baseURL, "error", err)
		writeJSON(w, http.StatusBadGateway, grimmoryTestResponse{OK: false, Message: err.Error()})
		return
	}

	msg := "Connected to Grimmory"
	if status.Version != "" {
		msg = "Connected to Grimmory " + status.Version
	}
	if client.HasCredentials() {
		if err := client.VerifyAuth(r.Context()); err != nil {
			slog.Warn("grimmory: test login failed", "baseURL", baseURL, "error", err)
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
