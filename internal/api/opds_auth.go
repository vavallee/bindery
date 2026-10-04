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
//  2. Valid X-Api-Key header or ?apikey= query — allowed
//  3. Mode == local-only + RFC1918 IP  — always allowed
//  4. Valid signed session cookie      — allowed
//  5. Valid Basic credentials          — allowed
//  6. Otherwise                        — 401 with WWW-Authenticate: Basic
//
// Steps 2 and 3 are in that order on purpose, matching auth.Middleware after
// #1849: a request that carries a valid key must be recognised as a key
// request even when its source address would have been let through anyway.
// The reverse order let the local-only bypass swallow a verified key before
// anything could observe it, which is what turned into a 403 on mutations in
// #1849 (#1894). A missing or wrong key still falls through to the bypass.
//
// The realm ("Bindery OPDS") is what shows in the client's credential
// prompt; keep it descriptive so users know which server is asking.
func OPDSAuth(p auth.Provider, users *db.UserRepo, limiter *auth.LoginLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := p.Mode()

			// A requester's session is refused before the mode bypasses below,
			// matching auth.Middleware: the mode never elevates a requester.
			if opdsSessionIsRequester(r, p, users) {
				opdsRoleAllowed(w, auth.RoleRequester)
				return
			}

			if mode == auth.ModeDisabled {
				next.ServeHTTP(w, r)
				return
			}
			// Checked before the local-only bypass below, mirroring
			// auth.Middleware (#1849). Both branches let the request through
			// with identical context today, so for a trusted-local caller
			// nothing observable changes — but the bypass running first is the
			// exact shape that made a verified key invisible downstream in
			// #1849, and the OPDS subtree is one mutating route away from
			// reproducing it. Keep the key check first (#1894).
			if key := opdsAPIKey(r); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(p.APIKey())) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if mode == auth.ModeLocalOnly && auth.IsLocalRequestTrusted(r, p.TrustedProxyCIDRs()) {
				next.ServeHTTP(w, r)
				return
			}
			if c, err := r.Cookie(auth.SessionCookieName); err == nil {
				if uid, epoch, err := auth.VerifySessionMultiWithEpoch(p.SessionSecrets(), c.Value); err == nil {
					// Session-epoch check (Wave 1 / Bundle C): the cookie's
					// epoch must match the user's current users.session_epoch
					// (UpdatePassword bumps it on password change). users may
					// be nil under test harnesses that don't wire it; in that
					// case fall back to signature/expiry-only verification.
					// On every success path, attach the verified user id to
					// the request context so the OPDS handler can scope the
					// feed to the caller's library when EnforceTenancy is on
					// (D3).
					if users == nil {
						if !opdsRoleAllowed(w, p.UserRole(r.Context(), uid)) {
							return
						}
						r = r.WithContext(auth.WithUserID(r.Context(), uid))
						next.ServeHTTP(w, r)
						return
					}
					if liveEpoch, err := users.GetSessionEpoch(r.Context(), uid); err == nil && liveEpoch == epoch && !opdsSessionRevoked(r, p, c.Value) {
						if !opdsRoleAllowed(w, p.UserRole(r.Context(), uid)) {
							return
						}
						r = r.WithContext(auth.WithUserID(r.Context(), uid))
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			if username, password, ok := r.BasicAuth(); ok && users != nil {
				ip := opdsClientIP(r)
				tooMany := func() {
					w.Header().Set("WWW-Authenticate", `Basic realm="Bindery OPDS"`)
					http.Error(w, "too many attempts", http.StatusTooManyRequests)
				}
				if limiter != nil && !limiter.Allow(ip) {
					tooMany()
					return
				}
				u, err := users.GetByUsername(r.Context(), strings.TrimSpace(username))
				// Verify against a dummy hash when the user is missing so the
				// basic-auth response time does not reveal which usernames exist
				// (mirrors the main login handler). See auth.DummyPasswordHash.
				hash := auth.DummyPasswordHash()
				if err == nil && u != nil {
					hash = u.PasswordHash
				}
				// Reserve the attempt before the KDF, as the login handler
				// does: Allow alone let a concurrent burst run one
				// verification per request. The reservation stands as the
				// failure unless Reset clears it below.
				if limiter != nil && !limiter.Acquire(ip) {
					tooMany()
					return
				}
				ok, verr := auth.VerifyPasswordContext(r.Context(), password, hash)
				if verr != nil {
					// Client went away while queued for a KDF slot.
					http.Error(w, "server busy, try again", http.StatusServiceUnavailable)
					return
				}
				if err == nil && u != nil && ok {
					if limiter != nil {
						limiter.Reset(ip)
					}
					// The password is correct, so the limiter is reset, but a
					// requester may not read the feed or download its files:
					// they browse through /requests/library only.
					if !opdsRoleAllowed(w, u.Role) {
						return
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
				// A failed verification needs no Record here: the attempt
				// was already counted by Acquire above.
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

// opdsSessionIsRequester reports whether r carries a valid, current session
// cookie for a user whose role is requester.
func opdsSessionIsRequester(r *http.Request, p auth.Provider, users *db.UserRepo) bool {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return false
	}
	uid, epoch, err := auth.VerifySessionMultiWithEpoch(p.SessionSecrets(), c.Value)
	if err != nil {
		return false
	}
	if users != nil {
		if live, err := users.GetSessionEpoch(r.Context(), uid); err != nil || live != epoch {
			return false
		}
	}
	// A signed out requester cookie no longer speaks for anyone, so it is
	// treated like no cookie rather than refused as a requester.
	if opdsSessionRevoked(r, p, c.Value) {
		return false
	}
	return p.UserRole(r.Context(), uid) == auth.RoleRequester
}

// opdsSessionRevoked reports whether cookie was signed out via /auth/logout.
// A failed lookup counts as revoked: the cookie then fails here and the
// client falls through to Basic auth or the 401 challenge, never to access.
func opdsSessionRevoked(r *http.Request, p auth.Provider, cookie string) bool {
	revoked, err := p.SessionRevoked(r.Context(), auth.SessionTokenHash(cookie))
	if err != nil {
		slog.Warn("opds: session revocation lookup failed", "error", err)
		return true
	}
	return revoked
}

// opdsRoleAllowed answers 403 and returns false when role may not read the
// library directly (auth.RoleHasLibraryAccess). OPDS serves book files, and a
// requester browses the library read only, without downloads. The API key,
// disabled and local-only branches above admit the install itself and are
// unaffected.
func opdsRoleAllowed(w http.ResponseWriter, role string) bool {
	if auth.RoleHasLibraryAccess(role) {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"not available to requesters"}`))
	return false
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
