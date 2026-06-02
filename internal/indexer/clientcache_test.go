package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// testRSSResponse is the minimum valid Newznab response so the searcher's
// parse path doesn't error out. We don't care about the body shape here;
// the tests exist to verify caching behaviour, not parsing.
const testRSSResponse = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="0"/>
  </channel>
</rss>`

// newPooledTestSearcher wires the Searcher's injectable factory to bypass
// the SSRF dialer, while still exercising the production pool. The factory
// is also instrumented with a counter so tests can independently verify how
// many times the underlying *newznab.Client constructor ran — orthogonal to
// the cache's ConstructorCount.
func newPooledTestSearcher(t *testing.T) (*Searcher, *atomic.Int64) {
	t.Helper()
	var constructed atomic.Int64
	s := &Searcher{
		newClient: func(baseURL, apiKey string) *newznab.Client {
			constructed.Add(1)
			c := newznab.New(baseURL, apiKey)
			c.SetHTTPClient(&http.Client{Timeout: 5 * time.Second})
			return c
		},
	}
	return s, &constructed
}

func TestNewznab_ClientIsPooled(t *testing.T) {
	// Stand up a stub indexer that returns an empty RSS feed for every
	// query, and fire three searches against the same indexer config.
	// The pool must yield the same *newznab.Client every time, so the
	// factory runs exactly once.
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(testRSSResponse))
	}))
	defer srv.Close()

	s, constructed := newPooledTestSearcher(t)
	indexers := []models.Indexer{{
		ID:         1,
		Name:       "stub",
		URL:        srv.URL,
		APIKey:     "key-A",
		Enabled:    true,
		Categories: []int{7020},
	}}
	criteria := MatchCriteria{Title: "anything", Author: "anyone"}

	for i := 0; i < 3; i++ {
		_ = s.SearchBook(context.Background(), indexers, criteria)
	}

	if got := constructed.Load(); got != 1 {
		t.Fatalf("expected client constructor to run once across 3 searches, got %d", got)
	}
	if got := s.cache.ConstructorCount(); got != 1 {
		t.Fatalf("expected cache constructor count = 1, got %d", got)
	}
	if got := s.cache.Len(); got != 1 {
		t.Fatalf("expected cache to hold exactly one entry, got %d", got)
	}
	// Sanity: the stub server actually served traffic, so we know the
	// pooled client really did issue HTTP requests rather than no-oping.
	if hits.Load() == 0 {
		t.Fatalf("expected stub indexer to receive at least one request")
	}
}

func TestNewznab_DifferentConfigsGetDifferentClients(t *testing.T) {
	// Two distinct indexer URLs must produce two distinct cached clients.
	// Same URL with two distinct apiKeys must also produce two distinct
	// entries, because the credentials are baked into the *Client struct.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(testRSSResponse))
	}))
	defer srv.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(testRSSResponse))
	}))
	defer srv2.Close()

	s, constructed := newPooledTestSearcher(t)
	indexers := []models.Indexer{
		{ID: 1, Name: "a", URL: srv.URL, APIKey: "key-A", Enabled: true, Categories: []int{7020}},
		{ID: 2, Name: "b", URL: srv2.URL, APIKey: "key-A", Enabled: true, Categories: []int{7020}},
		{ID: 3, Name: "c", URL: srv.URL, APIKey: "key-B", Enabled: true, Categories: []int{7020}},
	}
	_ = s.SearchBook(context.Background(), indexers, MatchCriteria{Title: "x", Author: "y"})

	if got := constructed.Load(); got != 3 {
		t.Fatalf("expected 3 client constructions for 3 distinct configs, got %d", got)
	}
	if got := s.cache.Len(); got != 3 {
		t.Fatalf("expected 3 cache entries, got %d", got)
	}

	// Repeating the same fan-out hits the cache for every indexer.
	_ = s.SearchBook(context.Background(), indexers, MatchCriteria{Title: "x", Author: "y"})
	if got := constructed.Load(); got != 3 {
		t.Fatalf("expected constructor count unchanged after second fan-out, got %d", got)
	}
}

func TestNewznab_SharedTransportIsSingleton(t *testing.T) {
	// The shared transport singleton must initialise at most once for the
	// process. This test runs after every other test in the package, so
	// the count is observed at >= 1; we only assert it's bounded.
	// Construct a handful of fresh clients via newznab.New (production
	// path, not the test factory) and verify the count stays put.
	before := newznab.TransportBuildCount()
	for i := 0; i < 5; i++ {
		_ = newznab.New(fmt.Sprintf("https://example-%d.test/api", i), "k")
	}
	after := newznab.TransportBuildCount()
	if after != before {
		t.Fatalf("shared transport was rebuilt: before=%d after=%d", before, after)
	}
}
