package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
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
	SettingAuthMode          = "auth.mode"
	SettingOIDCProviders     = "auth.oidc.providers" //nolint:gosec // setting key name, not a credential
)

// AuthHandler owns the login / setup / password / mode endpoints.
type AuthHandler struct {
	users    *db.UserRepo
	settings *db.SettingsRepo
	limiter  *auth.LoginLimiter
}

func NewAuthHandler(users *db.UserRepo, settings *db.SettingsRepo, limiter *auth.LoginLimiter) *AuthHandler {
	return &AuthHandler{users: users, settings: settings, limiter: limiter}
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
	Authenticated bool   `json:"authenticated"`
	SetupRequired bool   `json:"setupRequired"`
	Username      string `json:"username,omitempty"`
	Mode          string `json:"mode"`
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
	count, err := h.users.Count(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "status: "+err.Error())
		return
	}
	mode := h.mode(ctx)

	resp := statusResponse{
		SetupRequired: count == 0,
		Mode:          string(mode),
	}

	if uid := auth.UserIDFromContext(ctx); uid != 0 {
		if u, _ := h.users.GetByID(ctx, uid); u != nil {
			resp.Authenticated = true
			resp.Username = u.Username
		}
	} else if mode == auth.ModeDisabled || (mode == auth.ModeLocalOnly && auth.IsLocalRequest(r)) {
		// Trusted bypass — the UI should render normally without a login screen.
		resp.Authenticated = true
	}

	writeOK(w, resp)
}

// Setup creates the first admin user. Only allowed while no user exists.
func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	count, err := h.users.Count(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "setup: "+err.Error())
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
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash: "+err.Error())
		return
	}
	u, err := h.users.Create(ctx, req.Username, hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create user: "+err.Error())
		return
	}
	// Log the user in immediately.
	h.issueSession(w, r, ctx, u.ID, true)
	slog.Info("first-run setup complete", "username", u.Username)
	writeOK(w, map[string]any{"ok": true})
}

// Login validates credentials, issues a signed session cookie on success.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusInternalServerError, "lookup: "+err.Error())
		return
	}
	if u == nil || !auth.VerifyPassword(req.Password, u.PasswordHash) {
		h.limiter.Record(ip)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.limiter.Reset(ip)
	h.issueSession(w, r, ctx, u.ID, req.RememberMe)
	writeOK(w, map[string]any{"ok": true, "username": u.Username})
}

// Logout clears the session cookie. Always succeeds.
func (h *AuthHandler) Logout(w http.ResponseWriter, _ *http.Request) {
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
		writeErr(w, http.StatusInternalServerError, "hash: "+err.Error())
		return
	}
	if err := h.users.UpdatePassword(ctx, u.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "update: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// GetConfig returns the current auth config (mode, current API key, username).
// Requires authentication via the surrounding middleware.
func (h *AuthHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := authConfigResponse{
		Mode:   string(h.mode(ctx)),
		APIKey: h.apiKey(ctx),
	}
	if uid := auth.UserIDFromContext(ctx); uid != 0 {
		if u, _ := h.users.GetByID(ctx, uid); u != nil {
			resp.Username = u.Username
		}
	} else if list, err := h.listAllUsers(ctx); err == nil && len(list) == 1 {
		resp.Username = list[0].Username
	}
	writeOK(w, resp)
}

// RegenerateAPIKey rolls the key and returns the new value.
func (h *AuthHandler) RegenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key, err := auth.RandomHex(32)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gen: "+err.Error())
		return
	}
	if err := h.settings.Set(ctx, SettingAuthAPIKey, key); err != nil {
		writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	slog.Info("api key regenerated")
	writeOK(w, map[string]any{"apiKey": key})
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
		writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	slog.Info("auth mode changed", "mode", mode)
	writeOK(w, map[string]any{"mode": string(mode)})
}

// --- helpers -----------------------------------------------------------------

func (h *AuthHandler) mode(ctx context.Context) auth.Mode {
	s, _ := h.settings.Get(ctx, SettingAuthMode)
	if s == nil {
		return auth.ModeEnabled
	}
	return auth.ParseMode(s.Value)
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

func (h *AuthHandler) issueSession(w http.ResponseWriter, r *http.Request, ctx context.Context, userID int64, rememberMe bool) {
	dur := auth.SessionDurationShort
	if rememberMe {
		dur = auth.SessionDuration
	}
	exp := time.Now().Add(dur)
	value := auth.SignSession(h.sessionSecret(ctx), userID, exp)

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
	}
	if rememberMe {
		cookie.Expires = exp
	}
	http.SetCookie(w, cookie)
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
