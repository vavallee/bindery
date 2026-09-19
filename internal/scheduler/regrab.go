package scheduler

import (
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// deadRegrabCooldown is how long a dead download row (failed, importBlocked)
// keeps blocking the scheduler's automatic re-grab of the same release.
//
// A finished attempt must not block a re-grab for good (#2710): the causes are
// usually transient, and a release poisoned by one indexer 429 stayed
// ungrabbable until the user deleted the queue row by hand. But it must not be
// retried on every sweep either, because a release that fails at the download
// client fails again the moment it is re-sent, and only the stall path
// blocklists. Six hours sits between the two: shorter than the default twelve
// hour search cadence, so the ordinary next sweep of that book retries the
// release, and longer than the one hour minimum cadence, so the tightest
// configured sweep still cannot hammer the client with it.
//
// A person clicking Grab is not bound by this. The manual path (api.grab) has
// always retried a dead row immediately, and still does.
const deadRegrabCooldown = 6 * time.Hour

// blockingRegrabReason reports why an existing download row for the release's
// GUID stops the scheduler from grabbing that release again, or "" when the
// row may be reused.
//
// The reason is short because it is logged as the search outcome, next to the
// blocking row's status. Before #2710 this skip wrote nothing at all: the
// "auto-grabbing book" line had already been logged, so the log read as though
// the grab went ahead.
func blockingRegrabReason(existing *models.Download, now time.Time) string {
	switch {
	case existing == nil:
		return ""
	case existing.BlocksRegrab():
		return "already grabbed"
	case existing.Status.IsDeadForRegrab() && existing.LastActivityAt().After(now.Add(-deadRegrabCooldown)):
		return "failed too recently"
	default:
		return ""
	}
}
