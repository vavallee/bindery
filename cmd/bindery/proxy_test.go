package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/api"
)

func TestTrustedProxyMiddleware_OIDCRedirectUsesOriginalPeer(t *testing.T) {
	t.Setenv("BINDERY_TRUSTED_PROXY", "10.0.0.5/32")
	trusted := parseTrustedProxyCIDRs("10.0.0.5/32")

	var gotBase, gotRemoteAddr string
	handler := trustedProxyMiddleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotBase = api.ResolveOIDCRedirectBase(r, "", trusted)
		gotRemoteAddr = r.RemoteAddr
	}))

	req := httptest.NewRequest(http.MethodGet, "http://internal.cluster.svc/api/v1/auth/oidc/authelia/login", nil)
	req.RemoteAddr = "10.0.0.5:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "bindery.example.com")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotRemoteAddr != "203.0.113.50:54321" {
		t.Fatalf("trusted real-IP middleware should rewrite RemoteAddr, got %q", gotRemoteAddr)
	}
	if gotBase != "https://bindery.example.com" {
		t.Fatalf("OIDC redirect should trust forwarded headers from the original proxy peer, got %q", gotBase)
	}
}

// A reverse proxy missing from BINDERY_TRUSTED_PROXY looks like a private peer
// sending forwarded headers that get discarded. The middleware logs that once
// and changes nothing else.
func TestTrustedProxyMiddleware_WarnsOnceForForwardedHeadersFromUntrustedPrivatePeer(t *testing.T) {
	t.Setenv("BINDERY_TRUSTED_PROXY", "")
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var gotXFF string
	h := trustedProxyMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
	}))
	do := func(remote, xff string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	do("203.0.113.5:1000", "10.0.0.1") // public peer: spoofing, not misconfiguration
	if buf.Len() != 0 {
		t.Fatalf("a public peer must not trigger the warning: %s", buf.String())
	}
	do("10.0.0.5:1000", "198.51.100.4")
	do("10.0.0.5:1001", "198.51.100.5")
	if n := strings.Count(buf.String(), "not in BINDERY_TRUSTED_PROXY"); n != 1 {
		t.Fatalf("want exactly one warning, got %d: %s", n, buf.String())
	}
	if gotXFF != "" {
		t.Errorf("behaviour must not change: X-Forwarded-For still stripped, got %q", gotXFF)
	}
}
