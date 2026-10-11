package migrate

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

// primaryOutageThreshold is how many metadata lookups in a row may go
// unanswered by the primary provider before a bulk import stops asking it
// for the rest of the run.
//
// Every such lookup costs the primary's full timeout: 8s per CSV or Readarr
// row (the search fan out's per provider cap) and about a minute per
// Goodreads ISBN lookup against a black holed OpenLibrary (four attempts at
// the client's 15s timeout plus backoff). And since #2332 none of those rows
// can bind anyway, so an outage used to spend minutes to hours on an import
// that failed every row (#2613).
//
// One unanswered lookup is a blip a slow provider produces under load, and
// two can be a short stall. Three in a row with nothing answered in between
// is 24s of silence on the cheapest path, which is an outage. Stopping there
// costs at most three lookups' worth of waiting, and a false trip costs only
// a rerun: the rows it gave up on could not have bound while the primary was
// failing, and rows already added are skipped the second time.
const primaryOutageThreshold = 3

// primaryOutage tracks, across one import run, how many consecutive lookups
// the primary provider failed. A lookup the primary answered, match or not,
// resets it. Once the streak reaches primaryOutageThreshold the importer
// fails every remaining row without asking, with the same "did not answer"
// reason those rows would have got after waiting out the timeout.
//
// It is per run on purpose, not a process wide circuit breaker: the next
// import, or the next manual add, asks the primary afresh.
type primaryOutage struct {
	streak int
	// last is the outcome that extended the streak, kept so the rows failed
	// without asking name the same primary.
	last metadata.SearchOutcome
	// waited is how long this run has spent waiting out primary holds,
	// against primaryHoldBudget.
	waited time.Duration
}

// observe records one lookup's outcome.
//
// A primary that failed because it is rate limiting us is not down, and is
// left out of the streak here, whichever provider it is. Hardcover's pacer and
// the shared provider request loop both fail a lookup fast once a hold would
// outlast the fan out's deadline, so a single 429 could otherwise put three
// refused lookups in a row within a millisecond and fail every remaining row,
// where waiting lets the hold pass (#2075). lookupWaitingOutHolds does the
// waiting, and counts a lookup that is still refused afterwards through
// refusedAfterWaiting, so a primary that never stops refusing still trips
// the breaker. The fan out records failures in
// provider order with the primary first, so FirstErr is the primary's error
// whenever PrimaryFailed is set.
func (p *primaryOutage) observe(source string, o metadata.SearchOutcome) {
	var daily *metadata.DailyQuotaError
	switch {
	// Only the primary's hold stops the import. A held enricher's error can be
	// FirstErr while the primary answered, and that lookup is fine.
	case o.PrimaryFailed && errors.As(o.FirstErr, &daily):
		p.streak, p.last = primaryOutageThreshold, o
	case o.Primary != "" && slices.Contains(o.Answered, o.Primary):
		p.streak = 0
	case o.PrimaryFailed && isRateLimited(o.FirstErr):
		// Throttled, not down: neither extend nor reset the streak.
	case o.PrimaryFailed:
		p.streak++
		p.last = o
		if p.streak == primaryOutageThreshold {
			slog.Warn(source+" import: primary metadata provider is not answering, failing the remaining rows without asking it",
				"primary", o.Primary, "consecutiveFailures", p.streak, "error", o.FirstErr)
		}
	}
}

// refusedAfterWaiting records a lookup the primary still refused after the
// import waited its holds out, or once the run's wait budget was spent. That
// is no longer a pause the import can sit through, so it extends the streak
// like any unanswered lookup.
func (p *primaryOutage) refusedAfterWaiting(source string, o metadata.SearchOutcome) {
	p.streak++
	p.last = o
	if p.streak == primaryOutageThreshold {
		slog.Warn(source+" import: primary metadata provider keeps refusing for rate limit reasons, failing the remaining rows without asking it",
			"primary", o.Primary, "consecutiveRefusals", p.streak, "error", o.FirstErr)
	}
}

// holdBudgetSpent reports whether waiting a further wait would take this run
// past primaryHoldBudget.
func (p *primaryOutage) holdBudgetSpent(wait time.Duration) bool {
	return p.waited+wait > primaryHoldBudget
}

// down reports whether the importer should stop asking the primary.
func (p *primaryOutage) down() bool {
	return p.streak >= primaryOutageThreshold
}

// reason is the per row message for a row failed without a lookup.
func (p *primaryOutage) reason() string {
	if isRateLimited(p.last.FirstErr) {
		return primaryRateLimitedReason(p.last)
	}
	return primaryDownReason(p.last)
}

// primaryRateLimitedReason is the per row message once a primary that kept
// refusing for rate limit reasons tripped the breaker. Nothing is wrong with
// the row; the provider wants fewer requests, so the advice is to wait.
func primaryRateLimitedReason(o metadata.SearchOutcome) string {
	return fmt.Sprintf("primary metadata provider %s is rate limiting requests, try the import again later", o.Primary)
}

// isRateLimited reports a provider refusal for rate limit reasons: the shared
// sentinel every provider client marks with, or Hardcover's own, which its
// pacer also returns bare.
func isRateLimited(err error) bool {
	return errors.Is(err, metadata.ErrRateLimited) || errors.Is(err, hardcover.ErrRateLimited)
}
