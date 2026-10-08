package googlebooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/providererr"
)

// refusingClient answers the first refusals requests with a 429 and every
// later one with okBody, counting the requests it saw.
func refusingClient(refusals int32, okBody string, calls *atomic.Int32) *Client {
	return &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n <= refusals {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"Quota exceeded"}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(okBody))}, nil
	})}}
}

// A 429 used to be a hard failure the caller logged and moved past (#2369).
// It is a refusal to answer now, so the client waits and asks again.
func TestSearchBooks_RetriesA429(t *testing.T) {
	var calls atomic.Int32
	c := refusingClient(1, `{"totalItems":0,"items":[]}`, &calls)
	if _, err := c.SearchBooks(context.Background(), "dune"); err != nil {
		t.Fatalf("SearchBooks after one 429 = %v, want the retry to succeed", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (the 429 and its retry)", got)
	}
}

// A 429 that outlives the retries is marked as a rate limit, so callers that
// walk many authors can stop instead of burning through their queue.
func TestSearchBooks_PersistentRefusalIsRateLimited(t *testing.T) {
	var calls atomic.Int32
	c := refusingClient(1000, `{}`, &calls)
	_, err := c.SearchBooks(context.Background(), "dune")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("err = %v, want it to match providererr.ErrRateLimited", err)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("requests = %d, want the 429 retried", got)
	}
}
