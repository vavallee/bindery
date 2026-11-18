package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/audiblesync"
	"github.com/vavallee/bindery/internal/db"
)

// Audible Sync is an opt in, self-hosted service that serves the wishlist and
// library of the operator's own Audible account (internal/audiblesync). These
// keys configure Bindery's connection to it.
const (
	SettingAudibleSyncEnabled   = "audiblesync.enabled"
	SettingAudibleSyncBaseURL   = "audiblesync.base_url"
	SettingAudibleSyncAPIToken  = "audiblesync.api_token" //nolint:gosec // #nosec G101 -- settings key name, not a credential value
	SettingAudibleSyncAccountID = "audiblesync.account_id"
	// SettingAudibleSyncSyncInterval is how often the wishlist import lists
	// sync. Read at startup.
	SettingAudibleSyncSyncInterval = "audiblesync.sync_interval"
)

// AudibleSyncStoredConfig is the Audible Sync configuration as stored.
type AudibleSyncStoredConfig struct {
	Enabled   bool
	BaseURL   string
	APIToken  string
	AccountID int
}

// Usable reports whether the configuration is complete enough to call
// Audible Sync: enabled, with a URL and a token.
func (c AudibleSyncStoredConfig) Usable() bool {
	return c.Enabled && c.BaseURL != "" && c.APIToken != ""
}

// Client builds a client for the stored configuration.
func (c AudibleSyncStoredConfig) Client() (*audiblesync.Client, error) {
	return audiblesync.NewClient(c.BaseURL, c.APIToken, c.AccountID)
}

// LoadAudibleSyncConfig reads the Audible Sync configuration from settings.
func LoadAudibleSyncConfig(ctx context.Context, settings *db.SettingsRepo) AudibleSyncStoredConfig {
	get := func(key string) string {
		s, _ := settings.Get(ctx, key)
		if s == nil {
			return ""
		}
		return strings.TrimSpace(s.Value)
	}
	account, _ := strconv.Atoi(get(SettingAudibleSyncAccountID))
	return AudibleSyncStoredConfig{
		Enabled:   strings.EqualFold(get(SettingAudibleSyncEnabled), "true"),
		BaseURL:   get(SettingAudibleSyncBaseURL),
		APIToken:  get(SettingAudibleSyncAPIToken),
		AccountID: max(account, 0),
	}
}

// AudibleSyncConfigResponse is GET /audiblesync/config. The token is never
// returned, only whether one is stored.
type AudibleSyncConfigResponse struct {
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"baseUrl"`
	APITokenConfigured bool   `json:"apiTokenConfigured"`
	AccountID          int    `json:"accountId"`
}

type audibleSyncConfigRequest struct {
	Enabled   *bool   `json:"enabled"`
	BaseURL   *string `json:"baseUrl"`
	APIToken  *string `json:"apiToken"`
	AccountID *int    `json:"accountId"`
}

type audibleSyncTestResponse struct {
	OK       bool                 `json:"ok"`
	Message  string               `json:"message"`
	Version  string               `json:"version,omitempty"`
	Accounts []audibleSyncAccount `json:"accounts,omitempty"`
}

type audibleSyncAccount struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	CountryCode string `json:"countryCode"`
	Enabled     bool   `json:"enabled"`
	NeedsLogin  bool   `json:"needsLogin"`
}

// audibleSyncProber is what Test and Accounts ask of a client.
type audibleSyncProber interface {
	Health(ctx context.Context) (*audiblesync.Health, error)
	Accounts(ctx context.Context) ([]audiblesync.Account, error)
}

type audibleSyncClientFactory func(baseURL, token string) (audibleSyncProber, error)

// AudibleSyncHandler serves /audiblesync/*, all admin only.
type AudibleSyncHandler struct {
	settings *db.SettingsRepo
	newFn    audibleSyncClientFactory
}

// NewAudibleSyncHandler returns a handler backed by settings.
func NewAudibleSyncHandler(settings *db.SettingsRepo) *AudibleSyncHandler {
	return &AudibleSyncHandler{
		settings: settings,
		newFn: func(baseURL, token string) (audibleSyncProber, error) {
			return audiblesync.NewClient(baseURL, token, 0)
		},
	}
}

// GetConfig returns the stored configuration with the token redacted.
func (h *AudibleSyncHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, audibleSyncConfigResponse(LoadAudibleSyncConfig(r.Context(), h.settings)))
}

func audibleSyncConfigResponse(cfg AudibleSyncStoredConfig) AudibleSyncConfigResponse {
	return AudibleSyncConfigResponse{
		Enabled:            cfg.Enabled,
		BaseURL:            cfg.BaseURL,
		APITokenConfigured: cfg.APIToken != "",
		AccountID:          cfg.AccountID,
	}
}

// SetConfig saves the fields the request carries; absent fields are left as
// they are. An empty token leaves the stored one in place, so the form can be
// saved without re-entering it. Every value is validated before anything is
// written, and the writes land together.
func (h *AudibleSyncHandler) SetConfig(w http.ResponseWriter, r *http.Request) {
	var req audibleSyncConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	boolStr := func(b bool) string { return strconv.FormatBool(b) }

	var kvs []db.SettingKV
	if req.Enabled != nil {
		kvs = append(kvs, db.SettingKV{Key: SettingAudibleSyncEnabled, Value: boolStr(*req.Enabled)})
	}
	if req.BaseURL != nil {
		value := ""
		if strings.TrimSpace(*req.BaseURL) != "" {
			u, err := audiblesync.ValidateBaseURLSecure(*req.BaseURL)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			value = u
		}
		kvs = append(kvs, db.SettingKV{Key: SettingAudibleSyncBaseURL, Value: value})
	}
	if req.APIToken != nil {
		tok, err := audiblesync.NormalizeAPIToken(*req.APIToken)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if tok != "" {
			kvs = append(kvs, db.SettingKV{Key: SettingAudibleSyncAPIToken, Value: tok})
		}
	}
	if req.AccountID != nil {
		if *req.AccountID < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "accountId must be zero (Audible Sync's default account) or an account id"})
			return
		}
		value := ""
		if *req.AccountID > 0 {
			value = strconv.Itoa(*req.AccountID)
		}
		kvs = append(kvs, db.SettingKV{Key: SettingAudibleSyncAccountID, Value: value})
	}

	ctx := r.Context()
	if len(kvs) > 0 {
		if err := h.settings.SetMany(ctx, kvs); err != nil {
			writeServerError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, audibleSyncConfigResponse(LoadAudibleSyncConfig(ctx, h.settings)))
}

// Test probes the saved Audible Sync URL. A token in the body is tried in
// place of the stored one, so a token can be checked before it is saved;
// the URL is never taken from the body, so this endpoint cannot be used to
// probe arbitrary addresses (the saved URL passed ValidateBaseURLSecure).
//
// Success means both that the URL reaches Audible Sync (the open health
// check) and that the token is accepted (the account list, which needs it).
func (h *AudibleSyncHandler) Test(w http.ResponseWriter, r *http.Request) {
	client, cfg, err := h.clientFromProbe(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, audibleSyncTestResponse{OK: false, Message: err.Error()})
		return
	}
	ctx := r.Context()
	health, err := client.Health(ctx)
	if err != nil {
		slog.Warn("Audible Sync: test connection failed", "baseURL", cfg.BaseURL, "error", err)
		writeJSON(w, http.StatusBadGateway, audibleSyncTestResponse{OK: false, Message: "could not reach Audible Sync: " + lanTimeoutHint(cfg.BaseURL, err)})
		return
	}
	accounts, err := client.Accounts(ctx)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, audiblesync.ErrUnauthorized) {
			msg = "Audible Sync answered, but rejected the API token. Find it in Settings, API token in Audible Sync, or its AUDIBLE_SYNC_API_TOKEN."
		}
		writeJSON(w, http.StatusBadGateway, audibleSyncTestResponse{OK: false, Message: msg, Version: health.Version})
		return
	}
	msg := "Connected to Audible Sync"
	if health.Version != "" {
		msg += " " + health.Version
	}
	switch n := len(accounts); n {
	case 0:
		msg += ", but it has no Audible accounts yet. Add one in Audible Sync."
	case 1:
		msg += ", 1 Audible account"
	default:
		msg += ", " + strconv.Itoa(n) + " Audible accounts"
	}
	writeJSON(w, http.StatusOK, audibleSyncTestResponse{OK: true, Message: msg, Version: health.Version, Accounts: toAudibleSyncAccounts(accounts)})
}

// Accounts lists the Audible accounts the saved Audible Sync holds, for the
// account picker.
func (h *AudibleSyncHandler) Accounts(w http.ResponseWriter, r *http.Request) {
	client, _, err := h.clientFromProbe(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	accounts, err := client.Accounts(r.Context())
	if err != nil {
		writeUpstreamDetail(w, r, http.StatusBadGateway, err.Error(), err)
		return
	}
	writeJSON(w, http.StatusOK, toAudibleSyncAccounts(accounts))
}

func (h *AudibleSyncHandler) clientFromProbe(r *http.Request) (audibleSyncProber, AudibleSyncStoredConfig, error) {
	cfg := LoadAudibleSyncConfig(r.Context(), h.settings)
	var req struct {
		APIToken string `json:"apiToken"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return nil, cfg, errors.New("invalid request body")
		}
	}
	if cfg.BaseURL == "" {
		return nil, cfg, errors.New("save the Audible Sync URL before testing it")
	}
	token := strings.TrimSpace(req.APIToken)
	if token == "" {
		token = cfg.APIToken
	}
	if token == "" {
		return nil, cfg, errors.New("an API token is required: Audible Sync logs one on its first start, or set AUDIBLE_SYNC_API_TOKEN there")
	}
	c, err := h.newFn(cfg.BaseURL, token)
	return c, cfg, err
}

func toAudibleSyncAccounts(in []audiblesync.Account) []audibleSyncAccount {
	out := make([]audibleSyncAccount, 0, len(in))
	for _, a := range in {
		out = append(out, audibleSyncAccount{
			ID: a.ID, Name: a.Name, CountryCode: a.CountryCode, Enabled: a.Enabled, NeedsLogin: a.NeedsLogin,
		})
	}
	return out
}
