package httpsec

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// resetProxy clears any configured outbound proxy. Registered via t.Cleanup so
// the package-global proxy state never leaks between tests (these tests must
// not run with t.Parallel for the same reason).
func resetProxy(t *testing.T) {
	t.Cleanup(func() {
		if _, err := ConfigureOutboundProxy("", "", true); err != nil {
			t.Fatalf("reset proxy: %v", err)
		}
	})
}

func TestConfigureOutboundProxy_Disabled(t *testing.T) {
	resetProxy(t)

	u, err := ConfigureOutboundProxy("", "", true)
	if err != nil || u != nil {
		t.Fatalf("ConfigureOutboundProxy(\"\") = %v, %v; want nil, nil", u, err)
	}
	if ProxyFunc() != nil {
		t.Error("ProxyFunc() should be nil when no proxy is configured")
	}
	if got := DefaultProxyTransport(); got != http.DefaultTransport {
		t.Errorf("DefaultProxyTransport() = %v; want http.DefaultTransport when unset", got)
	}
}

func TestConfigureOutboundProxy_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // expected resolved proxy URL string
	}{
		{"http", "http://proxy.example:3128", "http://proxy.example:3128"},
		{"http with creds", "http://user:pass@proxy.example:3128", "http://user:pass@proxy.example:3128"},
		{"https", "https://proxy.example:8443", "https://proxy.example:8443"},
		{"socks5", "socks5://proxy.example:1080", "socks5://proxy.example:1080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProxy(t)

			u, err := ConfigureOutboundProxy(tc.raw, "", true)
			if err != nil {
				t.Fatalf("ConfigureOutboundProxy(%q) error: %v", tc.raw, err)
			}
			if u == nil || u.String() != tc.want {
				t.Fatalf("ConfigureOutboundProxy(%q) = %v; want %s", tc.raw, u, tc.want)
			}

			pf := ProxyFunc()
			if pf == nil {
				t.Fatal("ProxyFunc() is nil after configuring a proxy")
			}
			// A public (non-bypassed) destination resolves to the proxy.
			req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
			got, err := pf(req)
			if err != nil || got == nil || got.String() != tc.want {
				t.Fatalf("ProxyFunc()(public) = %v, %v; want %s", got, err, tc.want)
			}

			if DefaultProxyTransport() == http.DefaultTransport {
				t.Error("DefaultProxyTransport() should be a proxy-applying clone, not http.DefaultTransport")
			}
		})
	}
}

func TestConfigureOutboundProxy_Invalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"unsupported socks4", "socks4://proxy.example:1080"},
		{"unsupported scheme", "ftp://proxy.example:21"},
		{"missing host", "http://"},
		{"malformed escape", "http://%zz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProxy(t)

			u, err := ConfigureOutboundProxy(tc.raw, "", true)
			if err == nil {
				t.Fatalf("ConfigureOutboundProxy(%q) = %v, nil; want error", tc.raw, u)
			}
			// On error the proxy must stay disabled.
			if ProxyFunc() != nil {
				t.Error("ProxyFunc() should remain nil after a rejected proxy URL")
			}
			if DefaultProxyTransport() != http.DefaultTransport {
				t.Error("DefaultProxyTransport() should remain http.DefaultTransport after a rejected proxy URL")
			}
		})
	}
}

// TestProxyFunc_BypassRules covers both the bypass-local heuristics and the
// explicit no-proxy list: remote hosts are proxied; LAN / loopback / single-label
// hosts and no-proxy matches are dialled direct.
func TestProxyFunc_BypassRules(t *testing.T) {
	resetProxy(t)
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "nzbgeek.info, 172.16.0.0/12", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	pf := ProxyFunc()
	if pf == nil {
		t.Fatal("ProxyFunc() is nil")
	}

	cases := []struct {
		url     string
		proxied bool
	}{
		{"http://example.com/x", true},          // public -> proxied
		{"https://api.openlibrary.org/x", true}, // public -> proxied
		{"http://192.168.1.50:9117", false},     // RFC1918 -> bypass
		{"http://127.0.0.1:8080", false},        // loopback -> bypass
		{"http://172.16.5.4/x", false},          // no-proxy CIDR -> bypass
		{"http://jackett:9117/x", false},        // single-label host -> bypass
		{"http://localhost:8989", false},        // localhost -> bypass
		{"http://prowlarr.local/x", false},      // .local -> bypass
		{"https://nzbgeek.info/x", false},       // no-proxy exact -> bypass
		{"https://api.nzbgeek.info/x", false},   // no-proxy domain suffix -> bypass
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodGet, c.url, nil)
		got, err := pf(req)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.url, err)
			continue
		}
		if c.proxied && got == nil {
			t.Errorf("%s: expected proxied, got direct", c.url)
		}
		if !c.proxied && got != nil {
			t.Errorf("%s: expected direct, got proxied (%s)", c.url, got)
		}
	}
}

func TestProxyFunc_BypassLocalDisabled(t *testing.T) {
	resetProxy(t)
	// With bypassLocal=false only the explicit no-proxy list is honoured, so a
	// LAN host is proxied.
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.168.1.50:9117", nil)
	got, _ := ProxyFunc()(req)
	if got == nil {
		t.Error("with bypassLocal=false a LAN host should be proxied")
	}
}

// TestDefaultProxyTransport_RoutesThroughProxy verifies that a client built off
// DefaultProxyTransport() actually dials the configured proxy: the request for
// an external host lands on the local proxy server instead.
func TestDefaultProxyTransport_RoutesThroughProxy(t *testing.T) {
	resetProxy(t)

	hit := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	if _, err := ConfigureOutboundProxy(proxy.URL, "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy(%q): %v", proxy.URL, err)
	}

	client := &http.Client{Transport: DefaultProxyTransport()}
	resp, err := client.Get("http://example.invalid/path")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	select {
	case host := <-hit:
		if host != "example.invalid" {
			t.Errorf("proxy saw Host %q; want example.invalid", host)
		}
	default:
		t.Error("request did not reach the configured proxy")
	}
}
