package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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
//   - userID == 0 means there is no authenticated user. Requests the
//     middleware admits as the install (API key, disabled mode, local-only)
//     carry the admin role and are already allowed above; this covers
//     contexts built outside the middleware, which keep their pre-gate
//     admin-equivalent treatment;
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
		// No identity at all: a context built outside the middleware (the
		// middleware gives API-key, disabled and local-only requests the
		// admin role, handled above). Treat it as admin-equivalent so
		// machine-to-machine callers keep working post-gate.
		return true
	}
	if ownerUserID == 0 {
		// Row predates migration 025's backfill or was created without an
		// owner. Don't block the only auth'd user from seeing it.
		return true
	}
	return uid == ownerUserID
}

// ListScopeUserID returns the owner_user_id a LIST/browse query should scope
// to, or 0 for "unscoped / see everything". It mirrors CheckOwnership's
// bypasses so list views stay consistent with per-item access:
//
//   - tenancy disabled  => 0 (no scoping; single-user default);
//   - admin role        => 0 (admins manage every user's library, matching
//     CheckOwnership, which already lets an admin open any item by ID);
//   - otherwise         => the caller's user id, which the DB layer treats as
//     "owner_user_id = id OR owner_user_id IS NULL". API-key, disabled and
//     local-only requests never get here: the middleware gives them the
//     admin role. A 0 from a context with no identity still means unscoped
//     downstream, matching CheckOwnership.
//
// Use this — not UserIDFromContext — to scope owner-filtered list handlers so
// admins see the shared library and non-admins stay isolated to their own
// rows (plus unowned/global rows).
func ListScopeUserID(ctx context.Context) int64 {
	if !EnforceTenancy() {
		return 0
	}
	if UserRoleFromContext(ctx) == "admin" {
		return 0
	}
	return UserIDFromContext(ctx)
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

// operatorWarnOnce keeps a missing-operator install from logging on every
// request; the condition is process-lifetime, not per-request.
var operatorWarnOnce sync.Once

// withOperatorUserID stamps the install's operator identity on a request that
// authenticated as the install rather than as a person.
//
// Without it UserIDFromContext returns 0 for these requests, so every handler
// that persists per-user rows wrote them under a user that does not exist —
// invisible to the same operator reading through a session, and never matched
// by dismissals or scoping queries (#1725). An existing identity is never
// overwritten: the proxy-auth branch above may already have resolved a real
// user, and that is the more specific answer.
func withOperatorUserID(ctx context.Context, p Provider) context.Context {
	if UserIDFromContext(ctx) != 0 {
		return ctx
	}
	id := p.OperatorUserID(ctx)
	if id == 0 {
		operatorWarnOnce.Do(func() {
			slog.Warn("auth: no admin user to attribute install-authenticated requests to; " +
				"per-user data from disabled-mode, local-only and API-key requests stays unattributed")
		})
		return ctx
	}
	return context.WithValue(ctx, userIDCtxKey, id)
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

// WithUserID returns a context carrying the authenticated user ID. Exported so
// OPDS handlers and tests can attach a verified user without a full auth
// middleware stack.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDCtxKey, userID)
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
	// OperatorUserID returns the user id that requests authenticating as the
	// install itself — trusted local-only requests and API-key requests — act
	// as. Both are already treated as admin; without an id they were also
	// anonymous, so anything persisting per-user data wrote it under user 0
	// while the scheduler wrote under a different id (#1725). Returns 0 when
	// no admin exists, which callers treat as "unknown" and leave the context
	// unstamped rather than inventing an identity.
	OperatorUserID(ctx context.Context) int64
	// UserSessionEpoch returns the user's current session epoch, the value
	// the cookie's epoch field must match for the cookie to authenticate.
	// Bumped on password change so old cookies stop verifying.
	//
	// The returned error distinguishes a transient lookup failure (DB blip)
	// from a genuine "user gone / epoch advanced" rejection. On a non-nil
	// error the caller MUST NOT treat the cookie as merely invalid and drop to
	// unauthenticated — that would silently log out a user holding a valid
	// cookie whenever the DB hiccups. Instead the request is failed with a
	// server error so the blip surfaces as a 5xx rather than a spurious logout.
	// When err is nil the int64 is authoritative: a value that differs from the
	// cookie's epoch is a real mismatch (revoked cookie) and the user is logged
	// out exactly as before.
	UserSessionEpoch(ctx context.Context, userID int64) (int64, error)
}

// AllowUnauthPath reports whether the given method+path combination must always
// be let through regardless of auth state (health probes, auth endpoints).
// The method parameter matters: GET /auth/oidc/providers is intentionally
// public so the login page can discover providers, but PUT on the same path
// is an admin mutation that must go through normal auth.
func AllowUnauthPath(method, path string) bool {
	switch path {
	case "/api/v1/health",
		"/api/v1/auth/status",
		"/api/v1/auth/login",
		"/api/v1/auth/logout",
		"/api/v1/auth/setup",
		"/api/v1/auth/csrf":
		return true
	case "/api/v1/auth/oidc/providers":
		// Only the read path is public; mutations are admin-only.
		return method == http.MethodGet || method == http.MethodHead
	}
	// OIDC login + callback paths are public — the IdP redirect happens before
	// the user holds a Bindery session.
	if strings.HasPrefix(path, "/api/v1/auth/oidc/") &&
		(strings.HasSuffix(path, "/login") || strings.HasSuffix(path, "/callback")) {
		return true
	}
	return false
}

// ModeGrantsAdmin reports whether the auth mode on its own admits r as the
// install's admin, with no personal credential: disabled admits everyone,
// local-only admits a client on a private network (resolved through the
// trusted proxy set). The API key is the third install admin grant and is
// checked separately because it also earns the CSRF exemption.
//
// This is the single rule behind both the middleware, which stamps the admin
// role and the operator id when it holds, and GET /auth/status, which has to
// answer the same question on a path the middleware lets through before any
// mode branch runs. Two copies of it drifted once already: disabled mode was
// reported as admin by the status route while the middleware stamped nothing,
// so the UI rendered admin screens that every RequireAdmin route refused.
func ModeGrantsAdmin(mode Mode, r *http.Request, trusted []*net.IPNet) bool {
	switch mode {
	case ModeDisabled:
		return true
	case ModeLocalOnly:
		return IsLocalRequestTrusted(r, trusted)
	default:
		return false
	}
}

// Middleware returns the composite auth checker. Precedence per request:
//
//  1. Always try to resolve identity from a valid session cookie, so handlers
//     on unauth-allowed paths (e.g. /auth/status) can still see who's logged in.
//  2. In proxy mode, also try to resolve identity from the configured proxy
//     header (gated by trusted-proxy CIDR), so /auth/status reports the
//     proxy-authed user instead of always returning authenticated:false (#560).
//  3. Health / auth endpoints: always allowed through
//  4. Valid X-Api-Key header or ?apikey= query: admin, as the operator
//  5. ModeGrantsAdmin (disabled, or local-only and local): admin, as the operator
//  6. Valid signed session cookie: allowed
//  7. Mode == proxy: trusted peer IP + identity header → resolve/provision user
//  8. Otherwise: 401
//
// The API-key check deliberately precedes the mode grant: both grant admin,
// but only the key branch marks the request AuthedViaAPIKey, and the CSRF
// guards downstream key their exemption off that flag. With local-only first,
// a valid-key mutation from a LAN address short-circuited into the bypass,
// never got the flag, and was then rejected 403 by RequireXRequestedWith
// (#1849). Disabled mode used to sit ahead of the key too, with the same
// result for keyed integrations, and without stamping any role at all.
func Middleware(p Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Resolve identity up-front regardless of path. A successful
			// cookie verification attaches the user id to ctx so unauth-path
			// handlers (like /auth/status) can report "authenticated: true".
			ctx := r.Context()
			cookieValid := false
			// epochLookupFailed records a transient DB failure while looking up
			// the user's current session epoch. We cannot prove the cookie's
			// epoch matches, but we also must not treat that as a genuine
			// mismatch and silently log the user out (a DB blip is not a
			// credential revocation). When set, an otherwise-unauthenticated
			// request is answered with 500 instead of 401 below.
			epochLookupFailed := false
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
					curEpoch, epochErr := p.UserSessionEpoch(ctx, uid)
					switch {
					case epochErr != nil:
						// Transient lookup failure — do not authenticate, but do
						// not treat as a revoked cookie either. Flag it so an
						// otherwise-unauthenticated request fails with 500 rather
						// than silently logging the user out on a DB blip.
						slog.Error("session epoch lookup failed", "user_id", uid, "error", epochErr)
						epochLookupFailed = true
					case curEpoch == epoch:
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

			if AllowUnauthPath(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// Checked before the mode grant below: both branches grant admin,
			// so for a caller the mode already admits the only thing that
			// changes is the AuthedViaAPIKey flag, and that flag is what
			// exempts the request from the X-Requested-With / CSRF guards.
			// Running the bypass first meant a valid-key mutation from the LAN
			// (or from anywhere, in disabled mode) never earned the exemption
			// and came back 403 (#1849). An absent or wrong key falls through
			// to the grant exactly as before, so keyless callers are unaffected
			// and still need the header.
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
				ctx = withOperatorUserID(ctx, p)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			if ModeGrantsAdmin(mode, r, p.TrustedProxyCIDRs()) {
				// The mode itself admits the caller (disabled: everyone;
				// local-only: a trusted local client), so the request acts as
				// the install's admin, mirroring the API-key branch above. It is
				// the same rule GET /auth/status reports from, so the admin
				// screens the UI renders are the ones RequireAdmin lets through.
				// Without the role, RequireAdmin-protected endpoints answer
				// "admin role required" 403: first for local-only (#799), and
				// for disabled mode until this branch absorbed it.
				ctx := context.WithValue(r.Context(), userRoleCtxKey, "admin")
				ctx = withOperatorUserID(ctx, p)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			if cookieValid {
				next.ServeHTTP(w, r)
				return
			}

			// A transient DB error prevented us from confirming the cookie's
			// session epoch. The cookie may well be valid — we just couldn't
			// check — so the request would otherwise be rejected as
			// unauthenticated, silently logging the user out on a DB blip. None
			// of the path-based bypasses above applied (those already returned),
			// so surface this as a server error instead of a spurious 401.
			if epochLookupFailed {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				if _, err := w.Write([]byte(`{"error":"internal error"}`)); err != nil {
					slog.Warn("failed to write internal error response", "error", err)
				}
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
			if !AuthedViaAPIKey(r.Context()) && !AllowUnauthPath(r.Method, r.URL.Path) && r.Header.Get("X-Requested-With") != "bindery-ui" {
				// Leave a trace: this rejection was silent at every level, so
				// a 403 here was indistinguishable from an auth failure or a
				// routing mistake (#1895). Only the header's presence is
				// logged, never its value — that is caller-controlled bytes.
				logCSRFRejection("x_requested_with", r,
					"request is not API-key-authenticated and X-Requested-With is not the UI value",
					"x_requested_with_present", r.Header.Get("X-Requested-With") != "")
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
