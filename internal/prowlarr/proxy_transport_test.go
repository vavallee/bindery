package prowlarr

import (
	"net/http"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

// The Prowlarr client must route through the shared outbound-proxy transport
// like the newznab search client that talks to the same hosts, so sync/test
// calls honor BINDERY_OUTBOUND_PROXY instead of dialing Prowlarr directly.
// With a proxy configured the transport is handed back untouched: the dial
// targets the operator-trusted proxy, and a per-dial SSRF re-check there would
// reject a LAN or loopback proxy and break every sync.
func TestGuardedTransport_UsesProxyTransportWhenProxied(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })

	if _, err := httpsec.ConfigureOutboundProxy("http://127.0.0.1:0", "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy: %v", err)
	}

	if got := guardedTransport(); got != httpsec.DefaultProxyTransport() {
		t.Errorf("with a proxy configured, guardedTransport() must return the shared proxy transport unchanged; got %#v", got)
	}
}

// TestGuardedTransport_DialGuardWhenDirect covers #2353. On the direct path the
// transport carries the per-dial SSRF re-check, which is what closes the
// rebind window between the handler's ValidateOutboundURL and the connect.
func TestGuardedTransport_DialGuardWhenDirect(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	if _, err := httpsec.ConfigureOutboundProxy("", "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy reset: %v", err)
	}

	got := guardedTransport()
	tr, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("guardedTransport() = %T, want *http.Transport on the direct path", got)
	}
	if tr.DialContext == nil {
		t.Error("direct-path transport must install a DialContext (the rebind re-check)")
	}
	if got == httpsec.DefaultProxyTransport() {
		t.Error("direct-path transport must be a clone, not the shared default transport")
	}
	if tr.Proxy == nil {
		t.Error("the clone must keep the proxy resolver so BINDERY_OUTBOUND_PROXY still applies")
	}
}

// Every client shares one transport, and therefore one connection pool. A
// syncer builds a client per run, so a fresh pool per construction would be a
// real cost for the dial guard.
func TestNewWithTimeout_SharesOneTransport(t *testing.T) {
	a := NewWithTimeout("http://prowlarr:9696", "key", 30*time.Second)
	b := New("http://prowlarr:9696", "key")
	if a.http.Transport != b.http.Transport {
		t.Errorf("clients must share one transport; got %#v and %#v", a.http.Transport, b.http.Transport)
	}
	if a.http.Transport == nil {
		t.Error("client transport must not be nil (that would bypass the proxy and the dial guard)")
	}
}
