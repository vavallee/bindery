package grimmory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The Grimmory client must not follow a redirect to a link-local or cloud
// metadata address, and must not dial one even when the configured base URL
// points at one. Loopback and LAN targets (the normal case) stay reachable.

func TestClient_GrimmoryRedirectToMetadataRefused(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.Ping(ctx)
	if err == nil {
		t.Fatal("Ping followed a redirect to the metadata address without error")
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatalf("the loopback test server was never reached (the loopback target must stay allowed): %v", err)
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error = %v; want the link-local refusal", err)
	}
	// The redirect hop itself is refused (CheckRedirect), not just the later connect.
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("error = %v; want the redirect refused at the hop", err)
	}
}

func TestClient_GrimmoryLinkLocalBaseURLRefusedAtDial(t *testing.T) {
	c, err := NewClient("http://169.254.169.254:13378", "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.Ping(ctx)
	if err == nil {
		t.Fatal("Ping reached a link-local base URL without error")
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error = %v; want the link-local refusal", err)
	}
}
