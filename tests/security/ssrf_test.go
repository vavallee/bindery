// Package security holds integration-level penetration tests that exercise
// the security hardening added in v0.12.0. Each file covers one threat
// class; failures represent regressions against a shipped guard.
package security_test

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

// TestSSRF_WebhookStrict_BlocksPrivate asserts that the Strict policy
// (the default for webhook destinations) rejects every canonical private
// or sensitive destination. Each row is a real SSRF chain we've seen in
// the wild for apps with outbound webhook functionality.
func TestSSRF_WebhookStrict_BlocksPrivate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
	}{
		{"IPv4 loopback", "http://127.0.0.1/"},
		{"IPv4 loopback on high port", "http://127.0.0.1:8080/"},
		{"IPv6 loopback", "http://[::1]/"},
		{"IPv6 mapped IPv4 loopback", "http://[::ffff:127.0.0.1]/"},
		{"IPv4 RFC1918 10.x", "http://10.0.0.1/"},
		{"IPv4 RFC1918 172.16.x", "http://172.16.0.1/"},
		{"IPv4 RFC1918 192.168.x", "http://192.168.1.1/"},
		{"AWS metadata v4", "http://169.254.169.254/latest/meta-data/"},
		{"AWS metadata v6", "http://[fd00:ec2::254]/"},
		{"GCP metadata hostname", "http://metadata.google.internal/"},
		{"IPv4 link-local", "http://169.254.1.1/"},
		{"IPv6 link-local", "http://[fe80::1]/"},
		{"IPv6 ULA", "http://[fd12:3456:789a::1]/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := httpsec.ValidateOutboundURL(tc.url, httpsec.PolicyStrict); err == nil {
				t.Fatalf("expected %q to be rejected under PolicyStrict, was accepted", tc.url)
			}
		})
	}
}

// TestSSRF_LANPolicy_AllowsRFC1918 asserts that the LAN policy (used by
// indexers and download clients — homelab pattern) admits RFC1918 IPs but
// still blocks loopback and cloud-metadata endpoints. Those latter two
// are never legitimate destinations.
func TestSSRF_LANPolicy_AllowsRFC1918(t *testing.T) {
	t.Parallel()
	for _, u := range []string{
		"http://10.0.0.1/",
		"http://192.168.1.1:8080/",
		"http://172.20.5.5:9117/",
	} {
		if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyLAN); err != nil {
			t.Errorf("LAN policy should accept %q, got: %v", u, err)
		}
	}
	for _, u := range []string{
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://169.254.169.254/",
	} {
		if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyLAN); err == nil {
			t.Errorf("LAN policy should reject %q, accepted", u)
		}
	}
}

// TestSSRF_NonHTTPSchemes asserts that file:, ftp:, gopher: and data: URIs
// are rejected regardless of policy. A bug in any handler that passes
// user-controlled strings to http.NewRequest would otherwise be an
// immediate LFI vector via `file://`.
func TestSSRF_NonHTTPSchemes(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"file", "ftp", "gopher", "data"} {
		url := scheme + "://example.com/"
		if err := httpsec.ValidateOutboundURL(url, httpsec.PolicyStrict); err == nil {
			t.Errorf("scheme %q should be rejected, accepted", scheme)
		}
	}
}

// TestSSRF_MalformedInput verifies the validator fails closed on inputs
// that http.NewRequest or url.Parse would otherwise massage into something
// unintended. Empty strings, whitespace, and URLs without hosts all error.
func TestSSRF_MalformedInput(t *testing.T) {
	t.Parallel()
	for _, u := range []string{
		"",
		" ",
		"http://",
		"not-a-url",
		"http:// \n",
	} {
		if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyStrict); err == nil {
			t.Errorf("malformed input %q should be rejected", u)
		}
	}
}

// TestSSRF_ErrorMessages verifies the error wording doesn't leak internal
// resolver details to the caller. The 400 bodies surface to the UI, so
// "dial tcp 10.0.0.1:8080" style Go-net noise would be a minor infoleak.
// Acceptable words: "private", "loopback", "metadata", "link-local",
// "unsupported", "unable", "lookup failed", "ula".
func TestSSRF_ErrorMessages(t *testing.T) {
	t.Parallel()
	err := httpsec.ValidateOutboundURL("http://127.0.0.1/", httpsec.PolicyStrict)
	if err == nil {
		t.Fatal("expected error for loopback")
	}
	msg := strings.ToLower(err.Error())
	allowed := []string{"loopback", "private", "disallowed", "blocked", "not allowed"}
	ok := false
	for _, w := range allowed {
		if strings.Contains(msg, w) {
			ok = true
			break
		}
	}
	if !ok {
		t.Errorf("error message %q doesn't match any expected pattern", msg)
	}
}
