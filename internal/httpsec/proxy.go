package httpsec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
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
// every direct dial against policy, closing the DNS-rebind TOCTOU window
// between an up-front ValidateOutboundURL check and the actual connect. For
// clients that follow redirects it also catches a redirect to a forbidden host
// at dial time.
//
// When an outbound proxy is configured, a dial to the operator-trusted proxy
// itself is not checked (a per-dial recheck would reject a LAN or loopback
// proxy). A dial to anything else is a direct connection, which is what happens
// for a destination on the proxy bypass list (bypassLocal or no-proxy), and
// that one is checked against policy exactly as on the no-proxy path. A
// destination that goes through the proxy is only seen as the proxy address, so
// for those the up-front validation remains the guard. It clones
// DefaultProxyTransport so installing DialContext never mutates the shared pool.
func GuardedTransport(policy Policy) http.RoundTripper {
	base := DefaultProxyTransport()
	if t, ok := base.(*http.Transport); ok {
		c := t.Clone()
		c.DialContext = guardedDialContext(policy)
		return c
	}
	return &http.Transport{DialContext: guardedDialContext(policy)}
}

// guardedDialContext is NewDialContext(policy), except that while an outbound
// proxy is configured a dial to the proxy's own address bypasses the policy.
func guardedDialContext(policy Policy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	guarded := NewDialContext(policy)
	if outboundProxyURL == nil {
		return guarded
	}
	plain := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if isProxyAddr(addr) {
			return plain(ctx, network, addr)
		}
		return guarded(ctx, network, addr)
	}
}

// isProxyAddr reports whether addr ("host:port") is the configured outbound
// proxy's own address. Scheme default ports apply when the proxy URL has none.
func isProxyAddr(addr string) bool {
	u := outboundProxyURL
	if u == nil {
		return false
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	pport := u.Port()
	if pport == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			pport = "443"
		case "socks5":
			pport = "1080"
		default:
			pport = "80"
		}
	}
	return strings.EqualFold(host, u.Hostname()) && port == pport
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

var (
	directGuardedMu sync.Mutex
	directGuarded   = map[Policy]*http.Transport{}
)

// DirectGuardedTransport returns the RoundTripper for admin-configured
// infrastructure that is always dialled directly: download clients and the
// Audiobookshelf client. It is a clone of http.DefaultTransport (so the
// standard HTTP_PROXY / NO_PROXY environment still applies, and
// BINDERY_OUTBOUND_PROXY does not: those endpoints are intentionally left
// direct) with a per-dial policy check, so a redirect hop or a hostname that
// resolves to a forbidden address is refused at connect time.
//
// One transport is shared per policy so every client keeps one connection pool,
// as http.DefaultTransport gave them before.
func DirectGuardedTransport(policy Policy) http.RoundTripper {
	directGuardedMu.Lock()
	defer directGuardedMu.Unlock()
	if t, ok := directGuarded[policy]; ok {
		return t
	}
	var t *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	} else {
		t = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t.DialContext = NewDialContext(policy)
	directGuarded[policy] = t
	return t
}

// RedirectGuard returns an http.Client.CheckRedirect that stops after 10 hops
// (net/http's own limit) and re-validates each hop's URL against policy, so a
// redirect off a control endpoint cannot be used to reach a link-local or
// cloud-metadata address. The error names the policy refusal.
//
// For PolicyService a hop is checked as PolicyLANLoopback, and the follow-up
// request is marked so its dial is too: the link-local allowance belongs to the
// admin-typed base URL, never to a redirect a remote service answers with.
func RedirectGuard(policy Policy) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		hop := policy
		if policy == PolicyService {
			hop = PolicyLANLoopback
		}
		if err := ValidateOutboundURL(req.URL.String(), hop); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
		if policy == PolicyService {
			// The client sends this very request next, so replacing its
			// contents carries the marker to the dial.
			*req = *req.WithContext(context.WithValue(req.Context(), redirectHopKey{}, true))
		}
		return nil
	}
}

// metadataIdleConnsPerHost is how many idle connections a metadata provider
// transport keeps per host. net/http keeps 2, so a burst of 8 parallel lookups
// to one provider (an author refresh fanning out) closes six connections when
// it ends and pays the TCP and TLS handshakes again on the next burst.
const metadataIdleConnsPerHost = 16

// MetadataTransport returns the RoundTripper for a metadata provider client
// (Open Library, Hardcover, Google Books and the like). It is a clone of
// DefaultProxyTransport, so the configured outbound proxy, its bypass rules and
// every other field of the base carry over, with a larger idle pool per host.
// The clone is shared by every caller, as http.DefaultTransport was: some
// providers are built per request (the Hardcover settings test, the list
// syncer's client factory), and a clone per call would give each of those its
// own idle pool, never reused and held open until the idle timeout. The
// shared http.DefaultTransport that DefaultProxyTransport may return is never
// mutated. If the base changes (ConfigureOutboundProxy installs a proxy), the
// next call clones the new base.
//
// These providers are fixed public endpoints and were never dial guarded, so
// the clone adds no guard and removes none; a DialContext on the base survives
// the clone. Callers that need a policy use GuardedTransport.
func MetadataTransport() http.RoundTripper {
	base := DefaultProxyTransport()
	t, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	metadataMu.Lock()
	defer metadataMu.Unlock()
	if metadataBase == base && metadataClone != nil {
		return metadataClone
	}
	c := t.Clone()
	c.MaxIdleConnsPerHost = metadataIdleConnsPerHost
	metadataBase, metadataClone = base, c
	return c
}

var (
	metadataMu    sync.Mutex
	metadataBase  http.RoundTripper
	metadataClone *http.Transport
)
