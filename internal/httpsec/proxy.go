package httpsec

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Outbound-proxy state. All of these are written once during startup by
// ConfigureOutboundProxy — before any outbound HTTP client is constructed or
// any request is served — and treated as read-only thereafter, so no locking
// is required.
var (
	// outboundProxyURL is the parsed BINDERY_OUTBOUND_PROXY target, or nil when
	// no outbound proxy is configured.
	outboundProxyURL *url.URL
	// outboundProxyTransport is the shared RoundTripper handed to clients that
	// would otherwise rely on http.DefaultTransport. It defaults to
	// http.DefaultTransport (i.e. unchanged behaviour and the same shared pool)
	// until ConfigureOutboundProxy installs a proxy-applying clone.
	outboundProxyTransport http.RoundTripper = http.DefaultTransport
	// proxyBypassLocal, when true, sends requests to LAN / loopback / single-label
	// destinations directly rather than through the proxy.
	proxyBypassLocal bool
	// proxyNoProxy holds normalised host/domain/CIDR rules that are always sent
	// direct, regardless of proxyBypassLocal.
	proxyNoProxy []string
)

// ConfigureOutboundProxy parses raw (e.g. "http://user:pass@host:3128" or
// "socks5://host:1080") and records it as the process-wide outbound proxy used
// by ProxyFunc and DefaultProxyTransport. An empty raw disables proxying and
// returns (nil, nil).
//
// The scheme must be http, https, or socks5 — all dialled natively by
// net/http's transport — and the URL must include a host; otherwise the proxy
// is left disabled and a non-nil error is returned. Credentials, when present,
// travel in the URL userinfo.
//
// bypassLocal sends LAN / loopback / single-label destinations direct (matching
// Sonarr's "bypass proxy for local addresses"); noProxy is a comma-separated
// list of additional hosts, domain suffixes, or CIDRs that are always sent
// direct. Both let local indexers / download managers (e.g. a LAN Jackett /
// Prowlarr) stay reachable while genuinely-remote egress still flows through the
// proxy.
//
// Call once during startup, before the outbound HTTP clients are constructed.
func ConfigureOutboundProxy(raw, noProxy string, bypassLocal bool) (*url.URL, error) {
	proxyBypassLocal = bypassLocal
	proxyNoProxy = parseNoProxy(noProxy)

	raw = strings.TrimSpace(raw)
	if raw == "" {
		outboundProxyURL = nil
		outboundProxyTransport = http.DefaultTransport
		return nil, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		// Do not wrap err: url.Parse embeds the full raw URL (including any
		// cleartext password from the userinfo) in its error string, and this
		// error is logged at startup. Return a constant message instead.
		return nil, errors.New("invalid proxy url: malformed")
	}
	if u.Host == "" {
		return nil, errors.New("invalid proxy url: missing host")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http, https, or socks5)", u.Scheme)
	}

	outboundProxyURL = u
	// Clone the default transport so every DefaultProxyTransport() consumer keeps
	// sharing one connection pool, then point it at the per-request resolver.
	// Proxy is evaluated per request, which is what makes the bypass rules and
	// native socks5 dialing work.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = proxyResolver
	outboundProxyTransport = t
	return u, nil
}

// proxyResolver is the http.Transport.Proxy hook. It returns the configured
// proxy for a request, or nil (direct) when no proxy is set or the destination
// is bypassed.
func proxyResolver(req *http.Request) (*url.URL, error) {
	if outboundProxyURL == nil {
		return nil, nil
	}
	if bypassProxyForHost(req.URL.Hostname()) {
		return nil, nil
	}
	return outboundProxyURL, nil
}

// ProxyFunc returns a function suitable for http.Transport.Proxy, or nil when
// no outbound proxy is configured. Assigning the nil result to a transport's
// Proxy field leaves it proxy-less, matching the pre-proxy default. Use this
// for clients that build their own *http.Transport (e.g. the indexer client)
// and must keep their custom DialContext / connection pooling while still
// honouring the proxy (and its bypass rules).
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	if outboundProxyURL == nil {
		return nil
	}
	return proxyResolver
}

// DefaultProxyTransport returns the shared RoundTripper for clients that would
// otherwise rely on http.DefaultTransport. When no proxy is configured it is
// http.DefaultTransport itself (unchanged behaviour, shared pool); when a proxy
// is configured it is a single shared clone with the proxy applied, so all such
// clients continue to share one connection pool.
func DefaultProxyTransport() http.RoundTripper {
	return outboundProxyTransport
}

// GuardedTransport returns a RoundTripper that re-validates the resolved IP on
// every dial against policy, closing the DNS-rebind TOCTOU window between an
// up-front ValidateOutboundURL check and the actual connect. For clients that
// follow redirects it also catches a redirect to a forbidden host at dial time.
//
// When an outbound proxy is configured the dial targets the operator-trusted
// proxy (not the destination host), so a per-dial recheck would re-validate the
// proxy's own address and break a LAN/loopback proxy; in that case the shared
// proxy transport is returned unchanged and callers rely on the up-front
// validation (the rebind recheck cannot see past the proxy anyway). It clones
// DefaultProxyTransport so installing DialContext never mutates the shared pool.
func GuardedTransport(policy Policy) http.RoundTripper {
	base := DefaultProxyTransport()
	if ProxyFunc() != nil {
		return base
	}
	if t, ok := base.(*http.Transport); ok {
		c := t.Clone()
		c.DialContext = NewDialContext(policy)
		return c
	}
	return &http.Transport{DialContext: NewDialContext(policy)}
}

// bypassProxyForHost reports whether requests to host should skip the proxy and
// be dialled directly. The no-proxy list is always honoured; the LAN/loopback
// heuristics apply only when bypassLocal is enabled.
func bypassProxyForHost(host string) bool {
	if host == "" {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))

	for _, rule := range proxyNoProxy {
		if rule == "*" || matchNoProxyRule(rule, h) {
			return true
		}
	}

	if !proxyBypassLocal {
		return false
	}

	// IP literals: bypass private / loopback / link-local / ULA ranges.
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}
	// Hostnames: localhost, common LAN suffixes, and single-label names (e.g.
	// Docker service names like "jackett" / "prowlarr") are treated as local.
	if h == "localhost" ||
		strings.HasSuffix(h, ".local") ||
		strings.HasSuffix(h, ".lan") ||
		strings.HasSuffix(h, ".internal") ||
		!strings.Contains(h, ".") {
		return true
	}
	return false
}

// matchNoProxyRule matches a single normalised no-proxy rule against host. A
// rule may be a CIDR ("10.0.0.0/8"), an exact host, or a domain suffix
// ("example.com" / ".example.com", which also matches sub.example.com).
func matchNoProxyRule(rule, host string) bool {
	if strings.Contains(rule, "/") {
		if _, ipNet, err := net.ParseCIDR(rule); err == nil {
			if ip := net.ParseIP(host); ip != nil {
				return ipNet.Contains(ip)
			}
		}
		return false
	}
	r := strings.TrimPrefix(rule, ".")
	return host == r || strings.HasSuffix(host, "."+r)
}

// isPrivateIP reports whether ip is loopback, link-local, RFC1918, or IPv6 ULA.
func isPrivateIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || isRFC1918(ip) || isIPv6ULA(ip)
}

// parseNoProxy splits a comma-separated no-proxy list into normalised, lowercase
// rules with empties removed.
func parseNoProxy(raw string) []string {
	var rules []string
	for _, part := range strings.Split(raw, ",") {
		if r := strings.ToLower(strings.TrimSpace(part)); r != "" {
			rules = append(rules, r)
		}
	}
	return rules
}
