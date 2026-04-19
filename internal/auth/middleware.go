package auth

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

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

const userIDCtxKey ctxKey = "auth.user_id"

// UserIDFromContext returns the authenticated user ID (0 if unauthenticated).
func UserIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(userIDCtxKey).(int64)
	return v
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
	SessionSecret() []byte
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
//  2. Health / auth endpoints — always allowed through
//  3. Mode == disabled            — always allowed
//  4. Mode == local-only + local  — always allowed
//  5. Valid X-Api-Key header or ?apikey= query — allowed
//  6. Valid signed session cookie — allowed
//  7. Mode == proxy: trusted peer IP + identity header → resolve/provision user
//  8. Otherwise                   — 401
func Middleware(p Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Resolve identity up-front regardless of path. A successful
			// cookie verification attaches the user id to ctx so unauth-path
			// handlers (like /auth/status) can report "authenticated: true".
			ctx := r.Context()
			cookieValid := false
			if c, err := r.Cookie(SessionCookieName); err == nil {
				if uid, err := VerifySession(p.SessionSecret(), c.Value); err == nil {
					ctx = context.WithValue(ctx, userIDCtxKey, uid)
					cookieValid = true
				}
			}
			r = r.WithContext(ctx)

			if AllowUnauthPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			mode := p.Mode()
			if mode == ModeDisabled {
				next.ServeHTTP(w, r)
				return
			}
			if mode == ModeLocalOnly && IsLocalRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			if key := requestAPIKey(r); key != "" && key == p.APIKey() {
				next.ServeHTTP(w, r)
				return
			}
			if cookieValid {
				next.ServeHTTP(w, r)
				return
			}

			if mode == ModeProxy {
				if uid, ok := resolveProxyIdentity(r, p); ok {
					r = r.WithContext(context.WithValue(r.Context(), userIDCtxKey, uid))
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
// vulnerable and do not need the header.
func RequireXRequestedWith(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// safe methods — pass through
		default:
			if requestAPIKey(r) == "" && r.Header.Get("X-Requested-With") != "bindery-ui" {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}
