package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// CookieSecureMode controls when the Secure flag is set on session cookies.
// "auto" (default): detect TLS via r.TLS or X-Forwarded-Proto.
// "always": always set Secure (useful when the proxy doesn't send the header).
// "never":  never set Secure (legacy plain-HTTP installs with no proxy).
// Exported so the security regression suite in tests/security can assert
// the env-var contract without reaching into the package's internals.
func CookieSecureMode() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("BINDERY_COOKIE_SECURE"))); v {
	case "always", "never":
		return v
	default:
		return "auto"
	}
}

// AuthSettings keys in the shared `settings` table.
// gosec G101 flags these as "potential hardcoded credentials" because of the
// names, but they're setting-key identifiers, not secret values.
const (
	SettingAuthAPIKey        = "auth.api_key"        //nolint:gosec // setting key name, not a credential
	SettingAuthSessionSecret = "auth.session_secret" //nolint:gosec // setting key name, not a credential
	// SettingAuthSessionSecretPrevious holds the secret rotated out by the most
	// recent rotation. It is consulted only for *verification* so a cookie
	// signed under the just-rotated-out secret still validates during the
	// rotation window; SignSession never uses it. Absent/empty until the first
	// rotation, in which case verification is single-secret as before.
	SettingAuthSessionSecretPrevious = "auth.session_secret_previous" //nolint:gosec // setting key name, not a credential
	SettingAuthMode                  = "auth.mode"
	SettingOIDCProviders             = "auth.oidc.providers" //nolint:gosec // setting key name, not a credential
	// SettingOIDCFirstAdminPromoted is a one-shot guard for the
	// promote-first-OIDC-user fallback (issue #688). Once an OIDC user has been
	// auto-promoted to admin because the system had zero admins, this flag is
	// set so deleting every admin later cannot silently re-trigger promotion.
	SettingOIDCFirstAdminPromoted = "auth.oidc.first_admin_promoted" //nolint:gosec // setting key name, not a credential
)

// AuthHandler owns the login / setup / password / mode endpoints.
type AuthHandler struct {
	users            *db.UserRepo
	settings         *db.SettingsRepo
	limiter          *auth.LoginLimiter
	localAuthEnabled bool
	// revocations is the logout denylist. Nil only in harnesses that do not
	// exercise logout; production wires it via WithSessionRevocations.
	revocations *db.SessionRevocations
	// setupMu serialises first-run setup in this process so concurrent
	// callers queue instead of each running the KDF. The one-admin guarantee
	// itself is UserRepo.CreateFirstAdmin's single statement insert.
	setupMu sync.Mutex
	// verifyPassword is auth.VerifyPasswordContext; tests swap it to drive
	// the path where the caller gives up before a KDF slot frees.
	verifyPassword func(ctx context.Context, password, phc string) (bool, error)
}

func NewAuthHandler(users *db.UserRepo, settings *db.SettingsRepo, limiter *auth.LoginLimiter) *AuthHandler {
	return &AuthHandler{users: users, settings: settings, limiter: limiter, localAuthEnabled: true, verifyPassword: auth.VerifyPasswordContext}
}

// WithLocalAuthEnabled controls whether local password login and local user
// creation are allowed. When false, POST /auth/login returns 403 and the
// admin user-create endpoint is also blocked.
func (h *AuthHandler) WithLocalAuthEnabled(v bool) *AuthHandler {
	h.localAuthEnabled = v
	return h
}

// WithSessionRevocations wires the logout denylist. It must be the same
// instance the auth middleware's Provider consults.
func (h *AuthHandler) WithSessionRevocations(s *db.SessionRevocations) *AuthHandler {
	h.revocations = s
	return h
}

// --- Request / response shapes -----------------------------------------------

type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type statusResponse struct {
	Authenticated    bool   `json:"authenticated"`
	SetupRequired    bool   `json:"setupRequired"`
	Username         string `json:"username,omitempty"`
	Role             string `json:"role,omitempty"`
	Mode             string `json:"mode"`
	LocalAuthEnabled bool   `json:"localAuthEnabled"`
	// HostNotAllowed is set when the auth mode would have admitted the
	// caller without a login but the host name it used is not one Bindery
	// accepts for that (see auth.HostAllowedForModeGrant). The caller is
	// reported unauthenticated; the field says why.
	HostNotAllowed bool `json:"hostNotAllowed,omitempty"`
	// RefusedHost is the name that was refused, so the login page can tell
	// the operator what to add to BINDERY_ALLOWED_HOSTS. It is the caller's
	// own Host (or forwarded host), echoed back to the same caller.
	RefusedHost string `json:"refusedHost,omitempty"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type authConfigResponse struct {
	Mode     string `json:"mode"`
	APIKey   string `json:"apiKey"`
	Username string `json:"username"`
}

type modeRequest struct {
	Mode string `json:"mode"`
}

// --- Handlers ----------------------------------------------------------------

// Status reports whether the current request is authenticated and whether
// setup is required. Always public — this is what the frontend hits on load.
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// A signed session cookie the middleware could not check is not a
	// signed out caller. The UI reads authenticated=false from this route as
	// a logout and goes to the login page, so answer with an error it can
	// retry instead. A request the client abandoned gets no body at all.
	if auth.SessionLookupFailed(ctx) {
		writeSessionCheckUnavailable(w, r, nil)
		return
	}
	count, err := h.users.Count(ctx)
	if err != nil {
		if ctx.Err() != nil {
			w.WriteHeader(auth.StatusClientClosedRequest)
			return
		}
		writeServerError(w, r, err)
		return
	}
	mode := h.mode(ctx)

	resp := statusResponse{
		SetupRequired:    count == 0,
		Mode:             string(mode),
		LocalAuthEnabled: h.localAuthEnabled,
	}

	if uid := auth.UserIDFromContext(ctx); uid != 0 {
		u, err := h.users.GetByID(ctx, uid)
		if err != nil {
			// Same reasoning: a failed read is not a deleted user.
			writeSessionCheckUnavailable(w, r, err)
			return
		}
		if u != nil {
			resp.Authenticated = true
			resp.Username = u.Username
			resp.Role = u.Role
		}
	} else if requestHasAdminSemantics(r, h.settings) {
		// Trusted bypass — the UI should render normally without a login screen.
		// Treat as admin so role-gated UI surfaces correctly.
		resp.Authenticated = true
		resp.Role = "admin"
	} else if host, refused := auth.RefusedModeGrantHost(mode, r, auth.EnvTrustedProxyCIDRs()); refused {
		// The UI reads this route first and goes to the login page on
		// authenticated=false, so it may be the only request the operator's
		// browser makes. Log the refusal here too, or the name to allow
		// would never reach the log.
		auth.LogRefusedModeGrantHost(host)
		resp.HostNotAllowed = true
		resp.RefusedHost = host
	}

	writeOK(w, resp)
}

// writeSessionCheckUnavailable answers a status request whose session could
// not be checked: 503 so the UI keeps its signed in state and retries, or
// nothing useful when the client already abandoned the request.
// err, when set, is logged; the middleware already logged its own failure.
func writeSessionCheckUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		w.WriteHeader(auth.StatusClientClosedRequest)
		return
	}
	if err != nil {
		slog.Error("session check failed", "path", r.URL.Path, "error", err)
	}
	writeErr(w, http.StatusServiceUnavailable, "session check unavailable, try again")
}

// Setup creates the first admin user. Only allowed while no user exists.
//
// Concurrent calls create exactly one account: setupMu queues them in this
// process, and CreateFirstAdmin only inserts into an empty users table, so
// even a caller that got past the count check loses at the insert. Every
// loser gets the same 409 a call after setup gets.
func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h.setupMu.Lock()
	defer h.setupMu.Unlock()
	count, err := h.users.Count(ctx)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if count > 0 {
		writeErr(w, http.StatusConflict, "setup already complete")
		return
	}
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "username required and password must be ≥ 8 chars")
		return
	}
	hash, err := auth.HashPasswordContext(ctx, req.Password)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// The first user is the admin: CreateFirstAdmin writes the row with that
	// role, so the freshly set up operator is never left on "user" and locked
	// out of every admin gated page (Calibre plugin, user management, etc).
	u, err := h.users.CreateFirstAdmin(ctx, req.Username, hash)
	if errors.Is(err, db.ErrSetupComplete) {
		writeErr(w, http.StatusConflict, "setup already complete")
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// Log the user in immediately.
	if !h.issueSession(w, r, ctx, u.ID, true) {
		return
	}
	slog.Info("first-run setup complete", "username", u.Username)
	writeOK(w, map[string]any{"ok": true})
}

// Login validates credentials, issues a signed session cookie on success.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !h.localAuthEnabled {
		writeErr(w, http.StatusForbidden, "local login is disabled")
		return
	}
	ctx := r.Context()
	ip := clientIP(r)
	if !h.limiter.Allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts — try again later")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := h.users.GetByUsername(ctx, strings.TrimSpace(req.Username))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// Always run a password verification, even when the username does not
	// exist, so a missing user and a wrong password take the same time. If we
	// skipped the KDF for u == nil, the argon2 cost (tens of ms) would only be
	// paid for real usernames and its presence/absence would leak which
	// usernames exist. See auth.DummyPasswordHash.
	hash := auth.DummyPasswordHash()
	if u != nil {
		hash = u.PasswordHash
	}
	// Reserve the attempt before the KDF runs. Allow above is only a cheap
	// early refusal: in a concurrent burst every request passes it before
	// any failure is recorded, so it cannot bound how many verifications
	// run. Acquire counts and checks in one step, and the reservation stands
	// as a failure unless Reset clears it on success. It is taken before
	// waiting for a KDF slot, so queued requests are already counted.
	if !h.limiter.Acquire(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts — try again later")
		return
	}
	ok, err := h.verifyPassword(ctx, req.Password, hash)
	if err != nil {
		// The client went away while waiting for a KDF slot. Nothing was
		// verified, so refund the reservation: abandoned requests must not
		// lock the address out.
		h.limiter.Release(ip)
		writeErr(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	if u == nil || !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.limiter.Reset(ip)
	if !h.issueSession(w, r, ctx, u.ID, req.RememberMe) {
		return
	}
	writeOK(w, map[string]any{"ok": true, "username": u.Username})
}

// Logout signs out the presented session: it records the token on the
// server side denylist, so a copy of the cookie stops working too, and clears
// the browser's cookie. Only this one token is revoked; the user's sessions on
// other devices are untouched (a password change is what ends all of them).
//
// The route is reachable unauthenticated, so the cookie's signature is
// verified before anything is written: an unsigned or expired value is just
// cleared, and the denylist only ever holds tokens this server issued.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" && h.revocations != nil {
		ctx := r.Context()
		if _, exp, err := auth.VerifySessionMultiWithExpiry(h.sessionSecrets(ctx), c.Value); err == nil {
			if err := h.revocations.Revoke(ctx, auth.SessionTokenHash(c.Value), exp); err != nil {
				// Do not report success for a logout that left the token
				// valid; the client can retry.
				writeServerError(w, r, err)
				return
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeOK(w, map[string]any{"ok": true})
}

// ChangePassword updates the authenticated user's password.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := auth.UserIDFromContext(ctx)
	if uid == 0 {
		// Might happen under ModeDisabled / ModeLocalOnly. In those cases we
		// fall back to "only one user exists" and act on that user.
		count, _ := h.users.Count(ctx)
		if count != 1 {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
	}
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "new password must be ≥ 8 chars")
		return
	}
	var u *db.User
	if uid != 0 {
		u, _ = h.users.GetByID(ctx, uid)
	} else {
		// Single-user bypass: fetch the only row.
		if list, err := h.listAllUsers(ctx); err == nil && len(list) == 1 {
			u = list[0]
		}
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "user not found")
		return
	}
	if !auth.VerifyPassword(req.CurrentPassword, u.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "current password incorrect")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// UpdatePassword atomically bumps users.session_epoch, which invalidates
	// every existing session cookie for this user — including the one the
	// caller just used to authenticate. Re-issue a fresh cookie carrying the
	// new epoch so the browser that performed the change is not immediately
	// bounced to the login screen (Wave 1 / Bundle C audit finding). Other
	// browsers / devices / stolen cookies still hold the pre-bump epoch and
	// are correctly evicted.
	if err := h.users.UpdatePassword(ctx, u.ID, hash); err != nil {
		writeServerError(w, r, err)
		return
	}
	// Re-mint only when the change is happening on behalf of a logged-in
	// caller. The single-user-fallback path (uid==0) has no cookie to
	// preserve, so skip cookie issuance there.
	if uid != 0 {
		if !h.issueSession(w, r, ctx, u.ID, true) {
			return
		}
	}
	writeOK(w, map[string]any{"ok": true})
}

// GetConfig returns the current auth config (mode, current API key, username).
// Requires authentication via the surrounding middleware.
func (h *AuthHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := authConfigResponse{
		Mode: string(h.mode(ctx)),
	}
	// Only expose the API key to admin users — it grants full access. As
	// defence in depth behind auth.Middleware, a requester never receives it,
	// whatever role the context carries: the key would be a way out of the
	// requester role. Admin and user sessions are unchanged, including a user
	// session local-only mode stamps admin, which can regenerate the key and
	// so must be able to see it.
	canSeeKey := auth.UserRoleFromContext(ctx) == auth.RoleAdmin
	if uid := auth.UserIDFromContext(ctx); uid != 0 {
		u, _ := h.users.GetByID(ctx, uid)
		if u != nil {
			resp.Username = u.Username
			if u.Role == auth.RoleRequester {
				canSeeKey = false
			}
		}
	} else if list, err := h.listAllUsers(ctx); err == nil && len(list) == 1 {
		resp.Username = list[0].Username
	}
	if canSeeKey {
		resp.APIKey = h.apiKey(ctx)
	}
	writeOK(w, resp)
}

// RegenerateAPIKey rolls the key and returns the new value.
func (h *AuthHandler) RegenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key, err := auth.RandomHex(32)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if err := h.settings.Set(ctx, SettingAuthAPIKey, key); err != nil {
		writeServerError(w, r, err)
		return
	}
	slog.Info("api key regenerated")
	writeOK(w, map[string]any{"apiKey": key})
}

// RotateSessionSecret rotates the session signing secret. The current secret is
// moved into auth.session_secret_previous and a fresh 32-byte secret becomes
// the new current; both keys are persisted atomically via SettingsRepo.SetMany
// so a verifier never observes a half-applied rotation.
//
// After rotation: new logins (and re-issued CSRF tokens) sign with the new
// secret, while existing sessions remain valid because VerifySessionMulti is
// handed {current, previous}. The window closes on the next rotation, which
// overwrites the previous slot — at which point cookies signed under the
// twice-rotated-out secret stop verifying and those users must re-login.
//
// Admin-only: this route is mounted inside the RequireAdmin group in
// cmd/bindery/main.go alongside /auth/apikey/regenerate.
func (h *AuthHandler) RotateSessionSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// The new current secret. RandomBase64(32) yields >= 32 bytes of base64
	// text, comfortably clearing the minSecretLen fail-closed guard.
	newSecret, err := auth.RandomBase64(32)
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	// The current secret becomes the previous one. If it is somehow absent
	// (should not happen — bootstrapAuth seeds it before serving), we simply
	// rotate forward without a fallback window rather than fail.
	cur := h.sessionSecret(ctx)

	if err := h.settings.SetMany(ctx, []db.SettingKV{
		{Key: SettingAuthSessionSecretPrevious, Value: string(cur)},
		{Key: SettingAuthSessionSecret, Value: newSecret},
	}); err != nil {
		writeServerError(w, r, err)
		return
	}
	slog.Info("session signing secret rotated")
	writeOK(w, map[string]any{"ok": true})
}

// CSRF issues (or re-issues) a double-submit CSRF token bound to the caller's
// session cookie. The token is returned as JSON and also set as a readable
// (non-HttpOnly) cookie so the frontend JS can read it. Unauthenticated
// callers receive a 200 with an empty token — they have no session to bind to,
// so the cookie-absent check in RequireCSRFToken will reject mutations anyway.
func (h *AuthHandler) CSRF(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		writeOK(w, map[string]any{"csrfToken": ""})
		return
	}
	secret := h.sessionSecret(ctx)
	token := auth.MakeCSRFToken(secret, c.Value)

	var secure bool
	switch CookieSecureMode() {
	case "always":
		secure = true
	case "never":
		secure = false
	default:
		secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	}

	// Readable (no HttpOnly) so JS can access it.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	writeOK(w, map[string]any{"csrfToken": token})
}

// SetMode persists the auth mode setting.
func (h *AuthHandler) SetMode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req modeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	mode := auth.ParseMode(req.Mode)
	if err := h.settings.Set(ctx, SettingAuthMode, string(mode)); err != nil {
		writeServerError(w, r, err)
		return
	}
	slog.Info("auth mode changed", "mode", mode)

	// Switching into local-only with no trusted proxy configured is the same
	// hazard the boot path warns about, so warn here too: the mode can be
	// changed long after startup, and the operator changing it is the person
	// who needs to see it. The CIDR list is re-read from the environment rather
	// than taken from the cached parse so the check reflects the process's
	// current configuration.
	resp := map[string]any{"mode": string(mode)}
	trusted := auth.ParseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))
	if auth.WarnIfLocalOnlyWithoutTrustedProxy(mode, trusted) {
		// Additive field: existing clients ignore it, and the Settings UI can
		// surface the same text the log carries.
		resp["warning"] = auth.LocalOnlyNoTrustedProxyWarning
	}
	writeOK(w, resp)
}

// --- helpers -----------------------------------------------------------------

func (h *AuthHandler) mode(ctx context.Context) auth.Mode {
	return authModeFor(ctx, h.settings)
}

func authModeFor(ctx context.Context, settings *db.SettingsRepo) auth.Mode {
	if settings == nil {
		return auth.ModeEnabled
	}
	s, _ := settings.Get(ctx, SettingAuthMode)
	if s == nil {
		return auth.ModeEnabled
	}
	return auth.ParseMode(s.Value)
}

// requestHasAdminSemantics reports whether r acts as an admin: either the
// middleware stamped the role, or the auth mode admits the caller as the
// install's admin. The second half is auth.ModeGrantsAdmin, the rule the
// middleware itself stamps from, so a route the middleware lets through before
// its mode branches (GET /auth/status) cannot promise the UI an admin that
// RequireAdmin then refuses.
func requestHasAdminSemantics(r *http.Request, settings *db.SettingsRepo) bool {
	if auth.UserRoleFromContext(r.Context()) == "admin" {
		return true
	}
	return auth.ModeGrantsAdmin(authModeFor(r.Context(), settings), r, auth.EnvTrustedProxyCIDRs())
}

func (h *AuthHandler) apiKey(ctx context.Context) string {
	s, _ := h.settings.Get(ctx, SettingAuthAPIKey)
	if s == nil {
		return ""
	}
	return s.Value
}

func (h *AuthHandler) sessionSecret(ctx context.Context) []byte {
	s, _ := h.settings.Get(ctx, SettingAuthSessionSecret)
	if s == nil {
		return nil
	}
	// Secret is stored as base64; hand back raw bytes. Accept anything on
	// decode failure (the seed path writes valid base64).
	return []byte(s.Value)
}

// sessionSecrets returns the ordered verification candidate set
// {current, previous}. The previous entry is included only when it is set and
// non-empty; when absent the result is the single-element {current} slice and
// verification behaves exactly as it did before rotation existed. The
// minimum-length fail-closed guard is applied downstream by VerifySessionMulti
// for every entry, so a short previous secret cannot weaken verification.
func (h *AuthHandler) sessionSecrets(ctx context.Context) [][]byte {
	secrets := [][]byte{h.sessionSecret(ctx)}
	if s, _ := h.settings.Get(ctx, SettingAuthSessionSecretPrevious); s != nil && s.Value != "" {
		secrets = append(secrets, []byte(s.Value))
	}
	return secrets
}

// issueSession signs and sets the session cookie. It returns true on success.
// On failure it writes a 500 error response and returns false — callers must
// return immediately without writing any further response.
//
// The cookie carries the user's current session_epoch (looked up here). The
// middleware compares that field against the live column on every request and
// rejects mismatches, which is how UpdatePassword's epoch bump invalidates
// every outstanding cookie for that user (Wave 1 / Bundle C audit finding).
//
// Order matters at the password-change call site: epoch must be bumped FIRST,
// the new cookie minted SECOND. issueSession reads the post-bump epoch here,
// so a caller that mints the cookie before bumping would invalidate it
// instantly.
func (h *AuthHandler) issueSession(w http.ResponseWriter, r *http.Request, ctx context.Context, userID int64, rememberMe bool) bool {
	dur := auth.SessionDurationShort
	if rememberMe {
		dur = auth.SessionDuration
	}
	exp := time.Now().Add(dur)
	epoch, err := h.users.GetSessionEpoch(ctx, userID)
	if err != nil {
		writeServerError(w, r, err)
		return false
	}
	value, err := auth.SignSessionWithEpoch(h.sessionSecret(ctx), userID, epoch, exp)
	if err != nil {
		// Secret is absent or too short — fail closed rather than issue a
		// forgeable token. This should never happen in practice because
		// bootstrapAuth seeds a 32-byte secret before any requests are served,
		// and the server exits if bootstrapAuth fails.
		writeErr(w, http.StatusInternalServerError, "session signing unavailable")
		return false
	}

	var secure bool
	switch CookieSecureMode() {
	case "always":
		secure = true
	case "never":
		secure = false
	default: // "auto"
		secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	}

	cookie := &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(dur.Seconds()),
	}
	if rememberMe {
		cookie.Expires = exp
	}
	http.SetCookie(w, cookie)
	return true
}

func (h *AuthHandler) listAllUsers(ctx context.Context) ([]*db.User, error) {
	// Minimal helper — users repo doesn't need a full List today. Count + any
	// known usernames would be an alternative; we only need this to recover the
	// single-user identity under the local-bypass path.
	u, err := h.users.GetByUsername(ctx, "")
	_ = u
	_ = err
	// Fall back to fetching by id=1 (first-run inserts always produce id=1).
	if first, err := h.users.GetByID(ctx, 1); err == nil && first != nil {
		return []*db.User{first}, nil
	}
	return nil, nil
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// writeOK is the ok/created JSON writer used by the auth endpoints. The
// shared writeJSON(w, status, v) in search.go is the general-purpose one;
// this wrapper just matches the status=200 convention and the error writer.
func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
