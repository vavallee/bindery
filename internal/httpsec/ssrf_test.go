package httpsec

import (
	"errors"
	"net"
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
