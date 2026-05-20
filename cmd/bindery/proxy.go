package main

import (
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/auth"
)

// parseTrustedProxyCIDRs parses a comma-separated list of IP/CIDR strings
// (from BINDERY_TRUSTED_PROXY) into []*net.IPNet. Bare IPs are treated as
// /32 (IPv4) or /128 (IPv6). Invalid entries are skipped. It delegates to
// auth.ParseTrustedProxyCIDRs so the proxy middleware and the local-only
// trust decision parse the env var identically.
func parseTrustedProxyCIDRs(raw string) []*net.IPNet {
	return auth.ParseTrustedProxyCIDRs(raw)
}

// proxyHeaders is the set of forwarded headers that only a trusted proxy
// should be allowed to set. They are stripped from every request that does
// not originate from a configured trusted proxy, preventing spoofing of
// OPDS base URLs, HSTS detection, and session-cookie Secure flags.
var proxyHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Proto",
	"X-Forwarded-Host",
	"X-Real-IP",
}

// trustedProxyMiddleware returns a middleware that rewrites RemoteAddr from
// X-Forwarded-For / X-Real-IP only when the direct peer is a configured
// trusted proxy. When the peer is not trusted, all forwarded headers are
// stripped so downstream handlers cannot be spoofed via X-Forwarded-Proto
// or X-Forwarded-Host.
//
// Configured via BINDERY_TRUSTED_PROXY: a comma-separated list of IPs or
// CIDRs. Bare IPs are treated as /32 (IPv4) or /128 (IPv6).
func trustedProxyMiddleware() func(http.Handler) http.Handler {
	trusted := parseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the true TCP peer before chi's RealIP overwrites
			// RemoteAddr. The local-only trust decision needs the genuine peer
			// to walk X-Forwarded-For right-to-left; RealIP's leftmost pick is
			// attacker-controlled and must not be used for auth.
			r = r.WithContext(auth.WithRealPeer(r.Context(), r.RemoteAddr))

			peerHost, _, _ := net.SplitHostPort(r.RemoteAddr)
			peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))
			for _, cidr := range trusted {
				if peerIP != nil && cidr.Contains(peerIP) {
					middleware.RealIP(next).ServeHTTP(w, r)
					return
				}
			}
			// Peer is not a trusted proxy — strip all forwarded headers so
			// they cannot influence scheme/host detection downstream.
			r = r.Clone(r.Context())
			for _, h := range proxyHeaders {
				r.Header.Del(h)
			}
			next.ServeHTTP(w, r)
		})
	}
}
