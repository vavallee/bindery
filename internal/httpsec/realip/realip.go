// Package realip provides a hardened replacement for chi's deprecated
// middleware.RealIP. The upstream middleware blindly rewrites r.RemoteAddr
// from the leftmost X-Forwarded-For (or True-Client-IP / X-Real-IP) entry
// regardless of whether the immediate TCP peer is a trusted proxy, so any
// client on the open internet can forge an arbitrary RemoteAddr — see
// GHSA-3fxj-6jh8-hvhx, GHSA-rjr7-jggh-pgcp, GHSA-9g5q-2w5x-hmxf.
//
// TrustedRealIP only honours forwarded headers when the immediate peer is in
// a caller-supplied trusted-proxy CIDR set, and walks X-Forwarded-For
// right-to-left to find the first untrusted hop (the genuine client) instead
// of trusting the leftmost (attacker-controlled) entry.
package realip

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// TrustedRealIP returns a middleware that, when the immediate connection peer
// falls within trustedCIDRs, rewrites r.RemoteAddr to the rightmost value in
// X-Forwarded-For that is NOT in trustedCIDRs (the first untrusted hop walking
// back from the proxy chain). It falls back to True-Client-IP, then X-Real-IP,
// then leaves r.RemoteAddr alone.
//
// When the immediate peer is NOT in trustedCIDRs, forwarded headers are
// ignored — an open-internet client cannot spoof r.RemoteAddr.
//
// When trustedCIDRs is empty the middleware is a no-op. Mount it
// unconditionally; the caller does not need to gate at mount time.
//
// Why rightmost-untrusted instead of leftmost (the deprecated chi behaviour):
// X-Forwarded-For is appended left-to-right (oldest hop first). A remote
// client may prepend any value it likes; a trusted proxy at the edge will then
// append the client's true source IP and pass the whole list through. The
// leftmost entry is therefore attacker-controlled. Walking right-to-left and
// peeling off hops that are themselves trusted proxies yields the genuine
// client — the first hop we cannot vouch for. This matches the algorithm
// auth.ResolveClientIP uses for the local-only-auth trust decision so both
// code paths agree on "who is the client".
func TrustedRealIP(trustedCIDRs []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// No proxies configured: the TCP peer is the client by definition;
		// forwarded headers are untrusted and must not influence RemoteAddr.
		if len(trustedCIDRs) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peerHost, peerPort, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				// RemoteAddr without a port (httptest sometimes, or a custom
				// listener). Treat the whole string as the host; preserve
				// "no port" on rewrite.
				peerHost = r.RemoteAddr
				peerPort = ""
			}
			peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))
			if peerIP == nil || !ipInCIDRs(peerIP, trustedCIDRs) {
				// Peer is on the open internet (or unparseable). Forwarded
				// headers from it are untrusted: leave RemoteAddr alone.
				next.ServeHTTP(w, r)
				return
			}

			if client := clientFromXFF(r.Header.Values("X-Forwarded-For"), trustedCIDRs); client != "" {
				r.RemoteAddr = joinHostPort(client, peerPort)
				next.ServeHTTP(w, r)
				return
			}
			if tci := strings.TrimSpace(r.Header.Get("True-Client-IP")); tci != "" {
				if net.ParseIP(tci) != nil {
					r.RemoteAddr = joinHostPort(tci, peerPort)
					next.ServeHTTP(w, r)
					return
				}
				slog.Debug("realip: True-Client-IP unparseable, leaving RemoteAddr alone", "value", tci)
			}
			if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
				if net.ParseIP(xri) != nil {
					r.RemoteAddr = joinHostPort(xri, peerPort)
					next.ServeHTTP(w, r)
					return
				}
				slog.Debug("realip: X-Real-IP unparseable, leaving RemoteAddr alone", "value", xri)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientFromXFF walks the combined X-Forwarded-For chain right-to-left and
// returns the first hop that is NOT in trustedCIDRs (the genuine client). If
// every hop is trusted, returns the leftmost hop (the original client per
// standard XFF semantics). Returns "" when XFF is empty.
//
// An unparseable hop breaks the chain of trust: we cannot account for who is
// upstream of it, so we return that raw value and let the caller use it as
// the client (fail closed — a garbage entry will not look like a trusted
// private IP downstream).
func clientFromXFF(values []string, trustedCIDRs []*net.IPNet) string {
	hops := parseXFF(values)
	if len(hops) == 0 {
		return ""
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			return hops[i]
		}
		if !ipInCIDRs(ip, trustedCIDRs) {
			return hops[i]
		}
	}
	return hops[0]
}

// parseXFF flattens repeated X-Forwarded-For values and comma-separated
// entries into an ordered list of bare host strings (oldest hop first),
// dropping empty entries and stripping any host:port / [v6] decoration.
func parseXFF(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out = append(out, hostOnly(part))
		}
	}
	return out
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return strings.Trim(addr, "[]")
}

func ipInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, n := range cidrs {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// joinHostPort rebuilds an address string preserving the original peer's
// port. The deprecated chi RealIP stripped the port; we keep it so downstream
// callers doing net.SplitHostPort(r.RemoteAddr) continue to work. When the
// original RemoteAddr had no port (unusual but possible with custom
// listeners), we emit the bare host to match.
func joinHostPort(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
