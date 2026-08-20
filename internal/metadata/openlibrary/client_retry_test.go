package openlibrary

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedRoundTripper replays one response per call from responses, in
// order, holding at the last entry once exhausted. Used to simulate a
// provider that fails N times before recovering (or never recovers).
type scriptedRoundTripper struct {
	responses []scriptedResponse
	calls     atomic.Int32
}

type scriptedResponse struct {
	status  int
	body    string
	headers map[string]string
}

func (rt *scriptedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	i := int(rt.calls.Add(1)) - 1
	if i >= len(rt.responses) {
		i = len(rt.responses) - 1
	}
	resp := rt.responses[i]
	header := make(http.Header, len(resp.headers))
	for k, v := range resp.headers {
		header.Set(k, v)
	}
	return &http.Response{
		StatusCode: resp.status,
		Body:       io.NopCloser(strings.NewReader(resp.body)),
		Header:     header,
	}, nil
}

func TestGetJSON_RetriesOn429ThenSucceeds(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusTooManyRequests, body: `{"error":"throttled"}`},
		{status: http.StatusOK, body: `{"key":"/authors/OL1A"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	var target struct {
		Key string `json:"key"`
	}
	start := time.Now()
	err := c.getJSON(context.Background(), "https://openlibrary.org/authors/OL1A.json", &target)
	if err != nil {
		t.Fatalf("getJSON returned error after eventual success: %v", err)
	}
	if target.Key != "/authors/OL1A" {
		t.Fatalf("target.Key = %q, want /authors/OL1A", target.Key)
	}
	if got := rt.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (one 429, one success)", got)
	}
	if elapsed := time.Since(start); elapsed < getJSONBaseDelay/2 {
		t.Fatalf("elapsed = %v, expected a backoff wait before the retry", elapsed)
	}
}

func TestGetJSON_HonorsRetryAfterSeconds(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusServiceUnavailable, body: `{"error":"down"}`, headers: map[string]string{"Retry-After": "1"}},
		{status: http.StatusOK, body: `{}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	start := time.Now()
	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("getJSON returned error after eventual success: %v", err)
	}
	// The server said wait exactly 1s; the retry should not fire meaningfully
	// earlier than that (allow a little slack for scheduling jitter) and
	// should not fall back to computing its own (shorter, jittered) backoff.
	if elapsed < 950*time.Millisecond {
		t.Fatalf("elapsed = %v, want >= ~1s (Retry-After should have been honored)", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, suspiciously long for a 1s Retry-After", elapsed)
	}
}

func TestGetJSON_ExhaustsRetriesAndReturnsLastError(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusServiceUnavailable, body: `{"error":"down"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	// getJSONMaxRetries retries plus the initial attempt.
	if got, want := rt.calls.Load(), int32(getJSONMaxRetries+1); got != want {
		t.Fatalf("calls = %d, want %d (initial attempt + %d retries)", got, want, getJSONMaxRetries)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to mention the final HTTP 503", err)
	}
}

func TestGetJSON_DoesNotRetryNonRetryableStatus(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusBadRequest, body: `{"error":"bad query"}`},
		{status: http.StatusOK, body: `{}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error for a 400")
	}
	if got := rt.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (a 400 should not be retried)", got)
	}
}

func TestGetJSON_DoesNotRetryPastCallerDeadline(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusTooManyRequests, body: `{"error":"throttled"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	// Give the deadline time to actually expire before the retry sleep would
	// otherwise fire, so this exercises the ctx.Err() short-circuit rather
	// than racing it.
	<-ctx.Done()

	err := c.getJSON(ctx, "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
	}
	if got := rt.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (should not retry once the caller's own deadline is gone)", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "5", 5 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds", "-1", 0},
		{"garbage", "not-a-number-or-date", 0},
		{"capped", "3600", retryAfterCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.in); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBackoffDelay_RetryAfterWins(t *testing.T) {
	if got, want := backoffDelay(1, 2*time.Second), 2*time.Second; got != want {
		t.Fatalf("backoffDelay with retryAfter set = %v, want %v", got, want)
	}
}

func TestBackoffDelay_ComputedIsBoundedAndPositive(t *testing.T) {
	for attempt := 1; attempt <= getJSONMaxRetries+2; attempt++ {
		d := backoffDelay(attempt, 0)
		if d <= 0 {
			t.Fatalf("backoffDelay(%d, 0) = %v, want > 0", attempt, d)
		}
		if d > getJSONMaxDelay {
			t.Fatalf("backoffDelay(%d, 0) = %v, want <= %v", attempt, d, getJSONMaxDelay)
		}
	}
}

// drainTrackingBody wraps a Reader and records whether it was ever read all
// the way to EOF before Close, so a test can prove the caller drained a
// response body rather than just reading a prefix and closing.
type drainTrackingBody struct {
	io.Reader
	reachedEOF bool
	closed     bool
}

func (b *drainTrackingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.reachedEOF = true
	}
	return n, err
}

func (b *drainTrackingBody) Close() error {
	b.closed = true
	return nil
}

type oneShotRoundTripper struct {
	status int
	body   *drainTrackingBody
}

func (rt *oneShotRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: rt.status,
		Body:       rt.body,
		Header:     make(http.Header),
	}, nil
}

func TestGetJSON_DrainsRetryableResponseBodyBeforeClosing(t *testing.T) {
	// Larger than the 512-byte sample getJSON reads for the error message,
	// so a caller that stops at the sample without draining the rest would
	// leave this body only partially read.
	longBody := `{"error":"` + strings.Repeat("x", 2000) + `"}`
	body := &drainTrackingBody{Reader: strings.NewReader(longBody)}
	rt := &oneShotRoundTripper{status: http.StatusServiceUnavailable, body: body}
	c := &Client{http: &http.Client{Transport: rt}}

	// getJSON retries a 503 up to getJSONMaxRetries times; every retry after
	// the first re-reads the same already-drained body (RoundTrip returns the
	// same *http.Response each call here), which just yields EOF immediately.
	// That only affects timing, not what this test checks: whether the FIRST
	// response was drained before Close.
	_ = c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))

	if !body.closed {
		t.Fatal("response body was never closed")
	}
	if !body.reachedEOF {
		t.Fatal("response body was closed without being drained to EOF — forces a fresh connection on retry")
	}
}
