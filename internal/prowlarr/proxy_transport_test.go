package prowlarr

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

// The Prowlarr client must route through the shared outbound-proxy transport
// like the newznab search client that talks to the same hosts, so sync and test
// calls honor BINDERY_OUTBOUND_PROXY instead of dialing Prowlarr directly
// (#1847). With a proxy configured, a real client call lands on the proxy. The
// transport is used as it is there: the dial targets the operator-trusted
// proxy, and a per-dial SSRF re-check would reject a LAN or loopback proxy and
// break every sync, which this test's loopback proxy also covers.
func TestClient_RoutesThroughOutboundProxy(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	hit := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- r.Host + r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer proxy.Close()
	if _, err := httpsec.ConfigureOutboundProxy(proxy.URL, "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy: %v", err)
	}

	// The shared transport is built once per process, after startup has
	// configured the proxy (see sharedGuardedTransport). Other tests in this
	// package build it first without one, so this client gets the transport
	// startup would have built with the proxy in place.
	c := New("http://prowlarr.example:9696", "key")
	c.http.Transport = guardedTransport()
	version, err := c.Test(t.Context())
	if err != nil {
		t.Fatalf("Test through the proxy: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("version = %q, want the answer the proxy relayed", version)
	}
	select {
	case got := <-hit:
		if got != "prowlarr.example:9696/api/v1/system/status" {
			t.Errorf("proxy saw %q, want the Prowlarr status request", got)
		}
	default:
		t.Error("the request did not go through the configured proxy")
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
