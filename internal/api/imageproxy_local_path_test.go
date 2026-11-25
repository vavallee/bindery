package api

import "testing"

func TestProxyImageURL_ProtocolRelativeIsProxied(t *testing.T) {
	for _, raw := range []string{"//evil.example/x.jpg", `/\evil.example/x.jpg`} {
		if got := ProxyImageURL(raw); got == raw {
			t.Errorf("ProxyImageURL(%q) passed it through unproxied", raw)
		}
		if got := OPDSImageURL(raw); got == raw {
			t.Errorf("OPDSImageURL(%q) passed it through unproxied", raw)
		}
	}
	for _, raw := range []string{"/api/v1/images?url=x", "/covers/1.jpg", ""} {
		if ProxyImageURL(raw) != raw || OPDSImageURL(raw) != raw {
			t.Errorf("same-origin %q was rewritten", raw)
		}
	}
}
