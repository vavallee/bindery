package sabnzbd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A Prowlarr grab is a redirect: the release's download link points at
// Prowlarr with Prowlarr's apikey in the query, and Prowlarr answers with a
// 302 to the indexer. Go's default redirect policy copies the previous URL,
// query string included, into a Referer header on the next hop, which hands
// the Prowlarr apikey to the third party indexer.
func TestFetchNZBContent_RedirectSendsNoReferer(t *testing.T) {
	var (
		hits    atomic.Int32
		referer atomic.Value
	)
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		referer.Store(r.Header.Get("Referer"))
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexer.Close()
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, indexer.URL+"/getnzb/abc", http.StatusFound)
	}))
	defer prowlarr.Close()

	c := New("127.0.0.1", 0, "sabkey", "", false)
	allowNZBFetch(c)

	body, err := c.fetchNZBContent(context.Background(), prowlarr.URL+"/1/download?apikey=PROWLARRSECRET&link=abc")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(body) != testNZBContent {
		t.Fatalf("redirect was not followed to the indexer, got body %q", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("indexer hits = %d, want 1", hits.Load())
	}
	got, _ := referer.Load().(string)
	if strings.Contains(got, "PROWLARRSECRET") {
		t.Fatalf("the indexer received the Prowlarr apikey in Referer: %q", got)
	}
	if got != "" {
		t.Fatalf("the indexer received a Referer header: %q", got)
	}
}

// Every redirect hop must clear the same SSRF validation the first URL did.
// The dial guard covers this only when no outbound proxy is configured; with
// BINDERY_OUTBOUND_PROXY set the transport dials the proxy and the hops were
// unchecked. The test drops the dial guard to stand in for that mode.
func TestFetchNZBContent_RedirectToDisallowedTargetIsRefused(t *testing.T) {
	var blockedHits atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		blockedHits.Add(1)
		fmt.Fprint(w, testNZBContent)
	}))
	defer blocked.Close()
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blocked.URL+"/internal?apikey=HOPSECRET", http.StatusFound)
	}))
	defer prowlarr.Close()

	c := New("127.0.0.1", 0, "sabkey", "", false)
	c.fetchHTTP.Transport = nil
	c.validateNZBURL = func(raw string) error {
		if strings.HasPrefix(raw, blocked.URL) {
			return errors.New("url not allowed: points to a test blocked address")
		}
		return nil
	}

	_, err := c.fetchNZBContent(context.Background(), prowlarr.URL+"/1/download?apikey=PROWLARRSECRET")
	if err == nil {
		t.Fatal("expected the redirect to a disallowed address to be refused")
	}
	if blockedHits.Load() != 0 {
		t.Fatalf("the disallowed redirect target was contacted %d time(s)", blockedHits.Load())
	}
	if strings.Contains(err.Error(), "HOPSECRET") || strings.Contains(err.Error(), "PROWLARRSECRET") {
		t.Fatalf("error leaks a key: %v", err)
	}
}
