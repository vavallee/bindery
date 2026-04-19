package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
)

const (
	oidcFlowCookie = "bindery_oidc_flow"
	oidcFlowMaxAge = 10 * 60 // 10 minutes
)

var oidcProviderIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// OIDCHandler owns GET /auth/oidc/:provider/login and /callback.
type OIDCHandler struct {
	mgr      *oidc.Manager
	users    *db.UserRepo
	settings *db.SettingsRepo
	auth     *AuthHandler
}

func NewOIDCHandler(mgr *oidc.Manager, users *db.UserRepo, settings *db.SettingsRepo, auth *AuthHandler) *OIDCHandler {
	return &OIDCHandler{mgr: mgr, users: users, settings: settings, auth: auth}
}

// Login initiates the Authorization Code + PKCE flow.
// GET /api/v1/auth/oidc/:provider/login
func (h *OIDCHandler) Login(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "provider")
	if !oidcProviderIDRe.MatchString(providerID) {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}

	state, err := oidc.NewState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gen state: "+err.Error())
		return
	}
	nonce, err := oidc.NewNonce()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gen nonce: "+err.Error())
		return
	}
	verifier, err := oidc.NewVerifier()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gen verifier: "+err.Error())
		return
	}

	authURL, err := h.mgr.AuthURL(providerID, state, nonce, verifier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Store state+nonce+verifier in a secure HttpOnly cookie so the callback
	// can verify them without any server-side session store.
	flowVal, err := oidc.EncodeFlowState(state, nonce, verifier)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode flow: "+err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    flowVal,
		Path:     "/api/v1/auth/oidc",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   oidcFlowMaxAge,
	})

	http.Redirect(w, r, authURL, http.StatusFound)
}

// Callback completes the code exchange and issues a Bindery session cookie.
// GET /api/v1/auth/oidc/:provider/callback
func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerID := chi.URLParam(r, "provider")
	if !oidcProviderIDRe.MatchString(providerID) {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}

	// Retrieve and validate the flow state cookie.
	flowCookie, err := r.Cookie(oidcFlowCookie)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing flow cookie")
		return
	}
	fs, err := oidc.DecodeFlowState(flowCookie.Value)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid flow state: "+err.Error())
		return
	}

	// Clear the flow cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    "",
		Path:     "/api/v1/auth/oidc",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	// Validate state parameter.
	if r.URL.Query().Get("state") != fs.State {
		writeErr(w, http.StatusBadRequest, "state mismatch")
		return
	}

	// Check for IdP error response.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		// Strip CRLF from user-controlled query params before structured logging.
		slog.Warn("oidc callback: provider error", // #nosec -- providerID validated by oidcProviderIDRe at handler entry
			"error", sanitizeLog(errParam),
			"desc", sanitizeLog(desc),
			"provider", providerID,
		)
		writeErr(w, http.StatusUnauthorized, "oidc error from provider")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}

	claims, err := h.mgr.Exchange(ctx, providerID, code, fs.Nonce, fs.CodeVerifier)
	if err != nil {
		slog.Warn("oidc: token exchange failed", "provider", providerID, "error", err) // #nosec -- providerID validated by oidcProviderIDRe at handler entry
		writeErr(w, http.StatusUnauthorized, "token exchange failed")
		return
	}

	user, err := h.users.GetOrCreateByOIDC(ctx,
		claims.Issuer, claims.Sub,
		claims.PreferredUsername, claims.Email, claims.Name,
	)
	if err != nil {
		slog.Error("oidc: user provisioning failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "user provisioning failed")
		return
	}

	// Reuse the existing session issuance — OIDC sits in front of it.
	h.auth.issueSession(w, r, ctx, user.ID, true)
	// #nosec -- providerID validated by oidcProviderIDRe; user.Username from DB
	slog.Info("oidc: login successful", "provider", providerID, "username", user.Username)

	// Redirect to the UI root.
	http.Redirect(w, r, "/", http.StatusFound)
}

// GetProviders returns the configured OIDC provider list (id + name only,
// no secrets). Used by the login page to render "Sign in with X" buttons.
// GET /api/v1/auth/oidc/providers
func (h *OIDCHandler) GetProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, _ := h.settings.Get(ctx, SettingOIDCProviders)
	if s == nil || s.Value == "" {
		writeOK(w, []any{})
		return
	}
	ps, err := oidc.ParseProviders(s.Value)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "parse providers: "+err.Error())
		return
	}
	out := make([]oidc.ProviderPublicConfig, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Public())
	}
	writeOK(w, out)
}

// SetProviders stores the OIDC providers config and reloads the manager.
// PUT /api/v1/auth/oidc/providers
func (h *OIDCHandler) SetProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var incoming []oidc.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Load existing providers so we can preserve secrets not re-submitted.
	var existing []oidc.ProviderConfig
	if s, _ := h.settings.Get(ctx, SettingOIDCProviders); s != nil && s.Value != "" {
		existing, _ = oidc.ParseProviders(s.Value)
	}
	existingByID := make(map[string]oidc.ProviderConfig, len(existing))
	for _, e := range existing {
		existingByID[e.ID] = e
	}

	// Merge: preserve existing secret when incoming secret is empty.
	merged := make([]oidc.ProviderConfig, 0, len(incoming))
	for _, p := range incoming {
		if p.ClientSecret == "" {
			prev, ok := existingByID[p.ID]
			if !ok {
				writeErr(w, http.StatusBadRequest, "client_secret required for new provider: "+p.ID)
				return
			}
			p.ClientSecret = prev.ClientSecret
		}
		merged = append(merged, p)
	}

	raw, err := json.Marshal(merged) // #nosec G117 -- persisted server-side only, never returned via API (see ProviderPublicConfig split)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode: "+err.Error())
		return
	}
	if err := h.settings.Set(ctx, SettingOIDCProviders, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	// Reload async so the HTTP response isn't delayed by discovery.
	go func() {
		h.mgr.Reload(r.Context(), merged)
	}()
	writeOK(w, map[string]any{"ok": true, "count": len(merged)})
}

// cookieSecure mirrors the issueSession() logic in auth.go: respect
// BINDERY_COOKIE_SECURE env var, fall back to detecting TLS/proxy.
// G402 is excluded project-wide in security.yml (gosec -exclude=G402) because
// the Secure attribute is intentionally conditional on deployment mode.
func cookieSecure(r *http.Request) bool {
	switch CookieSecureMode() {
	case "always":
		return true
	case "never":
		return false
	default:
		return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	}
}

// sanitizeLog strips CR/LF from user-controlled strings before they reach
// log sinks, preventing CRLF log-injection even in structured loggers.
func sanitizeLog(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
