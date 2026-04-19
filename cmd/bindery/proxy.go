package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// parseTrustedProxyCIDRs parses a comma-separated list of IP/CIDR strings
// (from BINDERY_TRUSTED_PROXY) into []*net.IPNet. Bare IPs are treated as
// /32 (IPv4) or /128 (IPv6). Invalid entries are logged and skipped.
func parseTrustedProxyCIDRs(raw string) []*net.IPNet {
	if raw == "" {
		return nil
	}
	var trusted []*net.IPNet
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			slog.Warn("BINDERY_TRUSTED_PROXY: invalid entry, skipping", "entry", s, "err", err)
			continue
		}
		trusted = append(trusted, cidr)
	}
	return trusted
}

// trustedProxyMiddleware returns a middleware that rewrites RemoteAddr from
// X-Forwarded-For / X-Real-IP only when the direct peer is a configured
// trusted proxy. Without a trusted proxy configured, forwarded headers are
// ignored and the peer IP is used as-is — preventing XFF spoofing in
// local-only auth mode.
//
// Configured via BINDERY_TRUSTED_PROXY: a comma-separated list of IPs or
// CIDRs. Bare IPs are treated as /32 (IPv4) or /128 (IPv6).
func trustedProxyMiddleware() func(http.Handler) http.Handler {
	trusted := parseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))
	if len(trusted) == 0 {
		// No trusted proxy — use peer IP as-is (safe default).
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peerHost, _, _ := net.SplitHostPort(r.RemoteAddr)
			peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))
			for _, cidr := range trusted {
				if peerIP != nil && cidr.Contains(peerIP) {
					middleware.RealIP(next).ServeHTTP(w, r)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
