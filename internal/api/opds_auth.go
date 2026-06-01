package api

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// OPDSAuth returns middleware that guards the /opds/* subtree. Precedence
// mirrors the global auth middleware but adds HTTP Basic on top, because
// that's what KOReader / Moon+ Reader / Aldiko all speak natively:
//
//  1. Mode == disabled                 — always allowed
//  2. Mode == local-only + RFC1918 IP  — always allowed
//  3. Valid X-Api-Key header or ?apikey= query — allowed
//  4. Valid signed session cookie      — allowed
//  5. Valid Basic credentials          — allowed
//  6. Otherwise                        — 401 with WWW-Authenticate: Basic
//
// The realm ("Bindery OPDS") is what shows in the client's credential
// prompt; keep it descriptive so users know which server is asking.
func OPDSAuth(p auth.Provider, users *db.UserRepo, limiter *auth.LoginLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := p.Mode()

			if mode == auth.ModeDisabled {
				next.ServeHTTP(w, r)
				return
			}
			if mode == auth.ModeLocalOnly && auth.IsLocalRequestTrusted(r, p.TrustedProxyCIDRs()) {
				next.ServeHTTP(w, r)
				return
			}
			if key := opdsAPIKey(r); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(p.APIKey())) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if c, err := r.Cookie(auth.SessionCookieName); err == nil {
				if uid, err := auth.VerifySessionMulti(p.SessionSecrets(), c.Value); err == nil {
					// Attach the verified session's user id so the OPDS
					// handler can scope the feed to the caller's library
					// when EnforceTenancy is on (D3).
					r = r.WithContext(auth.WithUserID(r.Context(), uid))
					next.ServeHTTP(w, r)
					return
				}
			}
			if username, password, ok := r.BasicAuth(); ok && users != nil {
				ip := opdsClientIP(r)
				if limiter != nil && !limiter.Allow(ip) {
					w.Header().Set("WWW-Authenticate", `Basic realm="Bindery OPDS"`)
					http.Error(w, "too many attempts", http.StatusTooManyRequests)
					return
				}
				u, err := users.GetByUsername(r.Context(), strings.TrimSpace(username))
				if err == nil && u != nil && auth.VerifyPassword(password, u.PasswordHash) {
					if limiter != nil {
						limiter.Reset(ip)
					}
					// Attach the basic-auth user id to ctx so the OPDS
					// handler can filter the feed to the caller's library
					// under EnforceTenancy. Without this the basic-auth
					// path (KOReader, Moon+) would be the one place every
					// user sees every other user's books.
					r = r.WithContext(auth.WithUserID(r.Context(), u.ID))
					next.ServeHTTP(w, r)
					return
				}
				if limiter != nil {
					limiter.Record(ip)
				}
			}

			// Challenge — OPDS clients retry with credentials on 401.
			w.Header().Set("WWW-Authenticate", `Basic realm="Bindery OPDS"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			if _, err := w.Write([]byte(`{"error":"unauthorized"}`)); err != nil {
				slog.Warn("failed to write OPDS unauthorized response", "error", err)
			}
		})
	}
}

func opdsClientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

func opdsAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}
