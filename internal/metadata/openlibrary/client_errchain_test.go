package openlibrary

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

// errRoundTripper returns a fixed error for every request, mimicking the way
// http.Client.Do surfaces transport failures: wrapped in a *url.Error.
type errRoundTripper struct{ err error }

func (rt errRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: rt.err}
}

// TestGetJSONPreservesContextCanceled asserts that getJSON wraps transport
// errors with %w so the chain survives. Previously getJSON flattened the error
// via errors.New(RedactSecrets(err.Error())), which broke errors.Is and forced
// callers to classify failures by matching on the message string.
func TestGetJSONPreservesContextCanceled(t *testing.T) {
	c := &Client{http: &http.Client{Transport: errRoundTripper{err: context.Canceled}}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false; chain was flattened. err = %v", err)
	}
}

// TestGetJSONPreservesDeadlineExceeded covers the timeout case the same way.
func TestGetJSONPreservesDeadlineExceeded(t *testing.T) {
	c := &Client{http: &http.Client{Transport: errRoundTripper{err: context.DeadlineExceeded}}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; chain was flattened. err = %v", err)
	}
}

// TestGetJSONRealCancelledContext exercises the end-to-end path with a context
// that is already cancelled before the request is issued, so the cancellation
// originates from the real transport rather than an injected error.
func TestGetJSONRealCancelledContext(t *testing.T) {
	c := New() // real proxy transport; the cancelled context short-circuits the dial

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.getJSON(ctx, "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error for a pre-cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false. err = %v", err)
	}
}
