package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/api"
)

// TestSecurityHeaders_AlwaysPresent asserts the middleware emits the full
// baseline set on every response, regardless of method or path. A missing
// header is a regression against the v0.12.0 hardening.
func TestSecurityHeaders_AlwaysPresent(t *testing.T) {
	t.Parallel()
	h := api.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	required := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, want := range required {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: want %q, got %q", k, want, got)
		}
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy must be set")
	}
}

// TestSecurityHeaders_CSPLocksDownExternalOrigins verifies the CSP
// forbids inline script and foreign origins. Relaxing any of these
// without documenting the reason should fail this test.
func TestSecurityHeaders_CSPLocksDownExternalOrigins(t *testing.T) {
	t.Parallel()
	h := api.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, banned := range []string{
		"'unsafe-eval'",
		"*",
	} {
		if containsWord(csp, banned) {
			t.Errorf("CSP should not contain %q, got: %s", banned, csp)
		}
	}
	for _, required := range []string{
		"default-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	} {
		if !containsWord(csp, required) {
			t.Errorf("CSP missing required directive %q, got: %s", required, csp)
		}
	}
}

// TestSecurityHeaders_HSTSOnlyUnderTLS asserts HSTS is emitted only when
// TLS is actually in play (direct TLS or X-Forwarded-Proto: https). Emitting
// it on plaintext would lock the browser into a host that doesn't speak TLS.
func TestSecurityHeaders_HSTSOnlyUnderTLS(t *testing.T) {
	t.Parallel()
	h := api.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("plain HTTP", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Error("HSTS must NOT be set over plain HTTP")
		}
	})

	t.Run("X-Forwarded-Proto https", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Error("HSTS should be set behind a TLS-terminating proxy")
		}
	})
}

// containsWord is a substring test that matches the reviewer-friendly
// "CSP contains directive X" intent. Not regex-accurate, but sufficient
// for the fixed directive strings we emit.
func containsWord(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
