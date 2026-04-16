package auth

import (
	"context"
	"log/slog"
	"net/http"
)

// Mode represents the auth posture. Matches Sonarr's "Authentication Required"
// dropdown semantics.
type Mode string

const (
	ModeDisabled  Mode = "disabled"   // no auth check at all (not recommended; not the default)
	ModeLocalOnly Mode = "local-only" // RFC1918 + loopback clients bypass
	ModeEnabled   Mode = "enabled"    // everyone must authenticate
)

// ParseMode coerces a free-form string into a valid Mode; unknown values map
// to ModeEnabled (fail-safe).
func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeDisabled, ModeLocalOnly, ModeEnabled:
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

// Provider is the data the middleware needs at request time. Implemented by
// main.go via a small adapter; keeps this package free of db imports.
type Provider interface {
	Mode() Mode
	APIKey() string
	SessionSecret() []byte
	// SetupRequired reports whether no user exists yet (first-run). When true
	// and the request is unauthenticated, /setup endpoints are allowed through.
	SetupRequired() bool
}

// AllowUnauthPath returns true for routes the middleware must always let
// through, regardless of auth state (health probes, auth endpoints themselves).
func AllowUnauthPath(path string) bool {
	switch path {
	case "/api/v1/health",
		"/api/v1/auth/status",
		"/api/v1/auth/login",
		"/api/v1/auth/logout",
		"/api/v1/auth/setup":
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
//  7. Otherwise                   — 401
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

func requestAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}
