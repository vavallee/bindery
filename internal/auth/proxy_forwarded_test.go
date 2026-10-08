package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// Regression tests for #3096. trustedProxyMiddleware stores the TCP peer with
// WithRealPeer and then lets TrustedRealIP rewrite r.RemoteAddr to the client
// resolved from X-Forwarded-For. resolveProxyIdentity used to decide trust on
// that rewritten address, so behind any proxy that sets X-Forwarded-For it
// asked whether the visitor was a trusted proxy and rejected every request.
// These tests build the request the way it reaches auth.Middleware after that
// rewrite: the real peer in the context, the forwarded client in RemoteAddr.

func forwardedProxyRequest(path, realPeer, remoteAddr string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(WithRealPeer(req.Context(), realPeer))
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-User", "alice")
	return req
}

// A trusted proxy (172.17.0.1, cloudflared on the Docker bridge) forwarding a
// visitor at 203.0.113.9 must authenticate the identity header, on the status
// path the login page asks and on a protected route.
func TestProxyAuthTrustedPeerWithForwardedClientAuthenticates(t *testing.T) {
	for _, path := range []string{"/api/v1/auth/status", "/api/v1/author"} {
		t.Run(path, func(t *testing.T) {
			p := proxyProvider("172.17.0.0/16", false, 42)
			var gotUID int64
			called := false
			h := Middleware(p)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				called = true
				gotUID = UserIDFromContext(r.Context())
			}))
			w := &captureWriter{}
			h.ServeHTTP(w, forwardedProxyRequest(path, "172.17.0.1:40000", "203.0.113.9:40000"))
			if !called {
				t.Fatalf("handler not reached, status %d; the trusted proxy's identity header was rejected", w.status)
			}
			if gotUID != 42 {
				t.Fatalf("uid = %d; want 42: trust must be decided on the TCP peer, not the forwarded client", gotUID)
			}
		})
	}
}

// The reverse: the decision follows the TCP peer even when RemoteAddr holds an
// address inside the trusted range. An untrusted peer never has RemoteAddr
// rewritten in production, but if anything ever did put a trusted looking
// value there it must not lend the peer the proxy's trust.
func TestProxyAuthUntrustedPeerWithTrustedLookingRemoteAddrRejected(t *testing.T) {
	p := proxyProvider("172.17.0.0/16", false, 42)
	called := false
	h := Middleware(p)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	w := &captureWriter{}
	h.ServeHTTP(w, forwardedProxyRequest("/api/v1/author", "198.51.100.7:40000", "172.17.0.1:40000"))
	if called {
		t.Fatal("an untrusted TCP peer must not authenticate through the identity header")
	}
	if w.status != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", w.status)
	}
}

// The rejection warning names the TCP peer and the forwarded client
// separately, so an operator can see whether the proxy itself is missing from
// BINDERY_TRUSTED_PROXY. It used to log only the forwarded client as "peer".
func TestProxyAuthRejectionLogsPeerAndClientSeparately(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := proxyProvider("10.0.0.0/8", false, 42)
	h := Middleware(p)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	h.ServeHTTP(&captureWriter{}, forwardedProxyRequest("/api/v1/author", "172.17.0.1:40000", "203.0.113.9:40000"))

	out := buf.String()
	if !strings.Contains(out, "identity header from untrusted source") {
		t.Fatalf("no rejection warning logged: %s", out)
	}
	if !strings.Contains(out, "peer=172.17.0.1") {
		t.Errorf("warning should report the TCP peer as peer: %s", out)
	}
	if !strings.Contains(out, "client=203.0.113.9") {
		t.Errorf("warning should report the forwarded client as client: %s", out)
	}
}
