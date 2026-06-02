package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
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
//
// baseConfigured is true when BINDERY_OIDC_REDIRECT_BASE_URL was explicitly
// set, false when the base URL will be derived from request headers. Used by
// GetRedirectBase to tell the UI whether to show a "URL may not match" warning.
type OIDCHandler struct {
	mgr               *oidc.Manager
	users             *db.UserRepo
	settings          *db.SettingsRepo
	auth              *AuthHandler
	resolveBase       func(*http.Request) string
	baseConfigured    bool
	oidcAutoProvision bool
	oidcEmailLink     bool
	// localAuthEnabled mirrors BINDERY_LOCAL_AUTH_ENABLED. When false, the
	// promote-first-OIDC-user fallback (issue #688) is armed.
	localAuthEnabled bool
	// oidcDefaultRole is the role assigned to a freshly auto-provisioned OIDC
	// user (issue #688). "admin" or "user"; coerced to "user" if invalid.
	oidcDefaultRole string
	// oidcAdminGroup, when non-empty, makes the IdP authoritative for the admin
	// role: every login promotes/demotes the user based on group membership.
	oidcAdminGroup string
	// oidcGroupClaim is the ID-token claim path holding the user's groups.
	oidcGroupClaim string

	// lifetimeCtx is the process-lifecycle context, cancelled on server
	// shutdown so the async Manager.Reload goroutine spawned by SetProviders
	// is cancelled cleanly. Falls back to context.Background(); see #846.
	lifetimeCtx context.Context
}

// WithLifetimeCtx attaches the process-lifecycle context so the async
// Manager.Reload goroutine respects shutdown.
func (h *OIDCHandler) WithLifetimeCtx(ctx context.Context) *OIDCHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
func (h *OIDCHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

func NewOIDCHandler(mgr *oidc.Manager, users *db.UserRepo, settings *db.SettingsRepo, auth *AuthHandler, resolveBase func(*http.Request) string) *OIDCHandler {
	return &OIDCHandler{
		mgr:               mgr,
		users:             users,
		settings:          settings,
		auth:              auth,
		resolveBase:       resolveBase,
		oidcAutoProvision: true,
		oidcEmailLink:     false,
		localAuthEnabled:  true,
		oidcDefaultRole:   "user",
		oidcGroupClaim:    "groups",
	}
}

// WithBaseConfigured sets the baseConfigured flag, indicating that
// BINDERY_OIDC_REDIRECT_BASE_URL was explicitly configured. When false,
// GetRedirectBase signals the UI to display a warning that the callback URL
// shown may not match what the IdP receives (because it is derived from
// request headers, which can vary).
func (h *OIDCHandler) WithBaseConfigured(configured bool) *OIDCHandler {
	h.baseConfigured = configured
	return h
}

// WithOIDCAutoProvision controls whether an unknown OIDC subject (issuer+sub
// pair not found in the DB) triggers automatic user creation. When false, the
// callback returns 403 instead of provisioning a new account.
func (h *OIDCHandler) WithOIDCAutoProvision(v bool) *OIDCHandler {
	h.oidcAutoProvision = v
	return h
}

// WithOIDCEmailLink controls whether an unknown OIDC subject is matched
// against existing users by email on first login. When true, a successful
// email match links the OIDC identity to the existing account rather than
// creating a new one or returning 403.
func (h *OIDCHandler) WithOIDCEmailLink(v bool) *OIDCHandler {
	h.oidcEmailLink = v
	return h
}

// WithLocalAuthEnabled mirrors BINDERY_LOCAL_AUTH_ENABLED into the OIDC handler.
// When false, the promote-first-OIDC-user fallback is armed: an OIDC login that
// finds zero admins in the DB promotes the user being provisioned to admin
// (issue #688). Default true.
func (h *OIDCHandler) WithLocalAuthEnabled(v bool) *OIDCHandler {
	h.localAuthEnabled = v
	return h
}

// WithOIDCDefaultRole sets the role assigned at OIDC auto-provision time
// (issue #688). Valid values are "admin" and "user"; any other value falls
// back to "user".
func (h *OIDCHandler) WithOIDCDefaultRole(role string) *OIDCHandler {
	if role != "admin" && role != "user" {
		role = "user"
	}
	h.oidcDefaultRole = role
	return h
}

// WithOIDCAdminGroup sets the IdP group whose presence in the configured group
// claim grants the admin role (issue #688). When non-empty, the IdP becomes
// authoritative: every login promotes the user to admin if the group is present
// and demotes to user if it is absent. Empty disables group-based role mapping.
func (h *OIDCHandler) WithOIDCAdminGroup(group string) *OIDCHandler {
	h.oidcAdminGroup = strings.TrimSpace(group)
	return h
}

// WithOIDCGroupClaim sets the ID-token claim path Bindery reads the user's
// groups from (issue #688). Default "groups".
func (h *OIDCHandler) WithOIDCGroupClaim(claim string) *OIDCHandler {
	if c := strings.TrimSpace(claim); c != "" {
		h.oidcGroupClaim = c
	}
	return h
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

	// Enforce the provider's AllowedGroups policy. When AllowedGroups is
	// configured (non-empty), a login is only admitted if the IdP's `groups`
	// claim intersects it; an empty AllowedGroups means "allow all" and the
	// check is a no-op. This is fail-closed by design: if the IdP is not
	// sending a `groups` claim, or sends a different group name than what is
	// configured, every login is rejected — the admin must fix the IdP scope
	// mapping or the configured group name.
	if cfg, ok := h.mgr.ProviderConfig(providerID); ok && len(cfg.AllowedGroups) > 0 {
		if !oidc.GroupsAllowed(cfg.AllowedGroups, claims.Groups) {
			slog.Warn("oidc: login rejected by AllowedGroups policy",
				"provider", providerID, // #nosec -- providerID validated by oidcProviderIDRe at handler entry
				"sub", sanitizeLog(claims.Sub),
				"user_groups", len(claims.Groups),
				"allowed_groups", strings.Join(cfg.AllowedGroups, ","),
			)
			writeErr(w, http.StatusForbidden,
				"access denied: your account is not a member of an allowed group for this provider "+
					"(check the provider's allowed_groups setting and that the IdP is sending a 'groups' claim)")
			return
		}
	}

	// 1. Look up by (issuer, sub)
	user, err := h.users.GetByOIDC(ctx, claims.Issuer, claims.Sub)
	if err != nil {
		slog.Error("oidc: user lookup failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "user lookup failed")
		return
	}

	// 2. Email-link: try to match an existing user by email.
	//
	// SECURITY: only ever link by email when the IdP asserts the address is
	// verified (`email_verified == true`). Linking binds the OIDC
	// (issuer, sub) pair to an existing Bindery account — potentially an
	// admin account — so an unverified `email` claim is an account-takeover
	// vector: an attacker who can set an arbitrary unverified email at a
	// trusted IdP could otherwise claim a victim's account. If the email is
	// unverified (or the IdP omits `email_verified`), skip linking and fall
	// through to normal provisioning-by-subject below.
	if user == nil && h.oidcEmailLink && claims.Email != "" && !claims.EmailVerified {
		slog.Warn("oidc: skipping email-link for unverified email claim",
			"provider", providerID, // #nosec -- providerID validated by oidcProviderIDRe at handler entry
			"sub", sanitizeLog(claims.Sub),
			"reason", "IdP did not assert email_verified=true; falling through to subject-based provisioning")
	}
	if user == nil && h.oidcEmailLink && claims.Email != "" && claims.EmailVerified {
		byEmail, err := h.users.GetByEmail(ctx, claims.Email)
		if err != nil {
			slog.Error("oidc: email lookup failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "user lookup failed")
			return
		}
		if byEmail != nil {
			if err := h.users.LinkOIDCSubject(ctx, byEmail.ID, claims.Issuer, claims.Sub); err != nil {
				slog.Error("oidc: link subject failed", "error", err)
				writeErr(w, http.StatusInternalServerError, "user link failed")
				return
			}
			slog.Info("oidc: linked existing user by email", "username", byEmail.Username, "email", claims.Email)
			user = byEmail
		}
	}

	// 3. Auto-provision or deny
	if user == nil {
		if !h.oidcAutoProvision {
			slog.Warn("oidc: unknown user and auto-provisioning disabled", "issuer", claims.Issuer, "sub", sanitizeLog(claims.Sub))
			writeErr(w, http.StatusForbidden, "access denied: account not provisioned")
			return
		}
		// Decide the provisioning role (issue #688). Start from the configured
		// default; the promote-first-OIDC-user fallback may upgrade it to admin.
		// The fallback is gated by SettingsRepo.SetIfAbsent on the
		// FirstAdminPromoted guard — only one concurrent first-time login can
		// win the race and become admin; any other simultaneous logins fall
		// back to the default role even when they all observed admins == 0.
		provisionRole := h.resolveProvisionRole(ctx)
		user, err = h.users.GetOrCreateByOIDC(ctx,
			claims.Issuer, claims.Sub,
			claims.PreferredUsername, claims.Email, claims.Name,
			provisionRole,
		)
		if err != nil {
			slog.Error("oidc: user provisioning failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "user provisioning failed")
			return
		}
	}

	// 4. Group-claim role sync (issue #688). When BINDERY_OIDC_ADMIN_GROUP is
	// configured, the IdP is authoritative for the admin role on every login:
	// promote when the group is present, demote when absent. This intentionally
	// overrides any role set via PUT /api/v1/auth/users/{id}/role for OIDC users.
	if h.oidcAdminGroup != "" {
		groups := oidc.GroupClaimValues(claims.Raw, h.oidcGroupClaim)
		want := "user"
		if oidc.ContainsGroup(groups, h.oidcAdminGroup) {
			want = "admin"
		}
		if user.Role != want {
			if err := h.users.SetRoleUnguarded(ctx, user.ID, want); err != nil {
				slog.Error("oidc: group-claim role sync failed", "error", err, "user_id", user.ID)
				writeErr(w, http.StatusInternalServerError, "role sync failed")
				return
			}
			slog.Info("oidc: synced role from group claim",
				"username", user.Username, "from", user.Role, "to", want,
				"admin_group", h.oidcAdminGroup, "group_claim", h.oidcGroupClaim)
			user.Role = want
		}
	}

	// Reuse the existing session issuance — OIDC sits in front of it.
	h.auth.issueSession(w, r, ctx, user.ID, true)
	// #nosec -- providerID validated by oidcProviderIDRe; user.Username from DB
	slog.Info("oidc: login successful", "provider", providerID, "username", user.Username)

	// Redirect to the UI root.
	http.Redirect(w, r, "/", http.StatusFound)
}

// resolveProvisionRole decides the role for a brand-new OIDC user (issue #688).
//
// It starts from the configured BINDERY_OIDC_DEFAULT_ROLE. The
// promote-first-OIDC-user fallback then upgrades the result to "admin" when ALL
// of the following hold:
//
//   - local auth is disabled (BINDERY_LOCAL_AUTH_ENABLED=false) — without it
//     there is no other path back into an admin-less instance;
//   - the DB currently has zero admin users — the lockout trap;
//   - the one-shot SettingOIDCFirstAdminPromoted guard has never been set —
//     so deleting every admin later does not silently re-promote.
//
// Any error reading the guard or counting admins is treated conservatively:
// the fallback does not fire and the configured default role is used.
func (h *OIDCHandler) resolveProvisionRole(ctx context.Context) string {
	role := h.oidcDefaultRole
	if role != "admin" && role != "user" {
		role = "user"
	}
	if role == "admin" {
		return role // already admin; fallback would be a no-op
	}
	if h.localAuthEnabled {
		return role // local auth still offers a recovery path
	}
	admins, err := h.users.CountAdmins(ctx)
	if err != nil {
		slog.Warn("oidc: cannot count admins; skipping promote-first fallback", "error", err)
		return role
	}
	if admins != 0 {
		return role
	}
	// Atomically claim the promote-first slot. SetIfAbsent is an INSERT ON
	// CONFLICT DO NOTHING — exactly one concurrent first-time login wins; any
	// other simultaneous login that also saw admins == 0 loses here and falls
	// back to the default role. Without this, a TOCTOU race between the count
	// and the legacy Get+Set guard could promote two users to admin.
	won, err := h.settings.SetIfAbsent(ctx, SettingOIDCFirstAdminPromoted, "true")
	if err != nil {
		slog.Warn("oidc: cannot claim first-admin guard; skipping promote-first fallback", "error", err)
		return role
	}
	if !won {
		return role // another concurrent first-time login already claimed it
	}
	slog.Warn("oidc: promoting first OIDC user to admin — local auth disabled and no admin exists")
	return "admin"
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
	// SSRF guard: block loopback, link-local, and cloud-metadata destinations
	// so an admin (or anyone whose session leaks once) cannot use this admin
	// probe to fingerprint internal services or read AWS/Azure/DO metadata. The
	// default policy is LAN (RFC1918 still reachable for legitimate on-prem
	// IdPs); operators with stricter needs can keep the default, and operators
	// who intentionally point Bindery at a LAN IdP get the default behaviour
	// without further config. BINDERY_ALLOW_LAN_OIDC=true keeps the historical
	// behaviour where any URL the admin types is fetched verbatim, including
	// loopback, for users who run an OIDC provider on the Bindery host itself.
	var discoverOpts []oidc.DiscoverOption
	if !oidcAllowLAN() {
		policy := oidcDiscoveryPolicy()
		if err := httpsec.ValidateOutboundURL(issuer, policy); err != nil {
			// Mirror the handler's existing "report inline, don't surface as
			// HTTP error" contract: any failure that has the user-visible
			// shape of "we did not fetch the IdP" (unreachable, SSRF refusal,
			// scheme refusal) goes back as ok=false in the body so the Settings
			// UI can render the message next to the issuer field. The 400
			// branch is reserved for malformed input (e.g. empty issuer).
			writeOK(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// Same policy is re-applied to every redirect hop inside Discover so an
		// attacker can't bounce the request from a public URL into a private one.
		discoverOpts = append(discoverOpts, oidc.DiscoverPolicy(policy))
	}
	doc, err := oidc.Discover(r.Context(), issuer, discoverOpts...)
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
		// configured is true when BINDERY_OIDC_REDIRECT_BASE_URL was explicitly
		// set. When false, the base URL is derived from request headers (forwarded
		// or Host) and the UI should warn that the value shown may differ from
		// what the IdP actually receives.
		"configured": h.baseConfigured,
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
		var perr error
		existing, perr = oidc.ParseProviders(s.Value)
		if perr != nil {
			slog.Warn("auth_oidc: parse providers", "error", perr)
		}
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
	// Reload async so the HTTP response isn't delayed by discovery. Anchor
	// on the lifetime ctx so the goroutine cancels cleanly on shutdown,
	// rather than letting the discovery roundtrip complete against a process
	// that's about to exit. The request ctx is intentionally dropped, since
	// it would cancel as soon as the HTTP response is written.
	reloadCtx := h.bgCtx()
	go func() {
		h.mgr.Reload(reloadCtx, merged)
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

// oidcAllowLAN reports whether the operator has opted out of the SSRF guard
// on the OIDC discovery probe via BINDERY_ALLOW_LAN_OIDC. Setting this to
// "1" or "true" (case-insensitive) restores the historical behaviour where
// any URL the admin types is fetched verbatim, including loopback and other
// destinations a hardened deploy would normally refuse. The escape hatch
// exists for users who run an OIDC provider on the Bindery host itself.
func oidcAllowLAN() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("BINDERY_ALLOW_LAN_OIDC")))
	return v == "1" || v == "true"
}

// oidcDiscoveryPolicy returns the SSRF policy applied to the OIDC discovery
// probe and to redirects followed by the discovery client. PolicyLAN is the
// default: it blocks loopback, link-local, and cloud-metadata, but allows
// RFC1918 destinations so an on-prem Keycloak/Authentik on a LAN IP keeps
// working. Stricter setups can run with the LAN-side IdP unreachable from
// the Bindery host.
func oidcDiscoveryPolicy() httpsec.Policy {
	return httpsec.PolicyLAN
}
