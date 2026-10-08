package audible

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/providererr"
)

func refusingServer(t *testing.T, refusals int32, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= refusals {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"products":[],"total_results":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The ABS import path hits Audible hardest, and a 429 used to drop the
// author's audiobook catalogue on the floor (#2369).
func TestSearchBooksByAuthor_RetriesA429(t *testing.T) {
	var calls atomic.Int32
	c := New()
	c.baseURL = refusingServer(t, 1, &calls).URL
	if _, err := c.SearchBooksByAuthor(context.Background(), "Frank Herbert"); err != nil {
		t.Fatalf("SearchBooksByAuthor after one 429 = %v, want the retry to succeed", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (the 429 and its retry)", got)
	}
}

func TestSearchBooksByAuthor_PersistentRefusalIsRateLimited(t *testing.T) {
	var calls atomic.Int32
	c := New()
	c.baseURL = refusingServer(t, 1000, &calls).URL
	_, err := c.SearchBooksByAuthor(context.Background(), "Frank Herbert")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("err = %v, want it to match providererr.ErrRateLimited", err)
	}
}
