package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
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
							logCSRFRejection("csrf_token", r,
								"session cookie present but X-CSRF-Token is missing or does not match the session",
								"csrf_token_present", tok != "")
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

// logCSRFRejection records why one of the two CSRF guards
// (RequireXRequestedWith, RequireCSRFToken) turned a mutating request away.
// Both used to reject with a bare `{"error":"forbidden"}` and no log line at
// any level, which is what made #1849 impossible to diagnose from the outside:
// a request carrying a valid API key came back 403 and the server left no
// trace, so the cause had to be reconstructed from the source (#1895).
//
// Debug, deliberately. This guard also fires on genuine cross-site forgery
// attempts, and an unauthenticated caller decides how often it fires — at Warn
// or Info that would be a default-level log flood anyone could trigger from
// outside. At Debug it costs nothing until an operator raises the level to
// chase exactly this question.
//
// No credential material is logged: not the API key, the session cookie, the
// CSRF token, nor the Authorization header — only whether such material was
// *present*, which is what distinguishes "sent no key" from "sent a key we did
// not accept". The path is r.URL.Path and never RequestURI, because the query
// string can carry ?apikey= (#708 finding 4a).
func logCSRFRejection(guard string, r *http.Request, reason string, extra ...any) {
	attrs := []any{
		"guard", guard,
		"method", r.Method,
		"path", r.URL.Path,
		// peer is the TCP peer (the proxy, behind one); client is the
		// address X-Forwarded-For resolved to. Same value without a proxy.
		"peer", RealPeerHost(r),
		"client", ipString(requestPeerIP(r)),
		"reason", reason,
		"api_key_header", r.Header.Get("X-Api-Key") != "",
		// A key in ?apikey= is ignored on mutating methods (#708 finding 4a),
		// so "sent a key, still got 403" is a routine cause of landing here.
		// Reporting the parameter's presence separately answers that without
		// the operator having to know the rule.
		"api_key_query", r.URL.Query().Has("apikey"),
		"session_cookie_present", sessionCookiePresent(r),
	}
	slog.Debug("csrf guard: rejecting mutating request", append(attrs, extra...)...)
}

// sessionCookiePresent reports whether the request carries a non-empty session
// cookie. Only its presence is ever logged — the value authenticates the
// caller and would be a session-hijacking credential in a log file.
func sessionCookiePresent(r *http.Request) bool {
	c, err := r.Cookie(SessionCookieName)
	return err == nil && c.Value != ""
}
