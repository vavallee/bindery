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

// checkIP returns an error if ip is in a range forbidden by policy.
func checkIP(ip net.IP, policy Policy) error {
	// Unmap IPv4-mapped IPv6 (e.g. ::ffff:127.0.0.1 → 127.0.0.1) so the
	// IPv4 range checks below apply regardless of how the kernel returns them.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// Always blocked: loopback.
	if ip.IsLoopback() {
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
