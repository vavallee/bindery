package scheduler

import (
	"context"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// deadRegrabCooldown is how long a failed download row keeps blocking the
// scheduler's automatic re-grab of the same release, measured from the moment
// the row died (models.Download.DeadSince, dead_at).
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

// regrabReasonImportBlocked and the other reasons below name a skip in the
// search outcome and in the log line that accompanies it. They are short
// because they are read next to the blocking row's status.
const (
	regrabReasonLiveWork      = "already grabbed"
	regrabReasonTooRecent     = "failed too recently"
	regrabReasonImportBlocked = "import blocked, not retried automatically"
)

// blockingRegrabReason reports why an existing download row for the release's
// GUID stops the scheduler from grabbing that release again, or "" when the
// row may be reused.
//
// Before #2710 this skip wrote nothing at all: the "auto-grabbing book" line
// had already been logged, so the log read as though the grab went ahead.
func blockingRegrabReason(existing *models.Download, now time.Time) string {
	switch {
	case existing == nil:
		return ""
	case existing.Status == models.StateImportBlocked:
		// Blocked is re-grabbable by hand and deliberately not by the sweep
		// (models.Download.BlocksAutoRegrab). Its own reason, because "already
		// grabbed" would send the reader looking for a download in flight
		// rather than at the queue's Retry import.
		return regrabReasonImportBlocked
	case existing.BlocksAutoRegrab():
		return regrabReasonLiveWork
	case existing.Status.IsDeadForAutoRegrab() && existing.DeadSince().After(now.Add(-deadRegrabCooldown)):
		return regrabReasonTooRecent
	default:
		return ""
	}
}

// claimDeadRowForAutoGrab is the claim the auto grab makes on an existing row.
//
// It is a var so a test can drive the path where the claim is lost, which is
// otherwise a race: in production the only way to reach it is for a manual
// grab to claim the row between the scheduler reading it and claiming it
// itself. Nothing but a test ever replaces it.
var claimDeadRowForAutoGrab = func(ctx context.Context, downloads *db.DownloadRepo, dl *models.Download, deadBefore time.Time) (bool, error) {
	return downloads.RetryDeadForAutoGrab(ctx, dl, deadBefore)
}
