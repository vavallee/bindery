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
