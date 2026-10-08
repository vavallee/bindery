package nb

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// A 429 used to fail the lookup on the spot (#2369); the client now waits and
// asks again before reporting the refusal.
func TestGetBookByISBN_RetriesA429(t *testing.T) {
	var calls atomic.Int32
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("slow down"))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	if _, err := c.GetBookByISBN(context.Background(), "9788200000028"); err != nil {
		t.Fatalf("GetBookByISBN after one 429 = %v, want the retry to succeed", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (the 429 and its retry)", got)
	}
}
