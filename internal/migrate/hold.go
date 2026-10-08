package migrate

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

// primaryHoldRetries bounds how many times one lookup waits out a primary
// that turned it away and asks again, before the row gets its usual failure.
const primaryHoldRetries = 3

// primaryHoldMaxWait caps one wait, matching the longest hold a provider
// client will set from a Retry-After.
const primaryHoldMaxWait = providerhttp.RetryAfterCap

// primaryHoldWait is the wait when the primary refused for rate limit reasons
// without saying until when, as Hardcover's pacer does. A var so tests need
// not wait it out.
var primaryHoldWait = 2 * time.Second

// primaryHoldBudget bounds the time one import run may spend waiting out
// holds, across all its rows. The Readarr and Goodreads imports run detached
// from the request that started them and cannot be cancelled, so without a
// budget a primary that refuses for hours would keep the run waiting, and
// asking, for hours. Once it is spent, a refused lookup is not waited out
// again. A var so tests need not spend it in real time.
var primaryHoldBudget = 10 * time.Minute

// lookupWaitingOutHolds runs one importer lookup, and when the primary turned
// it away because it was holding its requests, waits the hold out and asks
// again rather than failing the row.
//
// The provider clients fail a held request at once instead of sleeping into
// the lookup's own deadline (#2075). That is right for an interactive search,
// but an import has nothing better to do than wait: failing the row would
// make the user rerun it, and failing rows in a row would trip the outage
// breaker in well under a second on the back of a single 429 and fail every
// row after it.
//
// Waiting is bounded twice over. A lookup still refused after
// primaryHoldRetries waits, or refused once the run's primaryHoldBudget is
// spent, counts toward the outage streak, so a primary that keeps refusing
// trips the breaker after primaryOutageThreshold such rows and the rest fail
// at once with a "try again later" reason rather than each asking again.
func lookupWaitingOutHolds[T any](ctx context.Context, outage *primaryOutage, source string, lookup func() (T, metadata.SearchOutcome, error)) (T, metadata.SearchOutcome, error) {
	for attempt := 0; ; attempt++ {
		v, o, err := lookup()
		wait, held := primaryHold(o)
		if !held {
			return v, o, err
		}
		if attempt == primaryHoldRetries || outage.holdBudgetSpent(wait) {
			outage.refusedAfterWaiting(source, o)
			return v, o, err
		}
		slog.Info(source+" import: primary metadata provider is holding requests, waiting before asking again",
			"primary", o.Primary, "wait", wait, "error", o.FirstErr)
		start := time.Now()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return v, o, err
		case <-timer.C:
		}
		outage.waited += time.Since(start)
	}
}

// primaryHold reports whether the outcome's primary failed because it was
// holding requests, and how long to wait before asking it again. A held
// request carries the time the hold ends; any other rate limit refusal gets
// primaryHoldWait.
func primaryHold(o metadata.SearchOutcome) (time.Duration, bool) {
	if !o.PrimaryFailed || o.FirstErr == nil {
		return 0, false
	}
	var held *providerhttp.HeldError
	switch {
	case errors.As(o.FirstErr, &held):
		return min(max(time.Until(held.Until), 0), primaryHoldMaxWait), true
	case isRateLimited(o.FirstErr):
		return primaryHoldWait, true
	}
	return 0, false
}
