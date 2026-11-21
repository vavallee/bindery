// Package releasefail decides what a failed torrent grab says about the
// release (#4111).
//
// A failed download row is re-sent after models.DeadRegrabCooldown, and the
// only automatic blocklist writers used to be the stall path and the usenet
// content failures (#3024). A torrent that failed at grab time or never
// reached the client was retried every cooldown for ever. This package
// separates the failures that are a property of the release from the ones that
// are a property of the indexer or of Bindery's side of the wire, counts the
// first kind per release, and blocklists a release once it has failed
// EscalateAfter times in a row.
//
// There is no counter column. A failure is written to history as a
// downloadFailed event carrying the release GUID and a class, and the count is
// the run of those events since the release last imported, inside Window, at
// least MinSpacing apart (see HistoryRepo.ReleaseFailureStreak).
// Deleting history only resets a counter, which errs towards retrying.
package releasefail

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Class says whose fault a failure looks like.
type Class string

const (
	// ClassLocal is a fault on Bindery's side of the wire or one the message
	// cannot attribute: the client is unreachable, a timeout, a rejected
	// credential. It says nothing about the release and is never counted.
	ClassLocal Class = "local"
	// ClassIndexer is the indexer refusing or failing the fetch of the
	// release's link: HTTP 429, a 5xx, or a "disabled till" ban. It is
	// transient for one attempt, so it only blocklists after repeating, and it
	// makes Bindery leave that indexer alone for a while.
	ClassIndexer Class = "indexer"
	// ClassRelease is the link being gone: the indexer answered 404 or 410.
	ClassRelease Class = "release"
	// ClassNeverArrived is a grab reported as sent that the client has no
	// record of, repeatedly.
	ClassNeverArrived Class = "neverArrived"
	// ClassClientError is the client reporting the torrent as errored. For
	// qBittorrent that state is usually a disk fault (an unwritable save path
	// or a full disk), which is the host and not the release, and a full disk
	// would blocklist every release in turn. It is recorded in history and
	// never counted.
	ClassClientError Class = "clientError"
	// ClassIndexerWide is an indexer failure that was not counted because it
	// is not evidence about this release: the indexer was already backing off,
	// or other releases from it failed the same way.
	ClassIndexerWide Class = "indexerWide"
)

// Counts reports whether failures of this class count towards a blocklist.
func (c Class) Counts() bool {
	switch c {
	case ClassIndexer, ClassRelease, ClassNeverArrived:
		return true
	}
	return false
}

// CountedClasses is every class that counts, for the history query.
var CountedClasses = []string{
	string(ClassIndexer), string(ClassRelease), string(ClassNeverArrived),
}

const (
	// EscalateAfter is how many consecutive counted failures of one release
	// blocklist it. One failure is a blip, two can be a bad hour, three
	// spaced a cooldown apart is the release.
	EscalateAfter = 3
	// Window bounds how far back failures count, so a release that failed
	// twice last month is not one failure from a ban.
	Window = 7 * 24 * time.Hour
	// DefaultIndexerBackoff is how long an indexer is left alone after a
	// transient failure that carried no time of its own.
	DefaultIndexerBackoff = time.Hour
	// MinSpacing is the least time between two counted failures of one
	// release. A burst of manual searches or sweeps cannot reach the threshold.
	MinSpacing = time.Hour
	// IndexerWideWindow is how far back another release's failure from the
	// same indexer counts as evidence that the indexer, not this release, is
	// at fault.
	IndexerWideWindow = 24 * time.Hour
	// MaxIndexerBackoff caps a time the indexer asked for.
	MaxIndexerBackoff = 24 * time.Hour
	// MinIndexerBackoff floors a time the indexer asked for.
	MinIndexerBackoff = 5 * time.Minute
)

// SendFailure is what a failed SendDownload says.
type SendFailure struct {
	Class Class
	// Detail is a short phrase naming the class and the HTTP status. It is
	// built here, never copied from the error, which can carry a URL with an
	// API key or the client's address.
	Detail string
	// RetryAt is when the indexer said it would take requests again, zero if
	// it did not say.
	RetryAt time.Time
}

var (
	indexerStatusRe = regexp.MustCompile(`indexer returned HTTP (\d{3})`)
	disabledTillRe  = regexp.MustCompile(`(?i)disabled till (\d{1,2}/\d{1,2}/\d{4} \d{1,2}:\d{2}:\d{2})`)
	retryAfterRe    = regexp.MustCompile(`(?i)retry-after[:= ]+\s*(\d{1,7})\b`)
)

// ClassifySend reads the error text of a failed SendDownload. Only the
// indexer fetch the torrent adapters make ("indexer returned HTTP N") is
// attributed away from Bindery: an HTTP error from the download client itself
// has the same digits and means something else.
func ClassifySend(msg string, now time.Time) SendFailure {
	status := 0
	if m := indexerStatusRe.FindStringSubmatch(msg); m != nil {
		status, _ = strconv.Atoi(m[1])
	}
	disabled := disabledTillRe.FindStringSubmatch(msg)
	switch {
	case status == 429 || status >= 500 || (status != 0 && disabled != nil):
		// An indexer failure, below.
	case status == 404 || status == 410:
		return SendFailure{
			Class:  ClassRelease,
			Detail: fmt.Sprintf("the indexer no longer serves the release (HTTP %d)", status),
		}
	case status == 0 && disabled != nil && strings.Contains(strings.ToLower(msg), "fetch torrent"):
		// The ban notice without a status code.
	default:
		return SendFailure{Class: ClassLocal, Detail: "the download client could not take the release"}
	}
	f := SendFailure{Class: ClassIndexer, Detail: "indexer disabled after recent failures"}
	if status != 0 {
		f.Detail = fmt.Sprintf("indexer HTTP %d", status)
	}
	f.RetryAt = parseRetryAt(msg, disabled, now)
	return f
}

// parseRetryAt reads a time out of an indexer failure, defensively: anything
// unparsable or in the past yields the zero time and the caller's default.
// "disabled till" is read as UTC because the indexer does not name a zone; if
// the real zone is ahead the time is early, and MinIndexerBackoff keeps
// Bindery from retrying right away.
func parseRetryAt(msg string, disabled []string, now time.Time) time.Time {
	if disabled != nil {
		if t, err := time.Parse("1/2/2006 15:04:05", disabled[1]); err == nil && t.After(now) {
			return t
		}
	}
	if m := retryAfterRe.FindStringSubmatch(msg); m != nil {
		if secs, err := strconv.Atoi(m[1]); err == nil && secs > 0 {
			return now.Add(time.Duration(secs) * time.Second)
		}
	}
	return time.Time{}
}

// IndexerBackoffUntil is when Bindery may use the indexer again after f, or
// the zero time when f is not an indexer failure. A time the indexer gave is
// honoured between MinIndexerBackoff and MaxIndexerBackoff.
func IndexerBackoffUntil(f SendFailure, now time.Time) time.Time {
	if f.Class != ClassIndexer {
		return time.Time{}
	}
	until := now.Add(DefaultIndexerBackoff)
	if !f.RetryAt.IsZero() {
		until = f.RetryAt
		if floor := now.Add(MinIndexerBackoff); until.Before(floor) {
			until = floor
		}
		if ceil := now.Add(MaxIndexerBackoff); until.After(ceil) {
			until = ceil
		}
	}
	return until
}

// Verdict is the escalation decision for one failure.
type Verdict struct {
	// Count is how many counted failures the release has, this one included
	// when it counts.
	Count int
	// Block is true when the release should now be blocklisted.
	Block bool
	// Class is the class to store on the history event: the one asked for,
	// or ClassIndexerWide when the failure is not evidence about the release.
	Class Class
}

// Assess decides what the failure about to be recorded for dl means. Call it
// before writing the history event, store Verdict.Class on it, and say on it
// whether the release was blocklisted.
//
// A failure counts only when it is evidence about the release:
//   - an indexer failure is not counted while the indexer is already backing
//     off (indexerBackedOff), or when another release from it failed the same
//     way in the last IndexerWideWindow, because that is an indexer wide fault;
//   - a never-arrived failure is not counted when the indexer failed releases
//     in that window, for the same reason;
//   - a repeated indexer failure is counted only if the indexer sent another
//     release to a client since this release last failed, so a long outage
//     cannot add up across retries;
//   - one inside MinSpacing of the last counted failure is recorded but adds
//     nothing (see HistoryRepo.ReleaseFailureStreak).
//
// A history error counts as no prior failures: it fails towards a retry.
func Assess(ctx context.Context, history *db.HistoryRepo, dl *models.Download, class Class, now time.Time, indexerBackedOff bool) Verdict {
	v := Verdict{Class: class}
	if !class.Counts() || history == nil || strings.TrimSpace(dl.GUID) == "" {
		return v
	}
	if class == ClassIndexer && indexerBackedOff {
		v.Class = ClassIndexerWide
		return v
	}
	if (class == ClassIndexer || class == ClassNeverArrived) && dl.IndexerID != nil {
		n, err := history.IndexerFailuresElsewhere(ctx, *dl.IndexerID, otherGUID(class, dl.GUID), string(ClassIndexer), now.Add(-IndexerWideWindow))
		if err != nil {
			slog.Warn("could not look for indexer wide failures", "guid", dl.GUID, "error", err)
		}
		if n > 0 {
			v.Class = ClassIndexerWide
			return v
		}
	}
	prior, last, pending, err := history.ReleaseFailureStreak(ctx, dl.GUID, dl.Title, dl.BookID, now.Add(-Window),
		CountedClasses, string(ClassNeverArrived), MinSpacing)
	if err != nil {
		slog.Warn("could not count prior failures of a release", "guid", dl.GUID, "error", err)
	}
	if pending && class != ClassNeverArrived {
		prior, last = 0, time.Time{}
	}
	v.Count = prior
	if last.IsZero() || now.Sub(last) >= MinSpacing {
		v.Count = prior + 1
	}
	// A repeated indexer failure counts only if the indexer served another
	// release since this one last failed: otherwise every retry of a release
	// through an outage would add up, and an outage is not the release.
	if class == ClassIndexer && prior > 0 && !last.IsZero() && dl.IndexerID != nil {
		served, err := history.IndexerServedElsewhere(ctx, *dl.IndexerID, dl.GUID, last)
		if err != nil {
			slog.Warn("could not check that the indexer is serving", "guid", dl.GUID, "error", err)
		}
		if !served {
			v.Class = ClassIndexerWide
			v.Count = prior
			return v
		}
	}
	v.Block = v.Count >= EscalateAfter
	return v
}

// otherGUID is the guid to exclude when looking for the indexer's other
// failures: an indexer failure of this release is not "elsewhere", a
// never-arrived one has no indexer failure of its own to exclude.
func otherGUID(class Class, guid string) string {
	if class == ClassIndexer {
		return guid
	}
	return ""
}

// Reason is the blocklist reason for a verdict. It names the class and the
// count, and holds nothing taken from an error.
func Reason(detail string, count int) string {
	return fmt.Sprintf("downloadFailed: %s, failed %d times in a row", detail, count)
}

// blocklistMu makes the check and the insert in Blocklist one step. Both
// writers (the scheduler and the importer's poller) live in this process, so a
// mutex is enough to keep two failures of one release from adding two rows.
var blocklistMu sync.Mutex

// Blocklist adds dl's release to the blocklist for every book, unless a
// release scoped entry already blocks it. Blocklist matching is global by
// design (migration 049), as for the usenet content failures (#3024), so an
// entry written for one owner's download blocks the release for every owner.
func Blocklist(ctx context.Context, blocklist *db.BlocklistRepo, dl *models.Download, reason string) {
	if blocklist == nil || strings.TrimSpace(dl.GUID) == "" {
		return
	}
	blocklistMu.Lock()
	defer blocklistMu.Unlock()
	if blocked, err := blocklist.BlocksBook(ctx, dl.GUID, nil); err == nil && blocked {
		return
	}
	entry := &models.BlocklistEntry{
		BookID:    dl.BookID,
		GUID:      dl.GUID,
		Title:     dl.Title,
		IndexerID: dl.IndexerID,
		Reason:    reason,
		Scope:     models.BlocklistScopeRelease,
	}
	if err := blocklist.Create(ctx, entry); err != nil {
		slog.Warn("could not blocklist a repeatedly failing release; it may be grabbed again",
			"guid", dl.GUID, "title", dl.Title, "error", err)
		return
	}
	slog.Warn("blocklisted a release that keeps failing", "guid", dl.GUID, "title", dl.Title, "reason", reason)
}
