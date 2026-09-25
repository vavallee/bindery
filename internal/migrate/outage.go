package migrate

import (
	"errors"
	"log/slog"
	"slices"

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
}

// observe records one lookup's outcome.
//
// A primary that failed because it is rate limiting us is not down, and is
// left out of the streak. Hardcover's own pacer fails a lookup fast once its
// backoff would outlast the fan out's deadline, so a free account on a large
// import could put three throttled lookups in a row and fail every remaining
// row, where waiting lets the throttle relax. The fan out records failures in
// provider order with the primary first, so FirstErr is the primary's error
// whenever PrimaryFailed is set.
func (p *primaryOutage) observe(source string, o metadata.SearchOutcome) {
	var daily *metadata.DailyQuotaError
	switch {
	case errors.As(o.FirstErr, &daily):
		p.streak, p.last = primaryOutageThreshold, o
	case o.Primary != "" && slices.Contains(o.Answered, o.Primary):
		p.streak = 0
	case o.PrimaryFailed && errors.Is(o.FirstErr, hardcover.ErrRateLimited):
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

// down reports whether the importer should stop asking the primary.
func (p *primaryOutage) down() bool {
	return p.streak >= primaryOutageThreshold
}

// reason is the per row message for a row failed without a lookup.
func (p *primaryOutage) reason() string {
	return primaryDownReason(p.last)
}
