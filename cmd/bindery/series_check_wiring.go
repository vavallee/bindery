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

// seriesCheckFunc is the one SeriesHandler method the series monitoring wiring
// calls, named so a test can stand in for the handler.
type seriesCheckFunc func(ctx context.Context, series *models.Series) (api.SeriesCheckResult, error)

// newSeriesChecker adapts the series handler to the scheduler's series
// monitoring job (#2523), translating errors into the job's flags the way
// newAuthorDiscoverer does: a rate limit from any provider is Backoff, a check
// already running is Busy, a server error, network failure or timeout is
// Unavailable, and anything else is about the one series.
func newSeriesChecker(check seriesCheckFunc, bulkRunning func() bool) scheduler.SeriesChecker {
	return scheduler.SeriesCheckerFuncs{
		Check: func(ctx context.Context, series *models.Series) scheduler.SeriesCheckOutcome {
			res, err := check(ctx, series)
			out := scheduler.SeriesCheckOutcome{
				Skipped: err == nil && res.Status == "",
				Status:  res.Status,
				Message: res.Message,
				Created: res.Created,
				Err:     err,
				Backoff: metadata.IsProviderHold(err) || errors.Is(err, metadata.ErrRateLimited) || errors.Is(err, hardcover.ErrRateLimited),
				Busy:    errors.Is(err, api.ErrSeriesCheckRunning),
			}
			out.Unavailable = !out.Backoff && !out.Busy && metadata.IsProviderUnavailable(err)
			return out
		},
		BulkRunning: bulkRunning,
	}
}
