package api

import (
	"net"
	"net/http"
	"strings"
)

// ResolveOIDCRedirectBase returns the base URL to use as the prefix for OIDC
// redirect_uri construction. Precedence:
//
//  1. configured (BINDERY_OIDC_REDIRECT_BASE_URL) — explicit operator override.
//     Required for path-prefix deploys and any case where forwarded headers
//     don't reflect the public URL the IdP will see.
//  2. X-Forwarded-Proto + X-Forwarded-Host on the request, if the request is
//     coming from a trusted proxy peer (BINDERY_TRUSTED_PROXY). Honors the
//     same trust boundary as proxy-auth mode.
//  3. r.Host with scheme inferred from r.TLS. Used when Bindery is reached
//     directly without a proxy.
//
// X-Forwarded-Proto is NEVER honoured outside the trusted-proxy branch — when
// Bindery is reachable directly, an attacker can set the header from a
// browser fetch and would otherwise be able to influence the redirect_uri
// scheme (e.g. forcing an https → http downgrade for the OIDC callback).
// r.TLS is the only safe signal in that case.
//
// Returns the empty string only when the request has no Host header at all
// (which would already have been rejected upstream). Trailing slashes are
// stripped; the caller appends the path.
func ResolveOIDCRedirectBase(r *http.Request, configured string, trusted []*net.IPNet) string {
	if configured != "" {
		return strings.TrimRight(configured, "/")
	}

	if len(trusted) > 0 && requestFromTrustedProxy(r, trusted) {
		xfh := r.Header.Get("X-Forwarded-Host")
		xfp := r.Header.Get("X-Forwarded-Proto")
		if xfh != "" && xfp != "" {
			return xfp + "://" + strings.TrimRight(xfh, "/")
		}
	}

	// Direct-reach path: only trust r.TLS for scheme. XFP from an untrusted
	// peer is attacker-controlled (see CVE-style scheme-downgrade scenario in
	// the function doc). trustedProxyMiddleware also strips XFP from untrusted
	// peers when wired in cmd/bindery, but this function is callable in
	// isolation (and tested in isolation) so it must stay safe on its own.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + strings.TrimRight(r.Host, "/")
}

// requestFromTrustedProxy reports whether the request's immediate peer (the
// last hop) is in the configured proxy CIDR list. Mirrors the trust check in
// internal/auth/middleware.go but lives here to avoid a circular import.
func requestFromTrustedProxy(r *http.Request, trusted []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, c := range trusted {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}
