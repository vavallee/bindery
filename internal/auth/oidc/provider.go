// Package oidc implements OIDC Authorization Code + PKCE for Bindery.
// Providers are configured via the settings table (key "auth.oidc.providers")
// as a JSON array. Each provider gets its own go-oidc verifier with a shared
// JWKS cache so we never hit the IdP on every request.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ProviderConfig is the internal representation persisted to the settings
// table. It includes the client secret and must never be marshalled directly
// to an API response — use ProviderPublicConfig for that.
type ProviderConfig struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Issuer        string   `json:"issuer"`
	ClientID      string   `json:"client_id"`
	ClientSecret  string   `json:"client_secret"` // write-only: never returned to callers
	Scopes        []string `json:"scopes"`
	AllowedGroups []string `json:"allowed_groups,omitempty"`
}

// ProviderPublicConfig is the API-safe view of a provider — no secret fields.
// Used in GET /auth/oidc/providers responses.
type ProviderPublicConfig struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Issuer        string   `json:"issuer"`
	ClientID      string   `json:"client_id"`
	Scopes        []string `json:"scopes"`
	AllowedGroups []string `json:"allowed_groups,omitempty"`
}

// Public returns the API-safe view of the config with the secret stripped.
func (c ProviderConfig) Public() ProviderPublicConfig {
	return ProviderPublicConfig{
		ID:            c.ID,
		Name:          c.Name,
		Issuer:        c.Issuer,
		ClientID:      c.ClientID,
		Scopes:        c.Scopes,
		AllowedGroups: c.AllowedGroups,
	}
}

// ParseProviders deserialises the stored JSON array.
func ParseProviders(raw string) ([]ProviderConfig, error) {
	if raw == "" {
		return nil, nil
	}
	var ps []ProviderConfig
	if err := json.Unmarshal([]byte(raw), &ps); err != nil {
		return nil, fmt.Errorf("parse oidc providers: %w", err)
	}
	return ps, nil
}

// Claims extracted from a validated ID token.
type Claims struct {
	Sub               string
	Issuer            string
	Email             string
	PreferredUsername string
	Name              string
	Groups            []string
}

// Manager holds per-provider OIDC verifiers and oauth2 configs, keyed by
// provider ID. It is rebuilt when the settings change.
type Manager struct {
	mu           sync.RWMutex
	providers    map[string]*entry
	redirectBase string
}

type entry struct {
	cfg      ProviderConfig
	verifier *gooidc.IDTokenVerifier
	oauth2   oauth2.Config
}

func NewManager(redirectBase string) *Manager {
	return &Manager{
		providers:    make(map[string]*entry),
		redirectBase: redirectBase,
	}
}

// Reload replaces the provider set from a fresh config slice. Providers that
// haven't changed keep their verifier (and therefore JWKS cache).
func (m *Manager) Reload(ctx context.Context, cfgs []ProviderConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	next := make(map[string]*entry, len(cfgs))
	for _, cfg := range cfgs {
		// Re-use the existing entry if config is identical (preserves JWKS cache).
		if e, ok := m.providers[cfg.ID]; ok && configEqual(e.cfg, cfg) {
			next[cfg.ID] = e
			continue
		}
		e, err := m.buildEntry(ctx, cfg)
		if err != nil {
			slog.Error("oidc: failed to initialise provider, skipping", "id", cfg.ID, "error", err)
			continue
		}
		next[cfg.ID] = e
	}
	m.providers = next
}

func (m *Manager) buildEntry(ctx context.Context, cfg ProviderConfig) (*entry, error) {
	p, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	oc := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  m.redirectBase + "/api/v1/auth/oidc/" + cfg.ID + "/callback",
		Scopes:       scopes,
	}
	verifier := p.Verifier(&gooidc.Config{ClientID: cfg.ClientID})
	return &entry{cfg: cfg, verifier: verifier, oauth2: oc}, nil
}

// Get returns the entry for a provider ID, or nil if not found.
func (m *Manager) Get(id string) *entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providers[id]
}

// List returns all configured provider configs (for the login page).
func (m *Manager) List() []ProviderConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ProviderConfig, 0, len(m.providers))
	for _, e := range m.providers {
		out = append(out, e.cfg)
	}
	return out
}

// AuthURL returns the Authorization Code + PKCE redirect URL for the provider.
// state and codeVerifier are caller-generated; nonce is embedded in the URL.
func (m *Manager) AuthURL(id, state, nonce, codeVerifier string) (string, error) {
	e := m.Get(id)
	if e == nil {
		return "", fmt.Errorf("unknown oidc provider %q", id)
	}
	challenge := pkceChallenge(codeVerifier)
	return e.oauth2.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

// Exchange completes the code exchange and validates the ID token.
// Returns the verified Claims on success.
func (m *Manager) Exchange(ctx context.Context, id, code, nonce, codeVerifier string) (*Claims, error) {
	e := m.Get(id)
	if e == nil {
		return nil, fmt.Errorf("unknown oidc provider %q", id)
	}
	token, err := e.oauth2.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("oidc: no id_token in response")
	}
	idToken, err := e.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc: id_token verification failed: %w", err)
	}
	if idToken.Nonce != nonce {
		return nil, fmt.Errorf("oidc: nonce mismatch")
	}
	var claims struct {
		Sub               string   `json:"sub"`
		Email             string   `json:"email"`
		PreferredUsername string   `json:"preferred_username"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: parse claims: %w", err)
	}
	return &Claims{
		Sub:               claims.Sub,
		Issuer:            idToken.Issuer,
		Email:             claims.Email,
		PreferredUsername: claims.PreferredUsername,
		Name:              claims.Name,
		Groups:            claims.Groups,
	}, nil
}

// --- PKCE helpers ------------------------------------------------------------

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// NewVerifier generates a cryptographically random PKCE code verifier.
func NewVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewState generates a random state string for CSRF protection in the OAuth flow.
func NewState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewNonce generates a random nonce for replay protection.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// --- State cookie value (state + nonce + codeVerifier packed as JSON) --------

type flowState struct {
	State        string    `json:"s"`
	Nonce        string    `json:"n"`
	CodeVerifier string    `json:"cv"`
	Expiry       time.Time `json:"exp"`
}

func EncodeFlowState(state, nonce, codeVerifier string) (string, error) {
	fs := flowState{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		Expiry:       time.Now().Add(10 * time.Minute),
	}
	return encodeFlowStateRaw(fs)
}

func configEqual(a, b ProviderConfig) bool {
	aj, _ := json.Marshal(a) // #nosec G117 -- persisted server-side only, never returned via API (see ProviderPublicConfig split)
	bj, _ := json.Marshal(b) // #nosec G117 -- persisted server-side only, never returned via API (see ProviderPublicConfig split)
	return string(aj) == string(bj)
}

func encodeFlowStateRaw(fs flowState) (string, error) {
	b, err := json.Marshal(fs)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodeFlowState(encoded string) (*flowState, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode flow state: %w", err)
	}
	var fs flowState
	if err := json.Unmarshal(b, &fs); err != nil {
		return nil, fmt.Errorf("parse flow state: %w", err)
	}
	if time.Now().After(fs.Expiry) {
		return nil, fmt.Errorf("flow state expired")
	}
	return &fs, nil
}
