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
//
// resolveBase resolves the public-facing base URL from the incoming request.
// Most deploys want the helper from this package (ResolveOIDCRedirectBase)
// which honors the configured BINDERY_OIDC_REDIRECT_BASE_URL env var first
// and falls back to forwarded headers from a trusted proxy. Tests can supply
// a fixed-value resolver.
type OIDCHandler struct {
	mgr         *oidc.Manager
	users       *db.UserRepo
	settings    *db.SettingsRepo
	auth        *AuthHandler
	resolveBase func(*http.Request) string
}

func NewOIDCHandler(mgr *oidc.Manager, users *db.UserRepo, settings *db.SettingsRepo, auth *AuthHandler, resolveBase func(*http.Request) string) *OIDCHandler {
	return &OIDCHandler{mgr: mgr, users: users, settings: settings, auth: auth, resolveBase: resolveBase}
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

	redirectBase := h.resolveBase(r)
	authURL, err := h.mgr.AuthURL(r.Context(), redirectBase, providerID, state, nonce, verifier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Store state+nonce+verifier+redirectBase in a secure HttpOnly cookie so
	// the callback can verify them without any server-side session store.
	// redirectBase is round-tripped because the IdP requires the redirect_uri
	// in the token exchange to match the one used in the authorize request,
	// and we resolve it from the request rather than a static env var.
	flowVal, err := oidc.EncodeFlowState(state, nonce, verifier, redirectBase)
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

	// Use the redirectBase pinned by /login when available — the IdP enforces
	// that the token-exchange redirect_uri matches the authorize redirect_uri.
	// Older flow cookies (pre-upgrade) may not have it; fall back to resolving
	// from the current request, which is correct for any deploy where the
	// proxy chain is stable across the two requests.
	redirectBase := fs.RedirectBase
	if redirectBase == "" {
		redirectBase = h.resolveBase(r)
	}
	claims, err := h.mgr.Exchange(ctx, redirectBase, providerID, code, fs.Nonce, fs.CodeVerifier)
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

// TestDiscovery probes <issuer>/.well-known/openid-configuration and returns
// the discovered metadata. The Settings UI exposes this as a "Test" button
// next to the issuer field so admins see DNS errors, TLS errors, 404s, and
// — most importantly — issuer-mismatch (where the IdP's discovered issuer
// differs from what the user entered, e.g. Authentik per-provider mode or
// Keycloak realm paths) before attempting a real login.
//
// On unreachable IdP / parse error / non-2xx, returns 200 with {ok:false,
// error:"..."} rather than HTTP-error so the UI can render the message
// inline; the request itself succeeded.
// POST /api/v1/auth/oidc/test-discovery
func (h *OIDCHandler) TestDiscovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	issuer := strings.TrimSpace(req.Issuer)
	if issuer == "" {
		writeErr(w, http.StatusBadRequest, "issuer required")
		return
	}
	doc, err := oidc.Discover(r.Context(), issuer)
	if err != nil {
		writeOK(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeOK(w, map[string]any{
		"ok":              true,
		"issuer_mismatch": strings.TrimRight(doc.Issuer, "/") != strings.TrimRight(issuer, "/"),
		"discovered":      doc,
	})
}

// GetRedirectBase returns the public-facing base URL Bindery will use as
// the prefix for OIDC callback URLs, resolved from the current request. The
// Settings UI uses this to render a live preview of the redirect URI so
// admins can copy-paste it into their IdP without constructing it manually —
// the #1 source of redirect_uri_mismatch errors.
// GET /api/v1/auth/oidc/redirect-base
func (h *OIDCHandler) GetRedirectBase(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"base":          h.resolveBase(r),
		"callback_path": oidc.CallbackPath("{id}"),
	})
}

// providerWithStatus pairs the public config of a configured provider with
// its current runtime status (loaded vs failed-discovery). Status is decided
// from the in-memory manager state, not the DB — a provider configured in the
// DB but missing from the manager is "failed" and unusable for login until it
// recovers (see EnsureLoaded).
type providerWithStatus struct {
	oidc.ProviderPublicConfig
	Status *oidc.Status `json:"status,omitempty"`
}

// GetProviders returns the configured OIDC provider list (id + name only,
// no secrets) with a per-provider runtime status block. Used by the admin
// settings UI; the public login page only needs id+name and ignores the rest.
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
	out := make([]providerWithStatus, 0, len(ps))
	for _, p := range ps {
		out = append(out, providerWithStatus{
			ProviderPublicConfig: p.Public(),
			Status:               h.mgr.Status(p.ID),
		})
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
