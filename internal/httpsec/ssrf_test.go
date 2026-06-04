package httpsec

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeResolver implements Resolver with a static host→IP map for tests.
type fakeResolver struct {
	m map[string][]net.IP
}

func (f fakeResolver) LookupIP(host string) ([]net.IP, error) {
	ips, ok := f.m[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return ips, nil
}

func mustIP(s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil {
		panic("bad IP: " + s)
	}
	return ip
}

func validate(raw string, policy Policy, resolver Resolver) error {
	return validateWithResolver(raw, policy, resolver)
}

func TestValidateOutboundURL_Scheme(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{"example.com": {mustIP("93.184.216.34")}}}

	// ftp:// rejected
	if err := validate("ftp://example.com/file", PolicyStrict, r); err == nil {
		t.Error("expected error for ftp scheme")
	}
	// file:// rejected
	if err := validate("file:///etc/passwd", PolicyStrict, r); err == nil {
		t.Error("expected error for file scheme")
	}
	// https:// allowed
	if err := validate("https://example.com/hook", PolicyStrict, r); err != nil {
		t.Errorf("unexpected error for https: %v", err)
	}
	// http:// allowed
	if err := validate("http://example.com/hook", PolicyStrict, r); err != nil {
		t.Errorf("unexpected error for http: %v", err)
	}
}

func TestValidateOutboundURL_Loopback(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	cases := []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
	}
	for _, u := range cases {
		if err := validate(u, PolicyStrict, r); err == nil {
			t.Errorf("expected block for loopback %q", u)
		}
		if err := validate(u, PolicyLAN, r); err == nil {
			t.Errorf("expected block for loopback %q (LAN policy)", u)
		}
		// PolicyLANLoopback is the admin-infra policy and must ALLOW loopback.
		if err := validate(u, PolicyLANLoopback, r); err != nil {
			t.Errorf("LANLoopback: unexpected block for loopback %q: %v", u, err)
		}
	}
}

// TestValidateOutboundURL_LANLoopbackPolicy pins the admin-infra policy: it adds
// loopback to PolicyLAN (loopback + RFC1918 allowed) but must still block
// link-local and cloud metadata.
func TestValidateOutboundURL_LANLoopbackPolicy(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	allowed := []string{
		"http://127.0.0.1:50155/api", // SAB on loopback (issue this fixes)
		"http://[::1]:9117/",
		"http://192.168.1.10:8080/", // RFC1918 still fine
		"http://10.0.0.5/",
	}
	for _, u := range allowed {
		if err := validate(u, PolicyLANLoopback, r); err != nil {
			t.Errorf("LANLoopback: expected allow for %q, got: %v", u, err)
		}
	}

	blocked := []string{
		"http://169.254.0.1/",            // link-local
		"http://[fe80::1]/",              // link-local v6
		"http://169.254.169.254/latest/", // cloud metadata
	}
	for _, u := range blocked {
		if err := validate(u, PolicyLANLoopback, r); err == nil {
			t.Errorf("LANLoopback: expected block for %q", u)
		}
	}
}

func TestValidateOutboundURL_IPv6MappedLoopback(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}
	// ::ffff:127.0.0.1 is an IPv4-mapped IPv6 address for 127.0.0.1.
	u := "http://[::ffff:127.0.0.1]/"
	if err := validate(u, PolicyStrict, r); err == nil {
		t.Errorf("expected block for IPv4-mapped loopback %q", u)
	}
}

func TestValidateOutboundURL_RFC1918_Strict(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	cases := []string{
		"http://10.0.0.1/",
		"http://10.255.255.255/",
		"http://172.16.0.1/",
		"http://172.31.255.255/",
		"http://192.168.0.1/",
		"http://192.168.255.255/",
	}
	for _, u := range cases {
		if err := validate(u, PolicyStrict, r); err == nil {
			t.Errorf("strict: expected block for RFC1918 %q", u)
		}
		// LAN policy should allow RFC1918.
		if err := validate(u, PolicyLAN, r); err != nil {
			t.Errorf("LAN: unexpected block for RFC1918 %q: %v", u, err)
		}
	}
}

func TestValidateOutboundURL_LinkLocal(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	cases := []string{
		"http://169.254.0.1/",
		"http://169.254.100.50/",
		"http://[fe80::1]/",
		"http://[fe80::1%25eth0]/", // zone-ID variant
	}
	for _, u := range cases {
		if err := validate(u, PolicyStrict, r); err == nil {
			t.Errorf("strict: expected block for link-local %q", u)
		}
		if err := validate(u, PolicyLAN, r); err == nil {
			t.Errorf("LAN: expected block for link-local %q", u)
		}
	}
}

func TestValidateOutboundURL_CloudMetadata_IP(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	// AWS/Azure/DO IMDS
	if err := validate("http://169.254.169.254/latest/meta-data/", PolicyStrict, r); err == nil {
		t.Error("expected block for 169.254.169.254")
	}
	if err := validate("http://169.254.169.254/latest/meta-data/", PolicyLAN, r); err == nil {
		t.Error("LAN: expected block for 169.254.169.254")
	}
}

func TestValidateOutboundURL_CloudMetadata_IPv6(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	// AWS IMDSv6
	if err := validate("http://[fd00:ec2::254]/", PolicyStrict, r); err == nil {
		t.Error("expected block for fd00:ec2::254")
	}
}

func TestValidateOutboundURL_CloudMetadata_Hostname(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{
		// Doesn't matter — hostname check fires before DNS lookup.
		"metadata.google.internal": {mustIP("169.254.169.254")},
	}}
	if err := validate("http://metadata.google.internal/computeMetadata/v1/", PolicyStrict, r); err == nil {
		t.Error("expected block for metadata.google.internal")
	}
}

func TestValidateOutboundURL_IPv6ULA_Strict(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{}}

	cases := []string{
		"http://[fc00::1]/",
		"http://[fd00::1]/",
		"http://[fdab:cdef:1234::1]/",
	}
	for _, u := range cases {
		if err := validate(u, PolicyStrict, r); err == nil {
			t.Errorf("strict: expected block for IPv6 ULA %q", u)
		}
		// LAN policy should allow ULA (private, but within LAN).
		if err := validate(u, PolicyLAN, r); err != nil {
			t.Errorf("LAN: unexpected block for IPv6 ULA %q: %v", u, err)
		}
	}
}

func TestValidateOutboundURL_DNSRebinding(t *testing.T) {
	// A hostname that resolves to a private IP — simulates DNS rebinding.
	r := fakeResolver{m: map[string][]net.IP{
		"evil.example.com": {mustIP("192.168.1.1")},
	}}
	if err := validate("http://evil.example.com/hook", PolicyStrict, r); err == nil {
		t.Error("strict: expected block for DNS hostname resolving to RFC1918")
	}
	// LAN policy: RFC1918 allowed, so this resolves fine.
	if err := validate("http://evil.example.com/hook", PolicyLAN, r); err != nil {
		t.Errorf("LAN: unexpected block for RFC1918 DNS: %v", err)
	}
}

func TestValidateOutboundURL_DNSRebinding_Loopback(t *testing.T) {
	// DNS rebinding to loopback must be blocked by all policies.
	r := fakeResolver{m: map[string][]net.IP{
		"sneaky.example.com": {mustIP("127.0.0.1")},
	}}
	for _, p := range []Policy{PolicyStrict, PolicyLAN} {
		if err := validate("http://sneaky.example.com/", p, r); err == nil {
			t.Errorf("policy %d: expected block for DNS resolving to loopback", p)
		}
	}
}

func TestValidateOutboundURL_PublicAllowed(t *testing.T) {
	r := fakeResolver{m: map[string][]net.IP{
		"hooks.example.com": {mustIP("93.184.216.34")},
	}}
	if err := validate("https://hooks.example.com/notify", PolicyStrict, r); err != nil {
		t.Errorf("unexpected block for public IP: %v", err)
	}
}

// TestValidateOutboundURL_Exported exercises the public entrypoint, which in
// turn drives the live netResolver. "localhost" is resolved from the local
// hosts file on every supported platform, so this runs without network access.
func TestValidateOutboundURL_Exported(t *testing.T) {
	// Live resolver should resolve "localhost" to 127.0.0.1 / ::1 and the
	// loopback check should reject it under both policies.
	if err := ValidateOutboundURL("http://localhost/", PolicyStrict); err == nil {
		t.Error("expected block for localhost under strict")
	}
	if err := ValidateOutboundURL("http://localhost/", PolicyLAN); err == nil {
		t.Error("expected block for localhost under LAN")
	}
}

func TestValidateOutboundURL_BadURL(t *testing.T) {
	// url.Parse rejects control chars in the URL.
	if err := ValidateOutboundURL("http://example.com/\x7f", PolicyStrict); err == nil {
		t.Error("expected error for malformed URL")
	}
	// Missing host.
	if err := ValidateOutboundURL("http:///path", PolicyStrict); err == nil {
		t.Error("expected error for URL without host")
	}
}

func TestValidateOutboundURL_DNSFailure(t *testing.T) {
	// An unreachable/nonexistent hostname must be rejected (fail-closed).
	r := fakeResolver{m: map[string][]net.IP{}}
	if err := validate("http://nope.invalid/", PolicyStrict, r); err == nil {
		t.Error("expected DNS failure to reject the URL")
	}
}

func TestPolicyFromEnv(t *testing.T) {
	const key = "BINDERY_TEST_SSRF_POLICY"

	// Unset → default preserved.
	t.Setenv(key, "")
	if got := PolicyFromEnv(PolicyStrict, key); got != PolicyStrict {
		t.Errorf("unset: want PolicyStrict, got %v", got)
	}

	// "1" → LAN.
	t.Setenv(key, "1")
	if got := PolicyFromEnv(PolicyStrict, key); got != PolicyLAN {
		t.Errorf("'1': want PolicyLAN, got %v", got)
	}

	// "true" (case-insensitive) → LAN.
	t.Setenv(key, "TRUE")
	if got := PolicyFromEnv(PolicyStrict, key); got != PolicyLAN {
		t.Errorf("'TRUE': want PolicyLAN, got %v", got)
	}

	// Any other value → default.
	t.Setenv(key, "maybe")
	if got := PolicyFromEnv(PolicyStrict, key); got != PolicyStrict {
		t.Errorf("'maybe': want PolicyStrict, got %v", got)
	}
}

// TestValidateIP exercises the exported thin wrapper around checkIP.
func TestValidateIP(t *testing.T) {
	// Public IP: always allowed.
	if err := ValidateIP(mustIP("93.184.216.34"), PolicyStrict); err != nil {
		t.Errorf("public IP blocked unexpectedly: %v", err)
	}
	// Loopback: always blocked.
	if err := ValidateIP(mustIP("127.0.0.1"), PolicyStrict); err == nil {
		t.Error("expected loopback to be blocked")
	}
	if err := ValidateIP(mustIP("127.0.0.1"), PolicyLAN); err == nil {
		t.Error("expected loopback to be blocked under LAN policy")
	}
	// Cloud metadata: always blocked.
	if err := ValidateIP(mustIP("169.254.169.254"), PolicyLAN); err == nil {
		t.Error("expected cloud-metadata IP to be blocked under LAN policy")
	}
	// RFC1918: blocked under Strict, allowed under LAN.
	if err := ValidateIP(mustIP("192.168.1.1"), PolicyStrict); err == nil {
		t.Error("expected RFC1918 to be blocked under Strict")
	}
	if err := ValidateIP(mustIP("192.168.1.1"), PolicyLAN); err != nil {
		t.Errorf("expected RFC1918 to be allowed under LAN, got: %v", err)
	}
}

// TestNewDialContext_BlocksLoopback verifies the custom dialer rejects a
// connection whose target resolves to loopback under all policies.
func TestNewDialContext_BlocksLoopback(t *testing.T) {
	// Start a real server on loopback so we have a valid port to dial.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Build an http.Client that uses the hardened dialer.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: NewDialContext(PolicyStrict),
		},
	}

	// srv.URL is "http://127.0.0.1:<port>" — loopback, always blocked.
	_, err := client.Get(srv.URL) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected dial to be rejected for loopback address, got nil error")
		return
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected 'loopback' in error, got: %v", err)
	}
}

// TestNewDialContext_AllowsPublicHosts verifies the custom dialer does not
// inject a policy error for a public IP literal. We use an immediately-expired
// context so the TCP connect never blocks; the important assertion is that the
// error is a context error, not a policy rejection.
func TestNewDialContext_AllowsPublicHosts(t *testing.T) {
	dialCtx := NewDialContext(PolicyLAN)
	// Cancel the context immediately so the TCP handshake never blocks, but the
	// policy check (which runs before the dial) has already completed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := dialCtx(ctx, "tcp", "93.184.216.34:80")
	// A policy rejection would contain "url not allowed"; a context/network
	// error is acceptable and expected here.
	if err != nil && strings.Contains(err.Error(), "url not allowed") {
		t.Errorf("public IP should not be rejected by policy, got: %v", err)
	}
}

// TestNewDialContext_BlocksRFC1918UnderStrict verifies a dial to an RFC1918
// address is blocked by PolicyStrict but would be allowed by PolicyLAN (the
// block is verified; the allow is not dialed since we have no LAN server).
func TestNewDialContext_BlocksRFC1918UnderStrict(t *testing.T) {
	dialCtx := NewDialContext(PolicyStrict)
	ctx := context.Background()
	_, err := dialCtx(ctx, "tcp", "192.168.1.1:80")
	if err == nil {
		t.Fatal("expected dial to RFC1918 to be rejected under PolicyStrict")
		return
	}
	if !strings.Contains(err.Error(), "private network") {
		t.Errorf("expected 'private network' in error, got: %v", err)
	}
}
