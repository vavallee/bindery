package googlebooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
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

// A quota that has run out for the day does not come back in a few seconds,
// so it is not retried: each retry only spends requests against it. It is
// still a rate limit, so scheduled work stops asking. The reason sits past a
// long message, as Google's real envelopes put it.
func TestSearchBooks_DailyQuotaIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{
		{http.StatusForbidden, "dailyLimitExceeded"},
		{http.StatusTooManyRequests, "quotaExceeded"},
	} {
		var calls atomic.Int32
		body := `{"error":{"code":` + strconv.Itoa(tc.status) + `,"message":"` + strings.Repeat("Quota exceeded. ", 60) +
			`","errors":[{"domain":"usageLimits","reason":"` + tc.reason + `"}]}}`
		c := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		_, err := c.SearchBooks(context.Background(), "dune")
		if !errors.Is(err, providererr.ErrRateLimited) {
			t.Errorf("HTTP %d %s: err = %v, want a rate limit", tc.status, tc.reason, err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("HTTP %d %s: requests = %d, want 1: a daily quota does not recover on retry", tc.status, tc.reason, got)
		}
	}
}

// A per minute throttle is still retried.
func TestSearchBooks_RateLimitReasonIsRetried(t *testing.T) {
	var calls atomic.Int32
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	if _, err := c.SearchBooks(context.Background(), "dune"); err != nil {
		t.Fatalf("SearchBooks = %v, want the throttle retried", err)
	}
}
