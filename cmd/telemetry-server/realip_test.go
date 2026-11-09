package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func ipReq(remote, xri, xff string) *http.Request {
	r := httptest.NewRequest("POST", "/ping", nil)
	r.RemoteAddr = remote
	if xri != "" {
		r.Header.Set("X-Real-Ip", xri)
	}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// With no trusted proxy configured the TCP peer is the client, whatever
// forwarding headers say, so a client cannot pick its own rate-limit bucket.
func TestRealIP_UntrustedPeerIgnoresForwardingHeaders(t *testing.T) {
	r := ipReq("203.0.113.9:4444", "198.51.100.1", "198.51.100.2, 198.51.100.3")
	if got := realIP(r, nil); got != "203.0.113.9" {
		t.Fatalf("no trusted list: realIP = %q, want the TCP peer 203.0.113.9", got)
	}
	// A configured list does not help a peer outside it.
	if got := realIP(r, parseTrustedProxies("10.0.0.0/8")); got != "203.0.113.9" {
		t.Fatalf("peer outside list: realIP = %q, want 203.0.113.9", got)
	}
}

// A peer inside a trusted CIDR keeps the previous behaviour: X-Real-Ip first,
// then the rightmost X-Forwarded-For, then the peer.
func TestRealIP_TrustedPeerUsesForwardingHeaders(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16, 192.0.2.7, junk")
	if len(trusted) != 2 {
		t.Fatalf("parsed %d entries, want 2 (junk skipped)", len(trusted))
	}
	cases := []struct {
		name, remote, xri, xff, want string
	}{
		{"x-real-ip wins", "10.42.1.5:1234", "198.51.100.1", "1.1.1.1, 198.51.100.2", "198.51.100.1"},
		{"rightmost xff", "10.42.1.5:1234", "", "1.1.1.1, 198.51.100.2", "198.51.100.2"},
		{"bare ip entry", "192.0.2.7:1", "198.51.100.4", "", "198.51.100.4"},
		{"no headers falls back to peer", "10.42.1.5:1234", "", "", "10.42.1.5"},
	}
	for _, c := range cases {
		if got := realIP(ipReq(c.remote, c.xri, c.xff), trusted); got != c.want {
			t.Errorf("%s: realIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// The default (TRUSTED_PROXIES unset) must work behind a reverse proxy on a
// private network without any configuration, and must not believe a public peer.
func TestRealIP_DefaultTrustsPrivatePeersOnly(t *testing.T) {
	def := resolveTrustedProxies("")
	for _, peer := range []string{"127.0.0.1:1", "10.1.2.3:1", "172.20.0.5:1", "192.168.1.9:1", "169.254.1.1:1", "[::1]:1", "[fd12:3456::1]:1", "[fe80::1]:1"} {
		if got := realIP(ipReq(peer, "198.51.100.1", ""), def); got != "198.51.100.1" {
			t.Errorf("default, private peer %s: realIP = %q, want X-Real-Ip", peer, got)
		}
		if got := realIP(ipReq(peer, "", "1.1.1.1, 198.51.100.2"), def); got != "198.51.100.2" {
			t.Errorf("default, private peer %s: realIP = %q, want rightmost XFF", peer, got)
		}
	}
	for _, peer := range []string{"203.0.113.9:1", "[2001:db8::1]:1", "172.32.0.1:1"} {
		if got := realIP(ipReq(peer, "198.51.100.1", "198.51.100.2"), def); got == "198.51.100.1" || got == "198.51.100.2" {
			t.Errorf("default, public peer %s: forwarding header was believed (%q)", peer, got)
		}
	}
}

func TestRealIP_NoneAndExplicitList(t *testing.T) {
	none := resolveTrustedProxies("none")
	if got := realIP(ipReq("10.1.2.3:1", "198.51.100.1", "198.51.100.2"), none); got != "10.1.2.3" {
		t.Errorf("none, private peer: realIP = %q, want the peer 10.1.2.3", got)
	}
	explicit := resolveTrustedProxies("203.0.113.0/24")
	if got := realIP(ipReq("203.0.113.9:1", "198.51.100.1", ""), explicit); got != "198.51.100.1" {
		t.Errorf("explicit list, listed peer: realIP = %q", got)
	}
	// An explicit list replaces the defaults: a private peer outside it is not trusted.
	if got := realIP(ipReq("10.1.2.3:1", "198.51.100.1", ""), explicit); got != "10.1.2.3" {
		t.Errorf("explicit list, private peer: realIP = %q, want 10.1.2.3", got)
	}
}
