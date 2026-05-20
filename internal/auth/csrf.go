package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

const CSRFCookieName = "bindery_csrf"

// MakeCSRFToken derives a double-submit CSRF token from the raw session cookie
// value and the session secret. Binding the token to the session value means
// it is automatically invalidated when the session rotates.
func MakeCSRFToken(secret []byte, sessionValue string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("csrf:" + sessionValue))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// ValidCSRFToken reports whether the supplied token matches what we would
// derive from the session cookie present on r.
func ValidCSRFToken(secret []byte, r *http.Request, token string) bool {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	want := MakeCSRFToken(secret, c.Value)
	return hmac.Equal([]byte(token), []byte(want))
}

// RequireCSRFToken rejects state-mutating requests that lack a valid
// X-CSRF-Token header. Exempt: verified-API-key requests, safe methods,
// AllowUnauthPath routes (login, logout, setup…), and requests with no session
// cookie.
//
// The API-key exemption keys off the AuthedViaAPIKey context flag, which
// Middleware sets only after subtle.ConstantTimeCompare confirms the key. A
// request carrying a *bogus* ?apikey= no longer skips the CSRF check: it fails
// key verification, falls through to cookie auth, and is held to the token
// requirement like any other session request (#708 finding 3). This depends on
// auth.Middleware running before this middleware — see cmd/bindery/main.go.
func RequireCSRFToken(secret func() []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				// safe methods — no mutation risk
			default:
				if !AuthedViaAPIKey(r.Context()) && !AllowUnauthPath(r.URL.Path) {
					if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
						tok := r.Header.Get("X-CSRF-Token")
						if !ValidCSRFToken(secret(), r, tok) {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusForbidden)
							_, _ = w.Write([]byte(`{"error":"invalid or missing CSRF token"}`))
							return
						}
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
