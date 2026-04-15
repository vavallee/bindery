package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders_AlwaysPresent(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

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
	check("Content-Security-Policy", cspValue)
}

func TestSecurityHeaders_CSP_ExactMatch(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	got := rr.Header().Get("Content-Security-Policy")
	if got != cspValue {
		t.Errorf("CSP mismatch:\n got  %q\n want %q", got, cspValue)
	}
}

func TestSecurityHeaders_HSTS_AbsentOverHTTP(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No TLS, no X-Forwarded-Proto header.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent over HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTS_PresentWhenTLS(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

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
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

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
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent for X-Forwarded-Proto: http, got %q", got)
	}
}
