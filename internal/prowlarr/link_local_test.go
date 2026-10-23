package prowlarr

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #2036: Prowlarr answers are remote data too; a redirect never gets the
// link-local allowance even with the option on.
func TestClient_RedirectNeverGetsLinkLocal(t *testing.T) {
	t.Setenv("BINDERY_ALLOW_LINK_LOCAL_SERVICES", "1")
	lan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer lan.Close()
	c := New("http://prowlarr:9696", "k")

	for _, target := range []string{
		"http://169.254.1.2:9696/x", "http://169.254.169.253/x", "http://[fe80::1]:9696/x", "http://169.254.169.254/latest",
	} {
		srv := httptest.NewServer(http.RedirectHandler(target, http.StatusFound))
		resp, err := c.http.Get(srv.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "redirect blocked") {
			t.Errorf("redirect to %s: err = %v, want it refused at the hop", target, err)
		}
	}

	srv := httptest.NewServer(http.RedirectHandler(lan.URL, http.StatusFound))
	defer srv.Close()
	resp, err := c.http.Get(srv.URL)
	if err != nil {
		t.Fatalf("redirect to a LAN address should still follow: %v", err)
	}
	_ = resp.Body.Close()
}
