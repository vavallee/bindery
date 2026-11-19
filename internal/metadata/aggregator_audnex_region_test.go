package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/audnex"
)

// #3982: audnex answers per marketplace, so a miss cached under one region
// must not be served after the operator picks another.
func TestSetAudnexRegionDropsCachedMiss(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("region") != "de" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"asin":"B0FDGG4R6M","title":"Der Nachbar","authors":[{"name":"Sebastian Fitzek"}],"language":"german"}`))
	}))
	defer srv.Close()

	a := NewAggregator(nil)
	a.WithAudnexClient(audnex.New("us").WithBaseURL(srv.URL))
	ctx := context.Background()
	lookup := func() *audnex.Book {
		t.Helper()
		b, err := a.load().getAudnexBookByASIN(ctx, "B0FDGG4R6M")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	if lookup() != nil {
		t.Fatal("us lookup found the book, want a miss")
	}
	if lookup() != nil || hits.Load() != 1 {
		t.Fatalf("second us lookup should be served from cache: hits=%d", hits.Load())
	}

	// The same region again is not a change: the cached miss stays.
	a.SetAudnexRegion("us")
	if lookup() != nil || hits.Load() != 1 {
		t.Fatalf("setting the same region reset the cache: hits=%d", hits.Load())
	}

	a.SetAudnexRegion("de")
	if got := a.AudnexRegion(); got != "de" {
		t.Fatalf("region = %q, want de", got)
	}
	b := lookup()
	if b == nil || b.Title != "Der Nachbar" {
		t.Fatalf("after switching to de the book = %+v, want Der Nachbar", b)
	}
}
