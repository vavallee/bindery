package oidc

import (
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

// A caller with no DiscoverPolicy is the documented no-guardrails mode (an
// in-process test pointed at httptest.NewServer). It must keep getting the
// default transport, which http.Client reads a nil RoundTripper as.
func TestDiscoverTransport_NilPolicyKeepsDefault(t *testing.T) {
	if got := discoverTransport(nil); got != nil {
		t.Errorf("discoverTransport(nil) = %#v, want nil (http.Client's default transport)", got)
	}
}

// TestDiscoverTransport_DialGuardWhenDirect covers #2353. Discover validated
// the issuer up front and re-validated every redirect hop, but dialled without
// re-checking, so a name answering with a public address for the lookups and a
// private one for the connect got through both.
func TestDiscoverTransport_DialGuardWhenDirect(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	if _, err := httpsec.ConfigureOutboundProxy("", "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy reset: %v", err)
	}

	policy := httpsec.PolicyLAN
	got := discoverTransport(&policy)
	tr, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("discoverTransport = %T, want *http.Transport on the direct path", got)
	}
	if tr.DialContext == nil {
		t.Error("direct-path transport must install a DialContext (the rebind re-check)")
	}
	if got == httpsec.DefaultProxyTransport() {
		t.Error("direct-path transport must be a clone, not the shared default transport")
	}
}

// With an outbound proxy configured the dial targets the proxy rather than the
// IdP, so the per-dial re-check is skipped and the shared transport is returned
// unchanged. Without this a loopback or LAN proxy would be rejected by the
// guard meant for the IdP.
func TestDiscoverTransport_NoDialGuardWhenProxied(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	if _, err := httpsec.ConfigureOutboundProxy("http://127.0.0.1:0", "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy: %v", err)
	}

	policy := httpsec.PolicyLAN
	if got := discoverTransport(&policy); got != httpsec.DefaultProxyTransport() {
		t.Errorf("with a proxy configured, discoverTransport must return the shared proxy transport unchanged; got %#v", got)
	}
}
