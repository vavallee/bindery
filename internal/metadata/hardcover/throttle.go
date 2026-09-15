package hardcover

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hardcover throttles per account, and its free tier throttles readily: a
// single 72-item Audiobookshelf import produced 86 rate-limit rejections
// across four call sites (#2075). Before this, Client.query made one request
// and returned whatever came back, so a burst of parallel lookups sent every
// request, had most of them refused, and surfaced each refusal to a caller
// that could not tell "Hardcover does not have this" from "Hardcover would
// not answer" (#2271).
//
// The pacing here is deliberately ADAPTIVE rather than a fixed rate. A fixed
// ceiling would have to be set for the free tier, which would slow every paid
// account down permanently to buy nothing; and it would still be wrong,
// because the tier is not something Bindery can see. So the throttle costs
// nothing until Hardcover actually refuses a request, engages on the first
// 429, hardens on each further one, and decays back to unrestricted after a
// run of successes. An account that is never refused is never paced.
const (
	// throttleBaseInterval is the spacing engaged by the first rejection. It
	// matches the hint Hardcover's own free-tier message carries ("Try again
	// in 1 seconds"), so the first correction is the one the server asked for
	// rather than a number invented here.
	throttleBaseInterval = time.Second

	// throttleMaxInterval caps the hardening. Eight seconds is already at the
	// aggregator's per-provider search budget (searchProviderTimeout), so
	// spacing beyond it would only convert rejections into deadline errors.
	throttleMaxInterval = 8 * time.Second

	// throttleMaxHold caps how long a single rejection may stall the client,
	// however large a retry hint the server sends. A malformed or hostile
	// hint must not be able to bench Hardcover for the rest of the process.
	throttleMaxHold = 30 * time.Second

	// throttleDecaySuccesses is how many consecutive successful requests halve
	// the current spacing. Twenty is roughly one author's worth of catalogue
	// work, so a bulk import that hits one early rejection relaxes again
	// within that import rather than staying paced until restart.
	throttleDecaySuccesses = 20

	// hardcoverMaxRetries bounds retries of a single query. Each retry waits
	// out the pacing above, so this is a small number by design: three
	// attempts at up to throttleMaxInterval apiece is the worst case a caller
	// can be made to wait, and the aggregator's own timeout is shorter.
	hardcoverMaxRetries = 2
)

// errThrottled reports that the local pacer would have had to sleep past the
// caller's own deadline. Failing fast is better than sleeping into a context
// timeout: the caller learns Hardcover was rate limited rather than that it
// was slow, which is the distinction #2271 turns on.
var errThrottled = errors.New("rate limited locally, and waiting would exceed the caller's deadline")

// throttle implements adaptive pacing; Quota stores its state per account.
type throttle struct {
	mu sync.Mutex
	// interval is the current spacing between requests. Zero means
	// unrestricted, which is the state until Hardcover first refuses.
	interval time.Duration
	// next is the earliest time the next request may start.
	next time.Time
	// successes counts consecutive successful requests at the current
	// interval, for decay.
	successes int

	// now and sleep are injectable so the tests can drive the pacer without
	// real time passing.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newThrottle() *throttle {
	return &throttle{now: time.Now, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wait blocks until this request's slot. It consumes the slot only if it can
// be honoured within the caller's deadline; otherwise it returns errThrottled
// and leaves the queue untouched for a caller with more budget.
func (t *throttle) wait(ctx context.Context) error {
	if t == nil {
		return nil
	}
	delay, ok := t.reserve(ctx)
	if !ok {
		return errThrottled
	}
	return t.sleep(ctx, delay)
}

func (t *throttle) reserve(ctx context.Context) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	start := now
	if t.next.After(start) {
		start = t.next
	}
	delay := start.Sub(now)
	if delay > 0 {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline && start.After(deadline) {
			return 0, false
		}
	}
	t.next = start.Add(t.interval)
	return delay, true
}

// penalize records that Hardcover refused a request. It hardens the spacing
// and holds the whole client off for the length of the server's own hint,
// which is why a retry needs no separate backoff: the next wait already sleeps
// for it, and so does every other in-flight caller.
func (t *throttle) penalize(hint time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.successes = 0
	if t.interval <= 0 {
		t.interval = throttleBaseInterval
	} else {
		t.interval *= 2
	}
	if t.interval > throttleMaxInterval {
		t.interval = throttleMaxInterval
	}
	hold := hint
	if hold < t.interval {
		hold = t.interval
	}
	if hold > throttleMaxHold {
		hold = throttleMaxHold
	}
	if until := t.now().Add(hold); until.After(t.next) {
		t.next = until
	}
}

// succeed records a request Hardcover answered, decaying the spacing after a
// run of them so a single early rejection does not pace the rest of the
// process.
func (t *throttle) succeed() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.interval <= 0 {
		return
	}
	t.successes++
	if t.successes < throttleDecaySuccesses {
		return
	}
	t.successes = 0
	t.interval /= 2
	if t.interval < throttleBaseInterval/2 {
		t.interval = 0
	}
}

// retryHintRe matches the "come back in N units" clause Hardcover puts in its
// rate-limit body. The free tier sends "Try again in 1 seconds"; the edge has
// also been seen to say "retry after 30s". Both are text in a JSON field,
// not a header, which is why this parses the message rather than only
// Retry-After.
var retryHintRe = regexp.MustCompile(`(?i)(?:try again in|retry after|retry in)\s+(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)?\b`)

// parseRetryHint reads Hardcover's own retry hint out of an error message,
// clamped to throttleMaxHold. Reports false when the message carries none,
// which is the common case for a 5xx.
func parseRetryHint(msg string) (time.Duration, bool) {
	m := retryHintRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 {
		// Unreachable for a well-formed match, but a 20-digit number
		// overflows Atoi and must not be read as "no wait at all".
		return 0, false
	}
	unit := time.Second
	switch strings.ToLower(m[2]) {
	case "m", "min", "mins", "minute", "minutes":
		unit = time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		unit = time.Hour
	}
	d := time.Duration(n) * unit
	if d <= 0 {
		return 0, false
	}
	if d > throttleMaxHold {
		d = throttleMaxHold
	}
	return d, true
}

// parseRetryAfterHeader reads a standard Retry-After header, accepting a
// delay-in-seconds or an HTTP-date, clamped like parseRetryHint. Hardcover is
// not known to send one, so this is belt and braces: if it starts to, the
// header is more authoritative than the prose.
func parseRetryAfterHeader(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0, false
		}
		d := time.Duration(secs) * time.Second
		if d > throttleMaxHold {
			d = throttleMaxHold
		}
		return d, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			if d > throttleMaxHold {
				d = throttleMaxHold
			}
			return d, true
		}
	}
	return 0, false
}

// isRetryableStatus reports whether a non-200 is a transient upstream state
// worth another attempt. A 401/403 is about the token and a 400 is about the
// query; neither improves by being sent again.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// throttledError annotates an error with the fact that Hardcover refused for
// rate-limit reasons, so callers that must distinguish "no such record" from
// "no answer" (#2271) can do it without matching on message text.
type throttledError struct{ err error }

func (e *throttledError) Error() string { return e.err.Error() }
func (e *throttledError) Unwrap() error { return e.err }

// ErrRateLimited marks any error caused by Hardcover refusing to answer.
// errors.Is(err, ErrRateLimited) is the supported check.
var ErrRateLimited = errors.New("hardcover rate limited")

func (e *throttledError) Is(target error) bool { return target == ErrRateLimited }

// rateLimited wraps err so errors.Is(err, ErrRateLimited) reports true while
// the original message (and Hardcover's own wording) still reaches the log.
func rateLimited(err error) error {
	if err == nil {
		return nil
	}
	return &throttledError{err: err}
}
