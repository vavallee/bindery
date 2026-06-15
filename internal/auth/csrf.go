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
// it is automatically invalidated when the session rotates. Tokens are always
// minted with the current secret; see ValidCSRFToken for the rotation-window
// acceptance of tokens minted under a just-rotated-out secret.
func MakeCSRFToken(secret []byte, sessionValue string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("csrf:" + sessionValue))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// ValidCSRFToken reports whether the supplied token matches what we would
// derive from the session cookie present on r, under any of the candidate
// secrets. During a session-secret rotation the verifier is handed
// {current, previous} so a CSRF token minted just before the rotation still
// validates until the holder's next /auth/csrf refresh re-mints it under the
// current secret. Verification is never weakened: the token is accepted only
// if it equals (in constant time) a token derived from some candidate secret.
func ValidCSRFToken(secrets [][]byte, r *http.Request, token string) bool {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	for _, secret := range secrets {
		want := MakeCSRFToken(secret, c.Value)
		if hmac.Equal([]byte(token), []byte(want)) {
			return true
		}
	}
	return false
}

// RequireCSRFToken rejects state-mutating requests that lack a valid
// X-CSRF-Token header. Exempt: verified-API-key requests, safe methods,
// AllowUnauthPath routes (login, logout, setup…), and requests with no session
// cookie.
//
// The secrets func returns an ordered candidate set ({current, previous}
// during a rotation window). The token is validated against every candidate so
// a token minted just before a session-secret rotation is still accepted until
// the next /auth/csrf refresh re-mints it under the current secret.
//
// The API-key exemption keys off the AuthedViaAPIKey context flag, which
// Middleware sets only after subtle.ConstantTimeCompare confirms the key. A
// request carrying a *bogus* ?apikey= no longer skips the CSRF check: it fails
// key verification, falls through to cookie auth, and is held to the token
// requirement like any other session request (#708 finding 3). This depends on
// auth.Middleware running before this middleware — see cmd/bindery/main.go.
func RequireCSRFToken(secrets func() [][]byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				// safe methods — no mutation risk
			default:
				if !AuthedViaAPIKey(r.Context()) && !AllowUnauthPath(r.Method, r.URL.Path) {
					if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
						tok := r.Header.Get("X-CSRF-Token")
						if !ValidCSRFToken(secrets(), r, tok) {
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
