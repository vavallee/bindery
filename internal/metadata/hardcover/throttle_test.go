package hardcover

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

// fakeClock drives the throttle without real time passing: sleeps advance the
// clock instead of blocking, so a test asserting eight seconds of pacing runs
// in microseconds.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	slept  []time.Duration
	sleeps int
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d > 0 {
		f.t = f.t.Add(d)
		f.slept = append(f.slept, d)
		f.sleeps++
	}
	return nil
}

func (f *fakeClock) totalSlept() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total time.Duration
	for _, d := range f.slept {
		total += d
	}
	return total
}

func newFakeThrottle() (*throttle, *fakeClock) {
	clk := newFakeClock()
	return &throttle{now: clk.now, sleep: clk.sleep}, clk
}

// TestThrottleUnpacedUntilRefused is the property that makes an adaptive pacer
// preferable to a fixed rate: an account that is never rate limited never pays
// for one. A fixed ceiling would have to be set for the free tier and would
// slow every paid account permanently.
func TestThrottleUnpacedUntilRefused(t *testing.T) {
	th, clk := newFakeThrottle()
	for i := 0; i < 50; i++ {
		if err := th.wait(context.Background()); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		th.succeed()
	}
	if clk.sleeps != 0 {
		t.Fatalf("an unrefused client must never sleep, slept %d times totalling %s", clk.sleeps, clk.totalSlept())
	}
}

func TestThrottleEngagesOnRejectionAndHardens(t *testing.T) {
	th, clk := newFakeThrottle()

	// First rejection engages the base spacing and holds for it.
	th.penalize(0)
	if err := th.wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := clk.totalSlept(); got != throttleBaseInterval {
		t.Errorf("first rejection should hold for %s, held %s", throttleBaseInterval, got)
	}

	// A second rejection doubles the spacing.
	th.penalize(0)
	th.mu.Lock()
	interval := th.interval
	th.mu.Unlock()
	if interval != 2*throttleBaseInterval {
		t.Errorf("second rejection should double the interval to %s, got %s", 2*throttleBaseInterval, interval)
	}

	// Hardening is capped.
	for i := 0; i < 20; i++ {
		th.penalize(0)
	}
	th.mu.Lock()
	interval = th.interval
	th.mu.Unlock()
	if interval != throttleMaxInterval {
		t.Errorf("hardening should cap at %s, got %s", throttleMaxInterval, interval)
	}
}

// TestThrottleHonoursServerHint covers the half of #2075 the report singles
// out: the body says "Try again in 1 seconds" and nothing consulted it.
func TestThrottleHonoursServerHint(t *testing.T) {
	th, clk := newFakeThrottle()
	th.penalize(5 * time.Second)
	if err := th.wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := clk.totalSlept(); got != 5*time.Second {
		t.Errorf("a 5s hint should hold for 5s, held %s", got)
	}
}

func TestThrottleClampsAbsurdHint(t *testing.T) {
	th, clk := newFakeThrottle()
	th.penalize(72 * time.Hour)
	if err := th.wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := clk.totalSlept(); got != throttleMaxHold {
		t.Errorf("an absurd hint must clamp to %s, held %s", throttleMaxHold, got)
	}
}

func TestThrottleDecaysBackToUnpaced(t *testing.T) {
	th, _ := newFakeThrottle()
	th.penalize(0)
	// Base 1s halves to 500ms, then 250ms is below base/2 and disengages, so
	// three decay rounds return the client to unpaced.
	for round := 0; round < 3; round++ {
		for i := 0; i < throttleDecaySuccesses; i++ {
			th.succeed()
		}
	}
	th.mu.Lock()
	interval := th.interval
	th.mu.Unlock()
	if interval != 0 {
		t.Errorf("sustained success should return the client to unpaced, interval still %s", interval)
	}
}

// TestThrottleFailsFastPastDeadline: sleeping into the caller's own timeout
// would report "Hardcover was slow" when what happened was "Hardcover refused",
// and that is precisely the distinction #2271 turns on.
func TestThrottleFailsFastPastDeadline(t *testing.T) {
	th, clk := newFakeThrottle()
	th.penalize(20 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline := clk.now().Add(2 * time.Second)
	ctx, cancelTimer := context.WithDeadline(ctx, deadline)
	defer cancelTimer()

	err := th.wait(ctx)
	if !errors.Is(err, errThrottled) {
		t.Fatalf("want errThrottled when the hold outlasts the deadline, got %v", err)
	}
	if clk.sleeps != 0 {
		t.Errorf("failing fast must not sleep, slept %d times", clk.sleeps)
	}
	// The rejected caller must not have consumed a slot: a caller with more
	// budget still gets the original hold, not the original plus one interval.
	th.mu.Lock()
	next := th.next
	th.mu.Unlock()
	if want := clk.now().Add(20 * time.Second); !next.Equal(want) {
		t.Errorf("a fast-failed caller must leave the queue untouched, next = %s want %s", next, want)
	}
}

func TestParseRetryHint(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want time.Duration
		ok   bool
	}{
		{"HTTP 429: API rate limit exceeded for tier 'Free'. Try again in 1 seconds.", time.Second, true},
		{"Token bucket exhausted, retry after 30s", 30 * time.Second, true},
		// Minutes and hours parse, but every hint is clamped to
		// throttleMaxHold, so anything past 30s comes back as 30s.
		{"retry in 2 minutes", throttleMaxHold, true},
		{"Try again in 0 seconds", 0, false},
		{"HTTP 502 (upstream returned a non-JSON response)", 0, false},
		{"", 0, false},
		// Clamped rather than honoured: a hint this large would bench the
		// provider for the rest of the process.
		{"retry after 9 hours", throttleMaxHold, true},
	} {
		got, ok := parseRetryHint(tc.msg)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseRetryHint(%q) = %s,%v want %s,%v", tc.msg, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseRetryAfterHeader(t *testing.T) {
	if got, ok := parseRetryAfterHeader("3"); !ok || got != 3*time.Second {
		t.Errorf("delay-seconds header: got %s,%v", got, ok)
	}
	if got, ok := parseRetryAfterHeader("900"); !ok || got != throttleMaxHold {
		t.Errorf("large delay-seconds header must clamp: got %s,%v", got, ok)
	}
	if _, ok := parseRetryAfterHeader("not-a-date"); ok {
		t.Error("unparsable header must report false so the body hint is tried instead")
	}
	if _, ok := parseRetryAfterHeader(""); ok {
		t.Error("absent header must report false")
	}
}

// TestQueryRetriesRateLimitThenSucceeds is the end-to-end shape: a 429 is
// retried rather than surfaced, and the caller sees the eventual success.
func TestQueryRetriesRateLimitThenSucceeds(t *testing.T) {
	th, _ := newFakeThrottle()
	var attempts int
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       io.NopCloser(strings.NewReader(`{"error":"API rate limit exceeded for tier 'Free'. Try again in 1 seconds."}`)),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"ok":true}}`)),
			Header:     make(http.Header),
		}, nil
	})
	c.throttle = th

	var out struct {
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	if err := c.query(context.Background(), "query Test { __typename }", nil, &out); err != nil {
		t.Fatalf("query: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected one retry after the 429, got %d attempts", attempts)
	}
	if !out.Data.OK {
		t.Error("expected the retry's payload to reach the caller")
	}
}

// TestQueryRateLimitIsClassifiable: once retries are exhausted the error must
// still say WHY, so a caller can tell a refusal from a genuine miss (#2271).
func TestQueryRateLimitIsClassifiable(t *testing.T) {
	th, _ := newFakeThrottle()
	var attempts int
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(`{"error":"API rate limit exceeded for tier 'Free'. Try again in 1 seconds."}`)),
			Header:     make(http.Header),
		}, nil
	})
	c.throttle = th

	var out struct{}
	err := c.query(context.Background(), "query Test { __typename }", nil, &out)
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("a 429 must be classifiable as ErrRateLimited, got %v", err)
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("the upstream wording must survive for the log: %v", err)
	}
	if attempts != hardcoverMaxRetries+1 {
		t.Errorf("expected %d attempts, got %d", hardcoverMaxRetries+1, attempts)
	}
}

// TestQueryDoesNotRetryTokenRejection: a 401 does not improve by being sent
// again, and retrying it would triple the load of a misconfigured install.
func TestQueryDoesNotRetryTokenRejection(t *testing.T) {
	var attempts int
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_token"}`)),
			Header:     make(http.Header),
		}, nil
	})

	var out struct{}
	if err := c.query(context.Background(), "query Test { __typename }", nil, &out); err == nil {
		t.Fatal("expected an error on 401")
	}
	if attempts != 1 {
		t.Errorf("a token rejection must not be retried, got %d attempts", attempts)
	}
	if errors.Is(errors.New("x"), ErrRateLimited) {
		t.Error("sanity: unrelated errors must not classify as rate limited")
	}
}

// TestThrottleSharedAcrossClientCopies guards the thing that is easy to break
// by hand: WithToken and WithTokenSource build a NEW Client, and dropping the
// throttle field there would give each copy its own budget while Hardcover
// counts them all against one account.
func TestThrottleSharedAcrossClientCopies(t *testing.T) {
	base := New()
	if base.pacer() == nil {
		t.Fatal("New() must attach a throttle")
	}
	if got := base.WithToken("t").pacer(); got != base.pacer() {
		t.Error("WithToken must carry the throttle forward")
	}
	if got := base.WithTokenSource(func(context.Context) string { return "t" }).pacer(); got != base.pacer() {
		t.Error("WithTokenSource must carry the throttle forward")
	}
	if NewAuthenticated("other").pacer() == base.pacer() {
		t.Error("standalone clients must not share cross-account penalties")
	}
}
