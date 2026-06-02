package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

// EnforceTenancyEnv is the environment variable that gates per-user resource
// scoping on Tier-2 join-scoped resources (queue/history/pending/OPDS). It
// defaults off so existing single-user installs and tests are unaffected;
// flipping it on at startup is the deploy-time switch that turns
// CheckOwnership from a no-op into a real check.
const EnforceTenancyEnv = "BINDERY_ENFORCE_TENANCY"

// EnforceTenancy reports whether the operator has opted into per-user resource
// scoping. Implemented as an env-on-call read (no caching) so t.Setenv-driven
// tests can flip the gate between cases without a separate seam. Values "1",
// "true", "yes", "on" (case insensitive) flip the gate on; anything else
// (including empty) leaves it off, matching the single-user default.
//
// The per-call os.Getenv cost is negligible compared to the SQL the rest of
// the handler runs.
func EnforceTenancy() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnforceTenancyEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// SetEnforceTenancyForTests forces the tenancy gate on or off for the duration
// of a single test by setting the env var via t.Setenv. The previous value is
// restored automatically by t.Setenv's cleanup hook so test order does not
// matter. This is the test seam D1's regression suite uses; D3's tests use
// t.Setenv directly, which has the same effect.
func SetEnforceTenancyForTests(t *testing.T, on bool) {
	t.Helper()
	if on {
		t.Setenv(EnforceTenancyEnv, "true")
	} else {
		t.Setenv(EnforceTenancyEnv, "")
	}
}

// CheckOwnership returns true when the request context's user owns
// ownerUserID. When EnforceTenancy() is false the check is a no-op (true),
// which preserves pre-multiuser behaviour for installs that have not opted in.
//
// When the gate is on:
//   - admin users always pass (matches existing RequireAdmin semantics — admins
//     manage every user's library);
//   - userID == 0 means there is no authenticated user (API key / disabled /
//     local-only mode), so the request is treated as admin-equivalent and
//     allowed through to preserve pre-gate behaviour for those auth modes;
//   - ownerUserID == 0 means the row has no owner (pre-migration-025 data),
//     and we also pass to avoid hiding legacy rows from their actual creator.
//
// The argument intentionally takes an int64 not a *int64 — callers must
// decide how a nil owner maps (typically 0). Pass 0 for "unowned".
func CheckOwnership(ctx context.Context, ownerUserID int64) bool {
	if !EnforceTenancy() {
		return true
	}
	if UserRoleFromContext(ctx) == "admin" {
		return true
	}
	uid := UserIDFromContext(ctx)
	if uid == 0 {
		// API-key / disabled / local-only requests carry no user identity.
		// Treat them as admin-equivalent so machine-to-machine integrations
		// (Harpoon, *arr-style callers) keep working post-gate.
		return true
	}
	if ownerUserID == 0 {
		// Row predates migration 025's backfill or was created without an
		// owner. Don't block the only auth'd user from seeing it.
		return true
	}
	return uid == ownerUserID
}

// WithUserID returns a context carrying the given user id. Exported so the
// OPDS handler can attach the basic-auth user id to ctx after verifying
// credentials — the standard cookie/proxy paths set this inside Middleware.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDCtxKey, userID)
}

// Mode represents the auth posture. Matches Sonarr's "Authentication Required"
// dropdown semantics.
type Mode string

const (
	ModeDisabled  Mode = "disabled"   // no auth check at all (not recommended; not the default)
	ModeLocalOnly Mode = "local-only" // RFC1918 + loopback clients bypass
	ModeEnabled   Mode = "enabled"    // everyone must authenticate
	ModeProxy     Mode = "proxy"      // trust identity header from a configured upstream proxy
)

// ParseMode coerces a free-form string into a valid Mode; unknown values map
// to ModeEnabled (fail-safe).
func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeDisabled, ModeLocalOnly, ModeEnabled, ModeProxy:
		return Mode(s)
	default:
		return ModeEnabled
	}
}

type ctxKey string

const (
	userIDCtxKey   ctxKey = "auth.user_id"
	userRoleCtxKey ctxKey = "auth.user_role"
	// viaAPIKeyCtxKey marks a request whose identity was established by a
	// *verified* API key (subtle.ConstantTimeCompare passed in Middleware).
	// The CSRF and X-Requested-With guards consult this flag to decide whether
	// to exempt the request — never the mere presence of an apikey parameter,
	// which an attacker can forge to switch the CSRF layer off (#708).
	viaAPIKeyCtxKey ctxKey = "auth.via_api_key" //nolint:gosec // context key name, not a credential
)

// AuthedViaAPIKey reports whether the request was authenticated by a verified
// API key. False for session-cookie, proxy, local-only, or disabled-mode
// requests — and false for requests carrying a *bogus* apikey parameter that
// failed key verification and fell through to cookie auth.
func AuthedViaAPIKey(ctx context.Context) bool {
	v, _ := ctx.Value(viaAPIKeyCtxKey).(bool)
	return v
}

// WithAPIKeyAuth returns a context marked as authenticated via a verified API
// key. Exported only so tests can construct the same state Middleware sets.
func WithAPIKeyAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, viaAPIKeyCtxKey, true)
}

// UserIDFromContext returns the authenticated user ID (0 if unauthenticated).
func UserIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(userIDCtxKey).(int64)
	return v
}

// UserRoleFromContext returns the authenticated user's role ("admin", "user", or "").
func UserRoleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userRoleCtxKey).(string)
	return v
}

// WithUserRole returns a context carrying the given role alongside the user id.
func WithUserRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, userRoleCtxKey, role)
}

// RequireAdmin is a middleware that rejects non-admin requests with 403.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserRoleFromContext(r.Context()) != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"admin role required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UserProvisioner resolves or creates a user by username. Used by proxy-auth.
type UserProvisioner interface {
	// ResolveOrProvisionUser returns the user ID for username, creating one if
	// autoProvision is true and the user does not yet exist. Returns 0, nil when
	// autoProvision is false and the user is not found.
	ResolveOrProvisionUser(ctx context.Context, username string, autoProvision bool) (int64, error)
}

// Provider is the data the middleware needs at request time. Implemented by
// main.go via a small adapter; keeps this package free of db imports.
type Provider interface {
	Mode() Mode
	APIKey() string
	// SessionSecret returns the current session signing secret — the secret
	// SignSession must always use when minting a new cookie.
	SessionSecret() []byte
	// SessionSecrets returns the ordered candidate set used for *verification*:
	// {current, previous}. During a secret rotation a cookie signed under the
	// just-rotated-out secret still verifies. When no previous secret is
	// configured this is a one-element slice and behavior matches single-secret
	// verification. Empty/too-short secrets are filtered downstream by
	// VerifySessionMulti, which fails closed if none remain.
	SessionSecrets() [][]byte
	// SetupRequired reports whether no user exists yet (first-run). When true
	// and the request is unauthenticated, /setup endpoints are allowed through.
	SetupRequired() bool
	// ProxyAuthHeader is the HTTP header carrying the upstream identity, e.g.
	// "X-Forwarded-User". Only consulted when Mode() == ModeProxy.
	ProxyAuthHeader() string
	// ProxyAutoProvision controls whether unknown usernames are created on the
	// fly when Mode() == ModeProxy.
	ProxyAutoProvision() bool
	// TrustedProxyCIDRs returns the parsed CIDR list for proxy-mode trust
	// decisions. Callers must not mutate the returned slice.
	TrustedProxyCIDRs() []*net.IPNet
	// UserProvisioner returns the provisioner used in proxy-auth mode.
	UserProvisioner() UserProvisioner
	// UserRole returns the role string ("admin" or "user") for the given user
	// id. Returns "" if the user is not found or an error occurs.
	UserRole(ctx context.Context, userID int64) string
	// UserSessionEpoch returns the user's current session epoch, the value
	// the cookie's epoch field must match for the cookie to authenticate.
	// Bumped on password change so old cookies stop verifying. Returns 0 if
	// the user does not exist or the lookup fails — the comparison below then
	// fails closed for any cookie minted after the 047 migration (which sets
	// session_epoch to >= 1 by default).
	UserSessionEpoch(ctx context.Context, userID int64) int64
}

// AllowUnauthPath returns true for routes the middleware must always let
// through, regardless of auth state (health probes, auth endpoints themselves).
func AllowUnauthPath(path string) bool {
	switch path {
	case "/api/v1/health",
		"/api/v1/auth/status",
		"/api/v1/auth/login",
		"/api/v1/auth/logout",
		"/api/v1/auth/setup",
		"/api/v1/auth/csrf",
		"/api/v1/auth/oidc/providers":
		return true
	}
	// OIDC login + callback paths are public — the IdP redirect happens before
	// the user holds a Bindery session.
	if strings.HasPrefix(path, "/api/v1/auth/oidc/") &&
		(strings.HasSuffix(path, "/login") || strings.HasSuffix(path, "/callback")) {
		return true
	}
	return false
}

// Middleware returns the composite auth checker. Precedence per request:
//
//  1. Always try to resolve identity from a valid session cookie, so handlers
//     on unauth-allowed paths (e.g. /auth/status) can still see who's logged in.
//  2. In proxy mode, also try to resolve identity from the configured proxy
//     header (gated by trusted-proxy CIDR), so /auth/status reports the
//     proxy-authed user instead of always returning authenticated:false (#560).
//  3. Health / auth endpoints — always allowed through
//  4. Mode == disabled            — always allowed
//  5. Mode == local-only + local  — always allowed
//  6. Valid X-Api-Key header or ?apikey= query — allowed
//  7. Valid signed session cookie — allowed
//  8. Mode == proxy: trusted peer IP + identity header → resolve/provision user
//  9. Otherwise                   — 401
func Middleware(p Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Resolve identity up-front regardless of path. A successful
			// cookie verification attaches the user id to ctx so unauth-path
			// handlers (like /auth/status) can report "authenticated: true".
			ctx := r.Context()
			cookieValid := false
			if c, err := r.Cookie(SessionCookieName); err == nil {
				if uid, epoch, err := VerifySessionMultiWithEpoch(p.SessionSecrets(), c.Value); err == nil {
					// Compare the cookie's epoch field against the user's
					// current session_epoch (bumped on password change). A
					// mismatch means the cookie pre-dates the most recent
					// credential rotation and must be rejected, even though
					// the signature and expiry are otherwise valid — this is
					// the "log everyone out after a password change" check
					// (Wave 1 / Bundle C audit finding). Pre-047-migration
					// cookies decode as epoch=0; the migration default of 1
					// makes them all fail here on upgrade, which is the
					// deliberate forced-logout-on-upgrade behaviour.
					if p.UserSessionEpoch(ctx, uid) == epoch {
						ctx = context.WithValue(ctx, userIDCtxKey, uid)
						ctx = context.WithValue(ctx, userRoleCtxKey, p.UserRole(ctx, uid))
						cookieValid = true
					}
				}
			}

			// In proxy mode, resolve the upstream identity header up-front as
			// well — gated by the trusted-proxy CIDR check inside
			// resolveProxyIdentity. This must run before the AllowUnauthPath
			// short-circuit so /auth/status (which is in that list) sees the
			// authenticated user (#560). Untrusted sources are rejected inside
			// resolveProxyIdentity, so a spoofed header from a public IP still
			// returns (0, false) and we drop through unchanged.
			mode := p.Mode()
			proxyValid := false
			if !cookieValid && mode == ModeProxy {
				if uid, ok := resolveProxyIdentity(r, p); ok {
					ctx = context.WithValue(ctx, userIDCtxKey, uid)
					ctx = context.WithValue(ctx, userRoleCtxKey, p.UserRole(ctx, uid))
					proxyValid = true
				}
			}
			r = r.WithContext(ctx)

			if AllowUnauthPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if mode == ModeDisabled {
				next.ServeHTTP(w, r)
				return
			}
			if mode == ModeLocalOnly && IsLocalRequestTrusted(r, p.TrustedProxyCIDRs()) {
				// Local-only bypass is always treated as admin, mirroring the
				// API-key branch below. Without this, RequireAdmin-protected
				// endpoints (auth mode change, user CRUD, settings writes)
				// return "admin role required" 403 to trusted-local requests
				// even though the whole point of local-only mode is to grant
				// frictionless access from a trusted private network (#799).
				ctx := context.WithValue(r.Context(), userRoleCtxKey, "admin")
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			if key := requestAPIKey(r); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(p.APIKey())) == 1 {
				// API key authentication is always treated as admin. Set the role
				// so RequireAdmin-protected endpoints are accessible without a
				// session cookie (Bug 11: misleading "admin role required" 403).
				ctx := context.WithValue(r.Context(), userRoleCtxKey, "admin")
				// Mark the request as API-key-authenticated. The CSRF and
				// X-Requested-With guards downstream key their exemption off
				// this verified flag, not the presence of an apikey parameter
				// (#708 finding 3). A request reaches this branch only after
				// subtle.ConstantTimeCompare confirmed the key.
				ctx = context.WithValue(ctx, viaAPIKeyCtxKey, true)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			if cookieValid {
				next.ServeHTTP(w, r)
				return
			}

			if mode == ModeProxy {
				if proxyValid {
					// Identity already attached above; just continue.
					next.ServeHTTP(w, r)
					return
				}
				// Proxy mode — identity header present but source untrusted, or
				// no header at all.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			// First-run escape hatch: before any user exists, the UI needs to
			// reach /auth/setup without credentials. Those paths are already in
			// AllowUnauthPath — any other path still 401s so random GETs can't
			// leak data pre-setup.
			_ = p.SetupRequired()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			if _, err := w.Write([]byte(`{"error":"unauthorized"}`)); err != nil {
				slog.Warn("failed to write unauthorized response", "error", err)
			}
		})
	}
}

// resolveProxyIdentity checks whether the request carries a trusted upstream
// identity header from a configured proxy IP. Returns (userID, true) on
// success. Returns (0, false) when the header is missing or the source is
// untrusted. A forged header from an untrusted IP is logged and rejected.
func resolveProxyIdentity(r *http.Request, p Provider) (int64, bool) {
	header := p.ProxyAuthHeader()
	username := strings.TrimSpace(r.Header.Get(header))

	peerIP := requestPeerIP(r)

	trusted := isTrustedProxy(peerIP, p.TrustedProxyCIDRs())

	if username != "" && !trusted {
		slog.Warn("proxy auth: identity header from untrusted source — rejecting",
			"header", header, "peer", peerIP)
		return 0, false
	}
	if !trusted || username == "" {
		return 0, false
	}

	uid, err := p.UserProvisioner().ResolveOrProvisionUser(r.Context(), username, p.ProxyAutoProvision())
	if err != nil {
		slog.Error("proxy auth: user provisioning failed", "username", username, "error", err)
		return 0, false
	}
	if uid == 0 {
		slog.Warn("proxy auth: user not found and auto-provisioning disabled", "username", username)
		return 0, false
	}
	return uid, true
}

func isTrustedProxy(ip net.IP, cidrs []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func requestPeerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// RequireXRequestedWith rejects non-GET/HEAD requests that lack the custom
// CSRF header. Browsers cannot set this header in cross-site requests, so a
// CSRF attacker cannot cause a mutating request to be accepted even if the
// session cookie rides along via SameSite=Lax.
//
// API-key-authenticated requests are exempt: CSRF requires a cookie to be the
// authentication mechanism, so requests carrying an explicit API key are not
// vulnerable and do not need the header. The exemption keys off the
// AuthedViaAPIKey context flag, which Middleware sets only after the key has
// been *verified* — a bogus ?apikey= parameter no longer disables the check
// (#708 finding 3). For this to work, auth.Middleware MUST run before this
// middleware in the chain (it does — see cmd/bindery/main.go).
func RequireXRequestedWith(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// safe methods — pass through
		default:
			// Auth endpoints (login, setup, logout…) are exempt: there is no
			// session cookie to protect against CSRF at those points, so
			// requiring the header is pure friction for non-browser clients.
			// This mirrors the identical exemption in RequireCSRFToken.
			if !AuthedViaAPIKey(r.Context()) && !AllowUnauthPath(r.URL.Path) && r.Header.Get("X-Requested-With") != "bindery-ui" {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// safeMethod reports whether the HTTP method is non-mutating (read-only).
// The ?apikey= query parameter is honoured only for these methods.
func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// requestAPIKey extracts the API key supplied with the request.
//
// The X-Api-Key header is always honoured. The ?apikey= query parameter is
// honoured ONLY for safe (read-only) methods: a key in the URL leaks into
// proxy access logs, browser history, and Referer headers, so it must not be
// usable to authorise a state-changing POST/PUT/DELETE/PATCH. Mutations must
// send the key in the header instead (#708 finding 4a). All documented client
// workflows (curl examples, OPDS readers, integrations) already use the header
// for mutations or the query param only for GET, so this does not break them.
func requestAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	if safeMethod(r.Method) {
		return r.URL.Query().Get("apikey")
	}
	return ""
}
