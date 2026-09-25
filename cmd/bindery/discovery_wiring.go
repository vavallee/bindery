package main

import (
	"context"
	"errors"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/scheduler"
)

// authorDiscoverFunc is the one AuthorHandler method the discovery wiring
// calls, named so a test can stand in for the handler.
type authorDiscoverFunc func(ctx context.Context, author *models.Author) (int, error)

// newAuthorDiscoverer adapts the author handler to the scheduler's discovery
// job (#2236). The scheduler imports neither internal/api nor the metadata
// providers, so this is where their errors become flags: a rate limit from
// any provider (metadata.ErrRateLimited, which the OpenLibrary and Hardcover
// clients both mark) is Backoff (stop the pass, leave the cursor), an author
// whose sync is already running is Busy (skip it, leave the cursor), a
// server error, network failure or timeout is Unavailable (counts toward the
// job's failure breaker), and anything else, such as a not found, is an error
// about that one author, stamped as checked.
func newAuthorDiscoverer(discover authorDiscoverFunc, bulkRunning func() bool) scheduler.AuthorDiscoverer {
	return scheduler.AuthorDiscovererFuncs{
		Discover: func(ctx context.Context, author *models.Author) scheduler.DiscoveryOutcome {
			created, err := discover(ctx, author)
			var daily *metadata.DailyQuotaError
			out := scheduler.DiscoveryOutcome{
				Created: created,
				Err:     err,
				// hardcover.ErrRateLimited is checked too: the bare sentinel does
				// not carry the shared mark, only its throttle errors do.
				Backoff: errors.As(err, &daily) || errors.Is(err, metadata.ErrRateLimited) || errors.Is(err, hardcover.ErrRateLimited),
				Busy:    errors.Is(err, api.ErrAuthorSyncRunning),
			}
			out.Unavailable = !out.Backoff && !out.Busy && metadata.IsProviderUnavailable(err)
			return out
		},
		BulkRunning: bulkRunning,
	}
}
