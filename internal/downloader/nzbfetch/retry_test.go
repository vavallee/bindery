package nzbfetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// noBackoff removes the sleep between attempts so the retry tests measure
// behaviour rather than wall-clock.
func noBackoff(t *testing.T) {
	t.Helper()
	prev := backoffFor
	backoffFor = func(int) time.Duration { return 0 }
	t.Cleanup(func() { backoffFor = prev })
}

// timeoutErr is the shape a stalled indexer produces: net.Error with
// Timeout() true, which is what http.Client returns when its own deadline
// fires part-way through a request.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "simulated i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

// A timeout is the failure this whole change exists for: the same release
// fetched fine minutes later, so one attempt was never a fair test.
func TestRetry_RetriesATimeoutThenSucceeds(t *testing.T) {
	noBackoff(t)
	calls := 0
	got, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, timeoutErr{}
		}
		return []byte("<nzb/>"), nil
	})
	if err != nil {
		t.Fatalf("retry should have recovered: %v", err)
	}
	if string(got) != "<nzb/>" {
		t.Errorf("got %q, want the successful fetch's body", got)
	}
	if calls != 3 {
		t.Errorf("made %d attempts, want 3", calls)
	}
}

// A body that stops short mid-read is the other half of the observed failure.
func TestRetry_RetriesATruncatedBodyRead(t *testing.T) {
	noBackoff(t)
	calls := 0
	if _, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		if calls < 2 {
			return nil, io.ErrUnexpectedEOF
		}
		return []byte("<nzb/>"), nil
	}); err != nil {
		t.Fatalf("retry should have recovered: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d attempts, want 2", calls)
	}
}

// A connection refused is transient in the same way — the indexer may be
// mid-deploy or briefly out of capacity.
func TestRetry_RetriesAConnectionError(t *testing.T) {
	noBackoff(t)
	calls := 0
	connErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if _, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		if calls < 2 {
			return nil, connErr
		}
		return []byte("<nzb/>"), nil
	}); err != nil {
		t.Fatalf("retry should have recovered: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d attempts, want 2", calls)
	}
}

// An indexer that refuses the grab means it three times as fast as once, and
// hammering a rate-limited indexer is how the limit got hit. Error() builds
// exactly these, so the predicate has to leave them alone.
func TestRetry_DoesNotRetryAnIndexerRejection(t *testing.T) {
	noBackoff(t)
	rejection := Error("https://indexer.example.com/getnzb",
		&http.Response{StatusCode: 400, Request: endedAt(t, "https://indexer.example.com/getnzb")},
		[]byte(nzbfinder203))

	calls := 0
	_, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		return nil, rejection
	})
	if err == nil {
		t.Fatal("expected the rejection to be returned")
	}
	if calls != 1 {
		t.Errorf("made %d attempts, want 1 — an indexer rejection is not transient", calls)
	}
	if !strings.Contains(err.Error(), "newznab error 203") {
		t.Errorf("the original rejection must survive: %v", err)
	}
}

// Anything the predicate cannot positively identify as transient is left
// alone. Retrying by default would turn every permanent misconfiguration into
// three requests.
func TestRetry_DoesNotRetryAnUnrecognisedError(t *testing.T) {
	noBackoff(t)
	calls := 0
	if _, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		return nil, errors.New("url not allowed: loopback")
	}); err == nil {
		t.Fatal("expected the error to be returned")
	}
	if calls != 1 {
		t.Errorf("made %d attempts, want 1", calls)
	}
}

// A cancelled caller is not an indexer flake. Retrying here would keep working
// after the request that wanted the result has gone.
func TestRetry_StopsWhenTheCallerCancels(t *testing.T) {
	noBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	if _, err := Retry(ctx, func(context.Context) ([]byte, error) {
		calls++
		return nil, timeoutErr{}
	}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("made %d attempts, want 1 — the caller had already given up", calls)
	}
}

func TestRetry_GivesUpAfterTheAttemptLimit(t *testing.T) {
	noBackoff(t)
	calls := 0
	_, err := Retry(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		return nil, timeoutErr{}
	})
	if err == nil {
		t.Fatal("expected the last failure to be returned")
	}
	if calls != Attempts {
		t.Errorf("made %d attempts, want %d", calls, Attempts)
	}
	if !strings.Contains(err.Error(), "simulated i/o timeout") {
		t.Errorf("the last failure must survive: %v", err)
	}
}

// The backoff must be interruptible: a caller that gives up while the retry is
// sleeping should not have to wait the sleep out. The cancel happens from
// outside, after the first attempt has already been judged transient, so this
// exercises the pause rather than the predicate.
func TestRetry_CancellationInterruptsTheBackoff(t *testing.T) {
	prev := backoffFor
	backoffFor = func(int) time.Duration { return 30 * time.Second }
	t.Cleanup(func() { backoffFor = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct{}, Attempts)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Retry(ctx, func(context.Context) ([]byte, error) {
			calls <- struct{}{}
			return nil, timeoutErr{}
		})
	}()

	<-calls // first attempt has run and is now backing off
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Retry slept through a cancelled context")
	}
	if got := len(calls); got != 0 {
		t.Errorf("made %d further attempts after cancellation, want 0", got)
	}
}
