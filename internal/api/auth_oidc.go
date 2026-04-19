package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
)

const (
	oidcFlowCookie  = "bindery_oidc_flow"
	oidcFlowMaxAge  = 10 * 60 // 10 minutes
)

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

	// Store state+nonce+verifier in an HttpOnly cookie so the callback can
	// verify them without any server-side session store.
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
		Name:   oidcFlowCookie,
		Value:  "",
		Path:   "/api/v1/auth/oidc",
		MaxAge: -1,
	})

	// Validate state parameter.
	if r.URL.Query().Get("state") != fs.State {
		writeErr(w, http.StatusBadRequest, "state mismatch")
		return
	}

	// Check for IdP error response.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("oidc callback: provider error", "error", errParam, "desc", desc)
		writeErr(w, http.StatusUnauthorized, "oidc error: "+errParam)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}

	claims, err := h.mgr.Exchange(ctx, providerID, code, fs.Nonce, fs.CodeVerifier)
	if err != nil {
		slog.Warn("oidc: token exchange failed", "provider", providerID, "error", err)
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
	type providerInfo struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]providerInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, providerInfo{ID: p.ID, Name: p.Name})
	}
	writeOK(w, out)
}

// SetProviders stores the OIDC providers config and reloads the manager.
// PUT /api/v1/auth/oidc/providers
func (h *OIDCHandler) SetProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Validate that it's a parseable provider array.
	ps, err := oidc.ParseProviders(string(raw))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.settings.Set(ctx, SettingOIDCProviders, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	// Reload async so the HTTP response isn't delayed by discovery.
	go func() {
		h.mgr.Reload(r.Context(), ps)
	}()
	writeOK(w, map[string]any{"ok": true, "count": len(ps)})
}

