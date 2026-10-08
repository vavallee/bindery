package audnex

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
		_, _ = w.Write([]byte(`{"asin":"B0036S4B2G","title":"Dune"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A 429 used to fail the ASIN lookup outright (#2369).
func TestGetBook_RetriesA429(t *testing.T) {
	var calls atomic.Int32
	c := New("us")
	c.baseURL = refusingServer(t, 1, &calls).URL
	b, err := c.GetBook(context.Background(), "B0036S4B2G")
	if err != nil || b == nil || b.Title != "Dune" {
		t.Fatalf("GetBook after one 429 = %+v, %v; want the retry to return the book", b, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (the 429 and its retry)", got)
	}
}

func TestGetBook_PersistentRefusalIsRateLimited(t *testing.T) {
	var calls atomic.Int32
	c := New("us")
	c.baseURL = refusingServer(t, 1000, &calls).URL
	_, err := c.GetBook(context.Background(), "B0036S4B2G")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("err = %v, want it to match providererr.ErrRateLimited", err)
	}
}
