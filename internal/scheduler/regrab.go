package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/models"
)

// deadRegrabCooldown is how long a failed download row keeps blocking the
// scheduler's automatic re-grab of the same release. The value and its
// reasoning live on models.DeadRegrabCooldown, which a manual grab of another
// user's failed row is held to as well.
const deadRegrabCooldown = models.DeadRegrabCooldown

// regrabReasonImportBlocked and the other reasons below name a skip in the
// search outcome and in the log line that accompanies it. They are short
// because they are read next to the blocking row's status.
const (
	regrabReasonLiveWork      = "already grabbed"
	regrabReasonTooRecent     = "failed too recently"
	regrabReasonImportBlocked = "import blocked, not retried automatically"
)

// errDropPending is what the automatic search's accept check returns, with
// skip set, for a release another book has imported (#3564). On a pending row
// it means the row can be removed: that release cannot be grabbed for this
// book while the import stands, and the row would otherwise stay in Pending
// for good. It never stops a search.
var errDropPending = errors.New("release imported for another book")

// heldForAnotherBook reports why the existing download row for a release
// keeps it from this book, or "" when there is no row, the row is this
// book's own, or the row may be reused (blockingRegrabReason).
//
// A row of another book cannot be shared: downloads.guid is unique, so this
// book cannot have the release while that row is live. The search passes the
// release over and tries the next approved one (#3564); before, it stopped at
// the first, and a good second release was never tried. A row with no book (a
// free text grab) counts as another book's. This book's own row keeps the old
// rule and stops the search, because a second grab for a book whose download
// is in flight would be a duplicate.
func heldForAnotherBook(existing *models.Download, bookID int64, now time.Time) string {
	if existing == nil || (existing.BookID != nil && *existing.BookID == bookID) {
		return ""
	}
	return blockingRegrabReason(existing, now)
}

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

// stalledReleaseKey carries the GUID of the release the stall handler has just
// failed into the re-search it starts.
type stalledReleaseKey struct{}

// withStalledRelease marks ctx as the re-search for a release that has just
// stalled, so that search skips it and moves on to the next approved release.
func withStalledRelease(ctx context.Context, guid string) context.Context {
	if guid == "" {
		return ctx
	}
	return context.WithValue(ctx, stalledReleaseKey{}, guid)
}

// stalledReleaseFrom is the GUID withStalledRelease set, or "".
func stalledReleaseFrom(ctx context.Context) string {
	guid, _ := ctx.Value(stalledReleaseKey{}).(string)
	return guid
}

// stalledReleaseSpec rejects the release a stall re-search was started for
// (#2709).
//
// Without it the re-search does nothing whenever the stalled release still
// ranks first, which is the usual case since nothing about the ranking
// changed: the decision loop approves it again, and the dead row's cooldown
// above then refuses the grab outright ("failed too recently") instead of
// trying the runner up. A client reported stall never showed this because it
// blocklists, and the blocklist spec rejects the release before it can be
// chosen. A no-metadata stall deliberately does not blocklist, so this spec is
// what lets its re-search pick another release.
//
// It is scoped to that one search, not stored: the next ordinary sweep may
// pick the release again once the cooldown has passed, which is the point of
// not blocklisting a release for what may have been a network fault.
type stalledReleaseSpec struct {
	guid string
}

func (s stalledReleaseSpec) IsSatisfiedBy(r decision.Release, _ models.Book) (bool, string) {
	if r.GUID == s.guid {
		return false, "release just stalled, trying another"
	}
	return true, ""
}
