package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #2036: the indexer dial guard follows the admin-typed service policy, so the
// save-time check and the connect agree about link-local.
func TestSharedTransport_LinkLocalFollowsServiceOptIn(t *testing.T) {
	dial := func(addr string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		c, err := sharedTransportInstance().DialContext(ctx, "tcp", addr)
		if c != nil {
			_ = c.Close()
		}
		return err
	}

	t.Setenv("BINDERY_ALLOW_LINK_LOCAL_SERVICES", "")
	if err := dial("169.254.1.2:9696"); err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Errorf("option off: err = %v, want the link-local refusal", err)
	}

	t.Setenv("BINDERY_ALLOW_LINK_LOCAL_SERVICES", "1")
	if err := dial("169.254.1.2:9696"); err != nil && strings.Contains(err.Error(), "not allowed") {
		t.Errorf("option on: policy refused link-local: %v", err)
	}
	for _, addr := range []string{"169.254.169.254:80", "169.254.170.2:80"} {
		if err := dial(addr); err == nil || !strings.Contains(err.Error(), "cloud metadata") {
			t.Errorf("%s with option on: err = %v, want the metadata refusal", addr, err)
		}
	}
}

// An indexer is a remote party. With the option on, its redirects must not
// reach link-local or metadata, while a LAN redirect still follows.
func TestHTTPClient_RedirectNeverGetsLinkLocal(t *testing.T) {
	t.Setenv("BINDERY_ALLOW_LINK_LOCAL_SERVICES", "1")
	lan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer lan.Close()

	for _, target := range []string{
		"http://169.254.1.2:9696/x", "http://169.254.169.253/x", "http://[fe80::1]:9696/x", "http://169.254.169.254/latest",
	} {
		srv := httptest.NewServer(http.RedirectHandler(target, http.StatusFound))
		resp, err := newHTTPClient().Get(srv.URL)
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
	resp, err := newHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("redirect to a LAN address should still follow: %v", err)
	}
	_ = resp.Body.Close()
}
