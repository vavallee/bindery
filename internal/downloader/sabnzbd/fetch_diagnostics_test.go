package sabnzbd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const nzbfinder203Body = `<?xml version="1.0" encoding="UTF-8"?>
<error code="203" description="This application is not allowed to download NZBs from NZBFinder."/>`

// TestAddURL_ProwlarrRedirectRejection reproduces #1404 end to end: the grab
// URL points at a Prowlarr-style proxy which 302-redirects to the indexer
// (Prowlarr's per-indexer Redirect setting), and the indexer rejects Bindery's
// identity with newznab error 203. The resulting error must surface the
// structured newznab error plus the redirect hop and the Prowlarr fix, instead
// of the raw XML soup the log used to carry.
//
// The proxy is addressed as "localhost" and redirects to "127.0.0.1" so the
// two ends of the hop have different hostnames, like prowlarr → nzbfinder.ws.
func TestAddURL_ProwlarrRedirectRejection(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(nzbfinder203Body))
	}))
	defer indexerSrv.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, indexerSrv.URL+"/getnzb/abc", http.StatusFound)
	}))
	defer proxySrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	allowNZBFetch(c)

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	grabURL := "http://localhost:" + proxyURL.Port() + "/3/download?apikey=k&link=abc"

	_, err = c.AddURL(context.Background(), grabURL, "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"newznab error 203",
		"not allowed to download NZBs",
		`redirected from "localhost" to "127.0.0.1"`,
		"Redirect setting",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

// TestAddURL_DirectIndexerErrorHasNoRedirectGuidance pins the negative: a
// plain same-host failure keeps the structured newznab detail but must not
// speculate about proxies or redirects.
func TestAddURL_DirectIndexerErrorHasNoRedirectGuidance(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(nzbfinder203Body))
	}))
	defer indexerSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "newznab error 203") {
		t.Errorf("error missing structured newznab detail:\n%s", msg)
	}
	if strings.Contains(msg, "redirected") || strings.Contains(msg, "Prowlarr") {
		t.Errorf("same-host failure must not mention redirects/Prowlarr:\n%s", msg)
	}
}
