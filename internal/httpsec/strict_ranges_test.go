package httpsec

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckIP_StrictBlocksReservedAndEmbeddedRanges(t *testing.T) {
	blocked := []string{
		"100.64.0.1", "100.100.100.200", "100.127.255.254",
		"198.18.0.1", "198.19.255.254", "192.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::a00:1", "64:ff9b::c0a8:101",
		"2002:7f00:1::1", "2002:a9fe:a9fe::1", "2002:c0a8:101::1", "2002:0a00:0001::1",
	}
	for _, s := range blocked {
		if err := checkIP(net.ParseIP(s), PolicyStrict); err == nil {
			t.Errorf("PolicyStrict allowed %s", s)
		}
	}
	allowed := []string{"93.184.216.34", "100.63.255.255", "100.128.0.1", "198.17.255.255", "198.20.0.1", "192.0.1.1",
		"64:ff9b::5db8:d822", "2002:5db8:d822::1", "2606:4700::1111"}
	for _, s := range allowed {
		if err := checkIP(net.ParseIP(s), PolicyStrict); err != nil {
			t.Errorf("PolicyStrict refused public %s: %v", s, err)
		}
	}
	// LAN-class policies are unchanged: Tailscale and CGNAT hosts stay reachable
	// for admin-configured services.
	for _, p := range []Policy{PolicyLAN, PolicyLANLoopback, PolicyService} {
		if err := checkIP(net.ParseIP("100.64.0.1"), p); err != nil {
			t.Errorf("policy %v refused 100.64.0.1: %v", p, err)
		}
	}
}

func TestRedirectGuard_StripsCredentialHeadersOnOriginChange(t *testing.T) {
	var got http.Header
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer dest.Close()
	target := strings.Replace(dest.URL, "127.0.0.1", "localhost", 1)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target+"/x", http.StatusFound) }))
	defer src.Close()

	c := &http.Client{CheckRedirect: RedirectGuard(PolicyLANLoopback)}
	req, _ := http.NewRequest(http.MethodGet, src.URL, nil)
	req.Header.Set("X-Api-Key", "SECRET")
	req.Header.Set("Authorization", "Bearer SECRET")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if v := got.Get("X-Api-Key"); v != "" {
		t.Errorf("X-Api-Key reached the other host: %q", v)
	}
	if v := got.Get("Authorization"); v != "" {
		t.Errorf("Authorization reached the other host: %q", v)
	}
}

func TestRedirectGuard_KeepsCredentialHeadersOnSameOrigin(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		got = r.Header.Clone()
	}))
	defer srv.Close()
	c := &http.Client{CheckRedirect: RedirectGuard(PolicyLANLoopback)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/start", nil)
	req.Header.Set("X-Api-Key", "SECRET")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got.Get("X-Api-Key") != "SECRET" {
		t.Errorf("same-origin redirect lost X-Api-Key: %q", got.Get("X-Api-Key"))
	}
}
