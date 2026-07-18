package main

import (
	"net/http"
	"net/http/httptest"
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
