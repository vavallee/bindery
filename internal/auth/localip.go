package auth

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// privateCIDRs: RFC1918 + loopback + link-local + IPv6 unique-local + IPv6 loopback.
// Matches what Sonarr considers "local" for the "Disabled for Local Addresses" mode.
var privateCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16", // link-local v4
		"::1/128",
		"fc00::/7",  // ULA v6
		"fe80::/10", // link-local v6
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// realPeerCtxKey carries the connection's true peer address (host:port or bare
// host) as observed before any X-Forwarded-* rewriting (chi's RealIP) ran.
// trustedProxyMiddleware stashes it so the local-only trust decision can start
// its right-to-left X-Forwarded-For walk from the actual TCP peer rather than
// from a value an attacker may control.
type realPeerCtxKeyT struct{}

var realPeerCtxKey = realPeerCtxKeyT{}

// WithRealPeer returns a context carrying the request's true peer address.
// Call this from the proxy middleware *before* chi's RealIP overwrites
// r.RemoteAddr, passing the original r.RemoteAddr.
func WithRealPeer(ctx context.Context, remoteAddr string) context.Context {
	return context.WithValue(ctx, realPeerCtxKey, remoteAddr)
}

// realPeerHost returns the bare host of the request's true TCP peer. It
// prefers the value stashed by WithRealPeer (the address before RealIP
// rewriting); if that is absent it falls back to r.RemoteAddr. The fallback is
// only safe when no proxy rewriting happened — see ResolveClientIP.
func realPeerHost(r *http.Request) string {
	addr := r.RemoteAddr
	if v, ok := r.Context().Value(realPeerCtxKey).(string); ok && v != "" {
		addr = v
	}
	return hostOnly(addr)
}

// hostOnly strips an optional :port and surrounding brackets from an address.
func hostOnly(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// IsLocalRequest reports whether the request's *real* client IP is in a
// private/loopback range, for the local-only auth bypass. It resolves the
// client IP from the true TCP peer and X-Forwarded-For, trusting only the
// proxies configured via BINDERY_TRUSTED_PROXY. It never trusts a
// client-supplied leftmost X-Forwarded-For entry (chi RealIP's pick), so a
// remote client cannot spoof a private IP to bypass local-only auth.
func IsLocalRequest(r *http.Request) bool {
	return IsLocalRequestTrusted(r, envTrustedProxyCIDRs())
}

// IsLocalRequestTrusted is IsLocalRequest with an explicit trusted-proxy CIDR
// set. Callers that already hold the parsed CIDR list (e.g. the auth Provider)
// should use this to avoid re-parsing the environment.
func IsLocalRequestTrusted(r *http.Request, trusted []*net.IPNet) bool {
	return IsLocalIP(ResolveClientIP(r, trusted))
}

// ResolveClientIP returns the request's real client IP for trust decisions.
//
// It deliberately does NOT use chi RealIP's leftmost X-Forwarded-For pick,
// which is attacker-controlled: a remote client can prepend a forged private
// IP and any proxy that does not strip inbound XFF will pass it through as the
// leftmost entry.
//
// Algorithm:
//   - The chain considered is [realPeer] + X-Forwarded-For entries, ordered
//     closest-to-server last. realPeer is the actual TCP peer (captured before
//     RealIP rewrote RemoteAddr).
//   - If no trusted proxy is configured, the peer is the client and XFF is
//     ignored entirely.
//   - If the peer itself is not a trusted proxy, the peer is the client and
//     its XFF header is not trusted.
//   - Otherwise walk the chain right-to-left, peeling off every hop that is a
//     trusted proxy; the first non-trusted address encountered is the real
//     client. If every hop is trusted, the left-most (outermost) hop is used.
//
// The returned string is a bare IP, or "" if no usable address is found.
func ResolveClientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := realPeerHost(r)

	// No trusted proxy configured: the TCP peer is the client, full stop.
	// Any X-Forwarded-For header is untrusted and ignored.
	if len(trusted) == 0 {
		return peer
	}

	peerIP := net.ParseIP(peer)
	// The peer is not one of our proxies: it is talking to us directly, so it
	// is the client. Do not trust an XFF header it supplied.
	if peerIP == nil || !ipInCIDRs(peerIP, trusted) {
		return peer
	}

	// Peer is a trusted proxy. Reconstruct the forwarding chain. X-Forwarded-For
	// is appended left-to-right (oldest first), so the rightmost entries are the
	// closest hops. The TCP peer sits one hop further in than the rightmost XFF
	// entry. Walk right-to-left, discarding trusted-proxy hops; the first
	// non-trusted hop is the genuine client.
	xff := parseXFF(r.Header.Values("X-Forwarded-For"))
	for i := len(xff) - 1; i >= 0; i-- {
		ip := net.ParseIP(xff[i])
		if ip == nil {
			// An unparseable hop breaks the chain of trust: we can no longer
			// account for who is upstream of it. Treat it as the client so the
			// decision fails closed (a garbage entry will not be "local").
			return xff[i]
		}
		if !ipInCIDRs(ip, trusted) {
			return xff[i]
		}
	}

	// Every XFF hop was a trusted proxy (or XFF was empty). The client is the
	// outermost entry if present, otherwise the trusted peer itself.
	if len(xff) > 0 {
		return xff[0]
	}
	return peer
}

// parseXFF flattens one or more X-Forwarded-For header values into an ordered
// list of trimmed host strings (oldest hop first), dropping empty entries.
func parseXFF(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// XFF may carry "host:port" (rare) or bracketed IPv6 — normalise.
			out = append(out, hostOnly(part))
		}
	}
	return out
}

// ipInCIDRs reports whether ip falls within any of the given CIDRs.
func ipInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, n := range cidrs {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLocalIP returns true for RFC1918, loopback, and link-local addresses.
func IsLocalIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range privateCIDRs {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// envTrustedProxyCIDRs parses BINDERY_TRUSTED_PROXY once. It mirrors
// cmd/bindery.parseTrustedProxyCIDRs; that one cannot be imported (main
// package), and the env var is the single source of truth for both.
var envTrustedProxyCIDRs = sync.OnceValue(func() []*net.IPNet {
	return ParseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))
})

// ParseTrustedProxyCIDRs parses a comma-separated list of IP/CIDR strings into
// []*net.IPNet. Bare IPs become /32 (IPv4) or /128 (IPv6). Invalid entries are
// skipped silently. Exported so callers (and tests) share one parser.
func ParseTrustedProxyCIDRs(raw string) []*net.IPNet {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []*net.IPNet
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
		if _, cidr, err := net.ParseCIDR(s); err == nil {
			out = append(out, cidr)
		}
	}
	return out
}
