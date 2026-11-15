package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// realIP returns the client IP for rate limiting. Forwarding headers are only
// believed when the TCP peer is inside the trusted proxy list
// (TRUSTED_PROXIES). For a trusted peer it prefers X-Real-Ip set by Traefik
// (which reflects the actual downstream address regardless of any
// X-Forwarded-For the client may have injected), then the rightmost address in
// X-Forwarded-For (the one appended by the closest trusted proxy). Never uses
// the leftmost X-Forwarded-For value, which a client can spoof freely. With no
// trusted proxies, or an untrusted peer, the TCP RemoteAddr is the client.
func realIP(r *http.Request, trusted []*net.IPNet) string {
	// RemoteAddr is "IP:port"; strip the port so each unique client IP gets
	// one rate-limit bucket regardless of which ephemeral port it connects from.
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if !ipInNets(peer, trusted) {
		return peer
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	return peer
}

func ipInNets(host string, nets []*net.IPNet) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies parses a comma separated list of CIDRs or bare IPs.
// An entry that does not parse is logged and skipped rather than silently
// dropped: a typo here changes what the rate limiter keys on.
func parseTrustedProxies(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			if strings.Contains(e, ":") {
				e += "/128"
			} else {
				e += "/32"
			}
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			slog.Warn("TRUSTED_PROXIES: ignoring invalid entry", "entry", e)
			continue
		}
		out = append(out, n)
	}
	return out
}

// rateLimiter is a simple in-memory token bucket: one ping per window per IP.
type rateLimiter struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	window  time.Duration
	cleanup time.Duration
}

func newRateLimiter(window, cleanup time.Duration) *rateLimiter {
	rl := &rateLimiter{
		seen:    make(map[string]time.Time),
		window:  window,
		cleanup: cleanup,
	}
	go rl.purge()
	return rl
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if last, ok := rl.seen[ip]; ok && time.Since(last) < rl.window {
		return false
	}
	rl.seen[ip] = time.Now()
	return true
}

func (rl *rateLimiter) purge() {
	for range time.Tick(rl.cleanup) {
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.window)
		for ip, t := range rl.seen {
			if t.Before(cutoff) {
				delete(rl.seen, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// resolveTrustedProxies turns the TRUSTED_PROXIES value into the peer list
// whose forwarding headers are believed. Unset or empty means the private
// defaults, "none" means believe nobody (TCP peer only), anything else is an
// explicit list that replaces the defaults.
func resolveTrustedProxies(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return defaultTrustedProxies
	case strings.EqualFold(raw, "none"):
		return nil
	}
	return parseTrustedProxies(raw)
}

// defaultTrustedProxies are loopback, RFC1918, link-local and ULA ranges: a
// reverse proxy in front of the ping server normally sits on one of them, so
// the server works behind Traefik with no configuration. The tradeoff is that
// a client on one of these networks could choose its own rate-limit bucket if
// the server were ever exposed directly on a private network; that is a rate
// limit bypass only. Set TRUSTED_PROXIES=none to refuse every forwarding header.
var defaultTrustedProxies = parseTrustedProxies("127.0.0.0/8, ::1/128, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10")
