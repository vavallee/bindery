// Package httpsec provides outbound-URL validation to prevent SSRF attacks.
package httpsec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// Policy controls which destinations are permitted for outbound requests.
type Policy int

const (
	// PolicyStrict blocks loopback, RFC1918, link-local, cloud-metadata, and
	// IPv6 ULA. Appropriate for webhooks that leave the local network.
	PolicyStrict Policy = iota

	// PolicyLAN blocks only loopback, link-local, and cloud-metadata.
	// RFC1918 addresses are allowed so homelabs can target LAN services
	// (indexers, download clients, ntfy).
	PolicyLAN

	// PolicyLANLoopback is PolicyLAN plus loopback (127.0.0.0/8, ::1). It is the
	// least restrictive policy and still blocks link-local and cloud metadata.
	// Use it ONLY for admin-configured infrastructure URLs the operator types
	// themselves (download clients, indexers, Prowlarr, the Calibre plugin / ABS
	// base URLs). Reaching a co-located service over loopback is the normal case
	// under `network_mode: host` or when the companion binds to 127.0.0.1, and
	// these endpoints are admin-only + CSRF-gated, so the loopback block bought
	// ~no security while breaking a legitimate topology. Never use this for URLs
	// influenced by untrusted input (cover/image proxy, webhooks) — those keep
	// PolicyStrict / PolicyLAN.
	PolicyLANLoopback
)

// Resolver abstracts net.LookupIP so tests can inject a fake resolver without
// hitting real DNS.
type Resolver interface {
	LookupIP(host string) ([]net.IP, error)
}

// netResolver delegates to the standard library with a bounded context.
type netResolver struct{}

func (netResolver) LookupIP(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// defaultResolver is the live resolver used in production.
var defaultResolver Resolver = netResolver{}

// cloudMetadataHosts lists well-known metadata endpoints that must never be
// reachable from a webhook, regardless of policy.
var cloudMetadataHosts = map[string]bool{
	"metadata.google.internal": true,
}

// ValidateOutboundURL returns an error if raw should not be fetched under
// policy. It rejects non-http/https schemes, cloud-metadata hostnames, and
// (depending on policy) private / loopback IP ranges. All A/AAAA records for
// the hostname are resolved and checked — this defeats DNS rebinding attacks
// where a hostname initially resolves to a public IP but later flips to a
// private one.
func ValidateOutboundURL(raw string, policy Policy) error {
	return validateWithResolver(raw, policy, defaultResolver)
}

func validateWithResolver(raw string, policy Policy, r Resolver) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url not allowed: invalid url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("url not allowed: scheme must be http or https")
	}

	host := u.Hostname()
	if host == "" {
		return errors.New("url not allowed: missing host")
	}

	// Reject cloud-metadata hostnames before attempting DNS resolution.
	if cloudMetadataHosts[strings.ToLower(host)] {
		return errors.New("url not allowed: points to cloud metadata endpoint")
	}

	// If host is already an IP literal, validate it directly; otherwise resolve
	// all A/AAAA records (defeats DNS rebinding by checking every returned IP).
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip, policy)
	}

	ips, err := r.LookupIP(host)
	if err != nil {
		// On lookup failure, reject rather than allow (fail-closed).
		return fmt.Errorf("url not allowed: dns lookup failed: %w", err)
	}
	for _, ip := range ips {
		if err := checkIP(ip, policy); err != nil {
			return err
		}
	}
	return nil
}

// testAllowLoopback opts the loopback check out of "always blocked" status,
// scoped by AllowLoopbackForTests. Never set in production — the call sites
// for outbound fetches (notifications, indexers, NZB fetch) all rely on
// loopback being rejected to prevent SSRF against the Bindery host itself.
var testAllowLoopback bool

// AllowLoopbackForTests permits loopback addresses through ValidateOutboundURL
// for the lifetime of the returned cleanup. Tests that need to point a
// guarded outbound call at an httptest.NewServer (which binds 127.0.0.1) use
// this; production code must never call it. Idiomatic use:
//
//	defer httpsec.AllowLoopbackForTests()()
//
// Not safe for t.Parallel: the flag is package-global. Sequential tests are
// fine, including subtests, since each call snapshots and restores.
func AllowLoopbackForTests() func() {
	prev := testAllowLoopback
	testAllowLoopback = true
	return func() { testAllowLoopback = prev }
}

// checkIP returns an error if ip is in a range forbidden by policy.
func checkIP(ip net.IP, policy Policy) error {
	// Unmap IPv4-mapped IPv6 (e.g. ::ffff:127.0.0.1 → 127.0.0.1) so the
	// IPv4 range checks below apply regardless of how the kernel returns them.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// Loopback is blocked except under PolicyLANLoopback (admin-configured
	// infrastructure URLs, where reaching a co-located service over 127.0.0.1 is
	// the intended, normal case) or when explicitly allowed for tests via
	// AllowLoopbackForTests (never opt in from production code).
	if ip.IsLoopback() && policy != PolicyLANLoopback && !testAllowLoopback {
		return errors.New("url not allowed: points to loopback address")
	}

	// Always blocked: link-local (169.254/16, fe80::/10).
	if ip.IsLinkLocalUnicast() {
		return errors.New("url not allowed: points to link-local address")
	}

	// Always blocked: cloud metadata IPs.
	if isCloudMetadata(ip) {
		return errors.New("url not allowed: points to cloud metadata endpoint")
	}

	if policy == PolicyStrict {
		// Block RFC1918 private ranges.
		if isRFC1918(ip) {
			return errors.New("url not allowed: points to private network")
		}
		// Block IPv6 ULA (fc00::/7).
		if isIPv6ULA(ip) {
			return errors.New("url not allowed: points to private network")
		}
	}

	return nil
}

// isRFC1918 returns true for 10/8, 172.16/12, 192.168/16.
func isRFC1918(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	}
	return false
}

// isCloudMetadata returns true for known cloud-metadata IP addresses:
// 169.254.169.254 (AWS/Azure/DO) and fd00:ec2::254 (AWS IMDSv6).
func isCloudMetadata(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 169 && ip4[1] == 254 && ip4[2] == 169 && ip4[3] == 254
	}
	// fd00:ec2::254 in 16-byte form.
	ec2v6 := net.ParseIP("fd00:ec2::254")
	return ip.Equal(ec2v6)
}

// isIPv6ULA returns true for Unique Local Addresses in fc00::/7.
func isIPv6ULA(ip net.IP) bool {
	if ip.To4() != nil {
		return false // IPv4, handled separately
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return false
	}
	// fc00::/7 means the first 7 bits of the first byte are 1111110x.
	return ip16[0]&0xfe == 0xfc
}

// ValidateIP returns an error if ip is forbidden under policy. It applies the
// same rules as ValidateOutboundURL's per-IP checks (loopback, link-local,
// cloud-metadata, and RFC1918/ULA depending on policy). Callers that have
// already resolved a hostname to a net.IP — for example, a custom DialContext
// that intercepts the resolved address — can use this to enforce the policy at
// connection time without re-parsing a URL.
func ValidateIP(ip net.IP, policy Policy) error {
	return checkIP(ip, policy)
}

// NewDialContext returns a DialContext function that wraps net.Dialer and
// re-validates every resolved IP against policy before completing the TCP
// connection. This prevents DNS-rebinding attacks where a hostname initially
// resolves to a public IP (passing ValidateOutboundURL at config time) but
// later flips to a private address after the DNS TTL expires.
//
// The returned function preserves the caller's context and is safe for
// concurrent use. Pass it to http.Transport.DialContext.
func NewDialContext(policy Policy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// addr is "host:port" as provided by the HTTP transport.
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("dial: invalid address %q: %w", addr, err)
		}

		// If host is already an IP literal, validate it directly and dial.
		if ip := net.ParseIP(host); ip != nil {
			if err := checkIP(ip, policy); err != nil {
				return nil, fmt.Errorf("dial %s: %w", addr, err)
			}
			return dialer.DialContext(ctx, network, addr)
		}

		// Resolve the hostname so we can validate each returned IP before
		// handing control to the kernel's connect(2). This is the per-request
		// re-validation that defeats DNS rebinding: the OS may have cached an
		// old (allowed) IP while DNS now points at a private address.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("dial: dns lookup %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("dial: no addresses for %q", host)
		}

		// Validate every resolved address. Fail-closed: if any IP is forbidden
		// we refuse the entire dial rather than trying only the allowed ones,
		// because an attacker might arrange for one A record to be public and
		// another to be the target address.
		for _, ia := range ips {
			if err := checkIP(ia.IP, policy); err != nil {
				return nil, fmt.Errorf("dial %s: resolved %s: %w", host, ia.IP, err)
			}
		}

		// All resolved IPs passed; dial using the first one (standard behaviour).
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// PolicyFromEnv returns override if the given env variable is set to "1" or
// "true" (case-insensitive), otherwise returns def. This lets callers flip
// the strict default to LAN policy for users with on-LAN services.
//
// Example: PolicyFromEnv(PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
func PolicyFromEnv(def Policy, envVar string) Policy {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envVar)))
	if v == "1" || v == "true" {
		return PolicyLAN
	}
	return def
}
