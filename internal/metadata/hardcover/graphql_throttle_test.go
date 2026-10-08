package hardcover

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func okWith(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// A rate limit Hardcover reports as a 200 with a GraphQL errors array is still
// a rate limit (#2791). It used to be fed to the throttle as a success, which
// never hardened the pacing and even decayed it, so the refusal the throttle
// exists to damp made Bindery send faster.
func TestQueryGraphQLRateLimitPenalisesThrottle(t *testing.T) {
	th, clk := newFakeThrottle()
	var attempts int
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		attempts++
		return okWith(`{"errors":[{"message":"Rate limit exceeded. Try again in 2 seconds.","extensions":{"code":"rate-limit"}}]}`), nil
	})
	c.throttle = th

	err := c.query(context.Background(), "query Test { __typename }", nil, new(struct{}))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a GraphQL rate limit classified as ErrRateLimited", err)
	}
	if attempts != hardcoverMaxRetries+1 {
		t.Errorf("attempts = %d, want %d: a refusal is retried like a 429", attempts, hardcoverMaxRetries+1)
	}
	if th.interval <= 0 {
		t.Error("throttle interval still zero: the refusal never reached the pacer")
	}
	if clk.totalSlept() == 0 {
		t.Error("retries were not paced: the server's hint was ignored")
	}
}

// A GraphQL error that is not a rate limit is not a success either. It must
// not advance the decay counter that unwinds pacing earlier refusals set up.
func TestQueryGraphQLErrorDoesNotDecayThrottle(t *testing.T) {
	th, _ := newFakeThrottle()
	th.interval = throttleBaseInterval
	var attempts int
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		attempts++
		return okWith(`{"errors":[{"message":"field 'nope' not found in type: 'query_root'"}]}`), nil
	})
	c.throttle = th

	err := c.query(context.Background(), "query Test { nope }", nil, new(struct{}))
	if err == nil || !strings.Contains(err.Error(), "GraphQL") {
		t.Fatalf("err = %v, want the GraphQL error surfaced", err)
	}
	if errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, a schema error is not a rate limit", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: a query error does not improve on retry", attempts)
	}
	if th.successes != 0 {
		t.Errorf("throttle successes = %d, want 0: an errors envelope is not a healthy request", th.successes)
	}
	if th.interval != throttleBaseInterval {
		t.Errorf("throttle interval = %v, want it left at %v", th.interval, throttleBaseInterval)
	}
}

// A clean answer still counts as one, so the pacing keeps relaxing.
func TestQueryCleanAnswerStillDecaysThrottle(t *testing.T) {
	th, _ := newFakeThrottle()
	th.interval = throttleBaseInterval
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		return okWith(`{"data":{"ok":true}}`), nil
	})
	c.throttle = th
	if err := c.query(context.Background(), "query Test { __typename }", nil, new(struct{})); err != nil {
		t.Fatalf("query: %v", err)
	}
	if th.successes != 1 {
		t.Errorf("throttle successes = %d, want 1", th.successes)
	}
}
