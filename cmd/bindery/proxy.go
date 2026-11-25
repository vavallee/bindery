package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/httpsec/realip"
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
// X-Forwarded-For / True-Client-IP / X-Real-IP only when the direct peer is a
// configured trusted proxy. When the peer is not trusted, all forwarded
// headers are stripped so downstream handlers cannot be spoofed via
// X-Forwarded-Proto or X-Forwarded-Host.
//
// Configured via BINDERY_TRUSTED_PROXY: a comma-separated list of IPs or
// CIDRs. Bare IPs are treated as /32 (IPv4) or /128 (IPv6).
//
// Previously this delegated RemoteAddr rewriting to chi's middleware.RealIP
// inside the trusted-peer branch. That middleware was deprecated upstream
// (SA1019 — GHSA-3fxj-6jh8-hvhx / GHSA-rjr7-jggh-pgcp / GHSA-9g5q-2w5x-hmxf)
// because it picks the leftmost X-Forwarded-For entry, which a remote client
// can forge. We now use httpsec/realip.TrustedRealIP, which walks XFF
// right-to-left to find the first untrusted hop (matches auth.ResolveClientIP).
func trustedProxyMiddleware() func(http.Handler) http.Handler {
	trusted := parseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))
	realIP := realip.TrustedRealIP(trusted)
	var warnOnce sync.Once

	return func(next http.Handler) http.Handler {
		// realIP itself is a no-op when trusted is empty, so this composes
		// safely without a mount-time gate.
		hardened := realIP(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the true TCP peer before TrustedRealIP overwrites
			// RemoteAddr. The local-only trust decision needs the genuine peer
			// to walk X-Forwarded-For right-to-left; without it, downstream
			// code would have to reverse-engineer "was this rewritten?" from
			// the post-rewrite value. Every "is this sender a trusted proxy?"
			// decision downstream (proxy auth identity header, OIDC redirect
			// base) must read auth.RealPeerHost, not r.RemoteAddr (#3096).
			r = r.WithContext(auth.WithRealPeer(r.Context(), r.RemoteAddr))

			peerHost, _, _ := net.SplitHostPort(r.RemoteAddr)
			peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))
			for _, cidr := range trusted {
				if peerIP != nil && cidr.Contains(peerIP) {
					hardened.ServeHTTP(w, r)
					return
				}
			}
			// Peer is not a trusted proxy — strip all forwarded headers so
			// they cannot influence scheme/host detection downstream.
			if forwardedFromPrivatePeer(r, peerHost) {
				warnOnce.Do(func() { warnForwardedFromUntrustedPeer(r, peerHost, len(trusted) == 0) })
			}
			r = r.Clone(r.Context())
			for _, h := range proxyHeaders {
				r.Header.Del(h)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// warnForwardedFromUntrustedPeer logs, once per process, that a request from a
// private-range peer carried forwarded headers that were discarded because the
// peer is not in BINDERY_TRUSTED_PROXY. That is what a reverse proxy looks like
// when its address is missing from the list: every client then shares the
// proxy's address, so the login rate limiter has one bucket for everyone (five
// wrong passwords for "admin" lock the admin login for all users) and
// local-only mode sees the proxy as the client. Nothing changes in behaviour;
// the line only names the cause. Peers outside private ranges are not reported:
// an internet client sending X-Forwarded-For is spoofing, not misconfigured.
func warnForwardedFromUntrustedPeer(r *http.Request, peerHost string, listEmpty bool) {
	seen := forwardedHeadersPresent(r)
	slog.Warn("forwarded headers from a private-range peer were ignored because the peer is not in BINDERY_TRUSTED_PROXY; "+
		"if this peer is your reverse proxy, add its address, otherwise all clients share its rate-limit bucket and local-only mode sees the proxy as the client",
		"peer", peerHost, "headers", seen, "trusted_proxy_list_empty", listEmpty)
}

// forwardedHeadersPresent lists the forwarding headers the request carries.
func forwardedHeadersPresent(r *http.Request) []string {
	var seen []string
	for _, h := range append([]string{"True-Client-IP"}, proxyHeaders...) {
		if r.Header.Get(h) != "" {
			seen = append(seen, h)
		}
	}
	return seen
}

// forwardedFromPrivatePeer reports whether a private-range peer sent forwarded
// headers. It is checked before the once-guard so that a public peer's spoofed
// header cannot use up the single warning.
func forwardedFromPrivatePeer(r *http.Request, peerHost string) bool {
	return auth.IsLocalIP(strings.Trim(peerHost, "[]")) && len(forwardedHeadersPresent(r)) > 0
}
