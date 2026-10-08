package openlibrary

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A refusal holds every request to OpenLibrary, not just the one refused.
//
// #2075: a bulk import has several catalogue fetches and edition samples in
// flight at once. Each used to back off on its own schedule, so while one
// waited out OpenLibrary's Retry-After the others kept sending, and the 429s
// escalated into timeouts and connection refusals. Here a second, unrelated
// lookup starts while the first is waiting out a one second Retry-After, and
// it must wait too.
func TestGetJSON_RefusalHoldsOtherRequests(t *testing.T) {
	refused := make(chan time.Time, 1)
	var mu sync.Mutex
	var otherSentAt time.Time

	c := New()
	c.http = &http.Client{Transport: roundTripFn(func(r *http.Request) (*http.Response, error) {
		ok := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}
		if strings.HasSuffix(r.URL.Path, "/other.json") {
			mu.Lock()
			otherSentAt = time.Now()
			mu.Unlock()
			return ok, nil
		}
		select {
		case refused <- time.Now():
			h := make(http.Header)
			h.Set("Retry-After", "1")
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h,
				Body: io.NopCloser(strings.NewReader(`{"error":"Too Many Requests"}`))}, nil
		default:
			return ok, nil
		}
	})}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- c.getJSON(context.Background(), baseURL+"/authors/OL1A/works.json", new(struct{}))
	}()

	refusedAt := <-refused
	// The transport hands the 429 back after signalling; give the first
	// lookup a moment to read it before the second one starts.
	time.Sleep(100 * time.Millisecond)
	if err := c.getJSON(context.Background(), baseURL+"/works/other.json", new(struct{})); err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first lookup: %v", err)
	}

	mu.Lock()
	gap := otherSentAt.Sub(refusedAt)
	mu.Unlock()
	if gap < 900*time.Millisecond {
		t.Fatalf("second lookup was sent %v after OpenLibrary asked for 1s of quiet, want it held until the Retry-After passed", gap)
	}
}

// A hold that outlasts the caller's deadline fails the request up front as a
// rate limit rather than sleeping into a timeout, so the caller learns
// OpenLibrary was refusing rather than that it was slow.
func TestGetJSON_HoldPastDeadlineFailsAsRateLimited(t *testing.T) {
	var calls int
	c := New()
	c.http = &http.Client{Transport: roundTripFn(func(*http.Request) (*http.Response, error) {
		calls++
		h := make(http.Header)
		h.Set("Retry-After", "20")
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h,
			Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := c.getJSON(ctx, baseURL+"/authors/OL1A.json", new(struct{}))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("getJSON took %v, want it to give up at once rather than sleep into its deadline", elapsed)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a rate limit", err)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1: the retry could not have been sent in time", calls)
	}
}
