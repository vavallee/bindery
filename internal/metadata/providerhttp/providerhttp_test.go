package providerhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/providererr"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string, header map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// doErr runs Do for a test that expects an error, closing any response.
func doErr(ctx context.Context, c *http.Client, gate *Gate, r Request) error {
	resp, err := Do(ctx, c, gate, r)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
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
		{"capped", "3600", RetryAfterCap},
		{"overflowing", "99999999999999999999", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseRetryAfter(tc.in); got != tc.want {
				t.Fatalf("ParseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
	if got := ParseRetryAfter(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)); got != RetryAfterCap {
		t.Fatalf("ParseRetryAfter(an hour from now) = %v, want the %v cap", got, RetryAfterCap)
	}
}

func TestBackoffDelay(t *testing.T) {
	if got := BackoffDelay(1, 2*time.Second); got != 2*time.Second {
		t.Fatalf("BackoffDelay with a Retry-After = %v, want the server's 2s", got)
	}
	for attempt := 0; attempt <= 70; attempt++ {
		d := BackoffDelay(attempt, 0)
		if d <= 0 || d > MaxDelay {
			t.Fatalf("BackoffDelay(%d, 0) = %v, want within (0, %v]", attempt, d, MaxDelay)
		}
	}
}

func TestStatusErrorClassification(t *testing.T) {
	for code, want := range map[int]error{
		http.StatusTooManyRequests:     providererr.ErrRateLimited,
		http.StatusServiceUnavailable:  providererr.ErrUnavailable,
		http.StatusInternalServerError: providererr.ErrUnavailable,
	} {
		if err := (&StatusError{Code: code}); !errors.Is(err, want) {
			t.Errorf("HTTP %d: errors.Is(%v) = false", code, want)
		}
	}
	err := &StatusError{Code: http.StatusBadRequest}
	if errors.Is(err, providererr.ErrRateLimited) || errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("a 400 is about the request, not the provider: %v", err)
	}
}

// Pass hands a listed status back for the caller to interpret, unretried.
func TestDoPassesListedStatus(t *testing.T) {
	var calls atomic.Int32
	c := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusNotFound, "", nil), nil
	})}
	resp, err := Do(context.Background(), c, nil, Request{URL: "https://example.invalid/x", Pass: []int{http.StatusNotFound}})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || calls.Load() != 1 {
		t.Fatalf("status %d after %d requests, want one 404", resp.StatusCode, calls.Load())
	}
}

// A refusal on one call holds the next call through the same gate for the
// server's Retry-After.
func TestDoRefusalHoldsTheGate(t *testing.T) {
	var calls atomic.Int32
	c := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusTooManyRequests, "slow down", map[string]string{"Retry-After": "1"}), nil
	})}
	gate := NewGate()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// The first request is refused, the retry waits out the second and is
	// refused again, and the next hold would end past the deadline, so the
	// refusal comes back instead of a third request.
	err := doErr(ctx, c, gate, Request{URL: "https://example.invalid/a"})
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("err = %v, want a rate limit", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}

	// A fresh call with a short budget does not send at all.
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	err = doErr(short, c, gate, Request{Provider: "example", URL: "https://example.invalid/b"})
	var held *HeldError
	if !errors.As(err, &held) || !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("err = %v, want a *HeldError matching ErrRateLimited", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want still 2: the held call must not be sent", got)
	}
}

// Redact flattens a transport error, whose message embeds the URL and with it
// any key in the query string (#1144).
func TestDoRedactsTransportErrors(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := doErr(ctx, c, nil, Request{URL: "https://example.invalid/v?key=SECRETKEY123", Redact: true})
	if err == nil || strings.Contains(err.Error(), "SECRETKEY123") {
		t.Fatalf("err = %v, want an error without the key", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Fatalf("err = %v still carries the *url.Error that embeds the key", err)
	}
}

// Without Redact the chain survives, so callers can classify cancellations.
func TestDoKeepsTransportErrorChain(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := doErr(ctx, c, nil, Request{Provider: "example", URL: "https://example.invalid/v"})
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "example request: ") {
		t.Fatalf("err = %v, want a prefixed error matching context.Canceled", err)
	}
}
