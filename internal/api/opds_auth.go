package api

import (
	"log/slog"
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
func OPDSAuth(p auth.Provider, users *db.UserRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := p.Mode()

			if mode == auth.ModeDisabled {
				next.ServeHTTP(w, r)
				return
			}
			if mode == auth.ModeLocalOnly && auth.IsLocalRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			if key := opdsAPIKey(r); key != "" && key == p.APIKey() {
				next.ServeHTTP(w, r)
				return
			}
			if c, err := r.Cookie(auth.SessionCookieName); err == nil {
				if _, err := auth.VerifySession(p.SessionSecret(), c.Value); err == nil {
					next.ServeHTTP(w, r)
					return
				}
			}
			if username, password, ok := r.BasicAuth(); ok && users != nil {
				u, err := users.GetByUsername(r.Context(), strings.TrimSpace(username))
				if err == nil && u != nil && auth.VerifyPassword(password, u.PasswordHash) {
					next.ServeHTTP(w, r)
					return
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

func opdsAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}
