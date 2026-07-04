package api

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// lockedCSP is the Content-Security-Policy emitted with the default (empty)
// frame-ancestors config — embedding fully blocked.
var lockedCSP = fmt.Sprintf(cspTemplate, "'none'")

// okHandler is a trivial 200 handler used to exercise the middleware.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestSecurityHeaders_AlwaysPresent(t *testing.T) {
	h := SecurityHeaders("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	check := func(header, want string) {
		t.Helper()
		got := rr.Header().Get(header)
		if got != want {
			t.Errorf("%s: got %q, want %q", header, got, want)
		}
	}

	check("X-Content-Type-Options", "nosniff")
	check("X-Frame-Options", "DENY")
	check("Referrer-Policy", "strict-origin-when-cross-origin")
	check("Content-Security-Policy", lockedCSP)
}

func TestSecurityHeaders_CSP_ExactMatch(t *testing.T) {
	h := SecurityHeaders("")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	got := rr.Header().Get("Content-Security-Policy")
	if got != lockedCSP {
		t.Errorf("CSP mismatch:\n got  %q\n want %q", got, lockedCSP)
	}
}

// When BINDERY_FRAME_ANCESTORS is set, the CSP frame-ancestors directive uses
// the supplied source list and X-Frame-Options is dropped so it cannot override
// the origin allowlist (issue #1367).
func TestSecurityHeaders_FrameAncestors_AllowsEmbedding(t *testing.T) {
	cases := []struct{ name, ancestors, wantSrc string }{
		{"same-origin", "'self'", "'self'"},
		{"specific-host", "https://organizr.example.com", "https://organizr.example.com"},
		{"trims-whitespace", "  'self'  ", "'self'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := SecurityHeaders(tc.ancestors)(okHandler())
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if got := rr.Header().Get("X-Frame-Options"); got != "" {
				t.Errorf("X-Frame-Options should be absent when framing is allowed, got %q", got)
			}
			wantCSP := fmt.Sprintf(cspTemplate, tc.wantSrc)
			if got := rr.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("CSP:\n got  %q\n want %q", got, wantCSP)
			}
			if got := rr.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors "+tc.wantSrc) {
				t.Errorf("CSP missing expected frame-ancestors %q: %q", tc.wantSrc, got)
			}
		})
	}
}

func TestSecurityHeaders_HSTS_AbsentOverHTTP(t *testing.T) {
	h := SecurityHeaders("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No TLS, no X-Forwarded-Proto header.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent over HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_PresentWhenTLS(t *testing.T) {
	h := SecurityHeaders("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{} // signal TLS
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := "max-age=63072000; includeSubDomains"
	if got := rr.Header().Get("Strict-Transport-Security"); got != want {
		t.Errorf("HSTS: got %q, want %q", got, want)
	}
}

func TestSecurityHeaders_HSTS_PresentWhenForwardedProto(t *testing.T) {
	h := SecurityHeaders("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := "max-age=63072000; includeSubDomains"
	if got := rr.Header().Get("Strict-Transport-Security"); got != want {
		t.Errorf("HSTS via X-Forwarded-Proto: got %q, want %q", got, want)
	}
}

func TestSecurityHeaders_HSTS_AbsentWhenForwardedProtoHTTP(t *testing.T) {
	h := SecurityHeaders("")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent for X-Forwarded-Proto: http, got %q", got)
	}
}
