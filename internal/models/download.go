package models

import "time"

type DownloadClient struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	APIKey   string `json:"apiKey"`
	UseSSL   bool   `json:"useSsl"`
	URLBase  string `json:"urlBase"`
	Category string `json:"category"`
	// CategoryAudiobook is the category/label/tag used when sending an
	// audiobook download. When empty, audiobooks fall back to Category, which
	// preserves pre-#700 behaviour for clients that have not opted in to the
	// per-media-type split.
	CategoryAudiobook string                `json:"categoryAudiobook"`
	PathRemap         string                `json:"pathRemap"`
	Priority          int                   `json:"priority"`
	Enabled           bool                  `json:"enabled"`
	CreatedAt         time.Time             `json:"createdAt"`
	UpdatedAt         time.Time             `json:"updatedAt"`
	Health            *DownloadClientHealth `json:"health,omitempty"`

	// Username and Password are used by download clients that authenticate with
	// credentials rather than an API key (e.g. qBittorrent, Transmission).
	// These fields are persisted in dedicated download_clients table columns.
	//
	// APIKey and Password are write-only over the HTTP API: the handlers blank
	// them on every response and report presence through the two booleans
	// below instead (#2213).
	Username string `json:"username"`
	Password string `json:"password"`

	// APIKeyConfigured and PasswordConfigured are response-only. They are
	// never persisted and never read from a request body; the API layer sets
	// them so a caller can tell that a credential is stored without being
	// handed the credential itself.
	APIKeyConfigured   bool `json:"apiKeyConfigured"`
	PasswordConfigured bool `json:"passwordConfigured"`
}

type DownloadClientHealth struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Download struct {
	ID   int64  `json:"id"`
	GUID string `json:"guid"`
	// OwnerUserID scopes the download to the user whose grab created it
	// (#1457). 0 = unowned (persisted as NULL); the downloads list uses the
	// STRICT owner scope, so stamping is what makes a non-admin's queue
	// non-empty under tenancy. Background grabs inherit the book's owner.
	OwnerUserID      int64         `json:"-"`
	BookID           *int64        `json:"bookId"`
	EditionID        *int64        `json:"editionId"`
	IndexerID        *int64        `json:"indexerId"`
	DownloadClientID *int64        `json:"downloadClientId"`
	Title            string        `json:"title"`
	NZBURL           string        `json:"nzbUrl"`
	Size             int64         `json:"size"`
	SABnzbdNzoID     *string       `json:"sabnzbdNzoId"`
	TorrentID        *string       `json:"torrentId"`
	Status           DownloadState `json:"status"`
	Protocol         string        `json:"protocol"`
	Quality          string        `json:"quality"`
	IndexerFlags     string        `json:"indexerFlags"`
	ErrorMessage     string        `json:"errorMessage"`
	AddedAt          time.Time     `json:"addedAt"`
	GrabbedAt        *time.Time    `json:"grabbedAt"`
	CompletedAt      *time.Time    `json:"completedAt"`
	ImportedAt       *time.Time    `json:"importedAt"`
	// DeadAt is when this download entered a state it cannot leave on its own
	// (failed, importBlocked), stamped by the repo writers that put it there
	// and cleared again when a grab claims the row (#2710). NULL for a row
	// that is not dead, and for one that died before migration 091, which the
	// migration backfills from the row's other stamps.
	//
	// It exists because nothing else records the moment of death: added_at,
	// grabbed_at and completed_at all belong to the attempt's beginning, so a
	// cooldown measured from them counts from the wrong instant.
	DeadAt           *time.Time `json:"-"`
	ImportRetryCount int        `json:"importRetryCount"`
	// ImportPath is the on-disk location of the completed download's files,
	// recorded when an import fails because no catalogue book matched (#1589).
	// The queue "Match to book" action imports from here directly. Empty when
	// no path was recorded (never completed, or matched successfully).
	ImportPath string `json:"-"`
}

// IsOrphanedImport reports whether d finished importing into a book that has
// since been deleted: status imported with no book (#2289). downloads.book_id
// is ON DELETE SET NULL, so such a row outlives its book and, unless a grab
// may reuse it, pins the release's GUID for good.
//
// A NULL book_id alone is not enough: an in flight row can legitimately have
// no book, since a free text grab is matched by the importer later.
//
// The manual grab (api.regrabbable) and the scheduler's auto grab both gate on
// this, and db.DownloadRepo.RetryFailed and RetryDeadForAutoGrab repeat it in
// SQL. Keep them in agreement.
func (d *Download) IsOrphanedImport() bool {
	return d != nil && d.Status == StateImported && d.BookID == nil
}

// BlocksRegrab reports whether d, the existing row holding a release's GUID,
// must stop a fresh grab of that release.
//
// Only live work blocks: a row that is grabbed, downloading, completed,
// importing or imported into a book that still exists. Re-grabbing any of
// those would duplicate a download that is already running or throw away an
// import that already worked.
//
// A dead row does not block (#2710). It is a finished attempt with no
// automatic path out, and the reason it died is usually gone by the time the
// release is chosen again: the reporter had twenty five rows failed on a
// loopback URL refusal and on indexer 429 and 500 responses, all long since
// fixed, and every automatic re-grab of those releases was dropped on the
// GUID. An orphaned import does not block either, for the separate reason
// IsOrphanedImport gives (#2289).
//
// This is the whole predicate for the manual grab (api.regrabbable). The
// scheduler's automatic grab is narrower: see BlocksAutoRegrab.
func (d *Download) BlocksRegrab() bool {
	if d == nil {
		return false
	}
	return !d.Status.IsDeadForRegrab() && !d.IsOrphanedImport()
}

// BlocksAutoRegrab is BlocksRegrab for the scheduler's automatic grab. It
// blocks everything BlocksRegrab blocks, and importBlocked as well.
//
// An importBlocked row's files are still on disk and its release downloaded
// fine; what failed was placing it in the library. Re-downloading is a
// reasonable thing for a person to choose there, which is why the manual grab
// allows it and the queue's Retry import sits next to it, but it is the wrong
// default for an unattended sweep: it fetches bytes that are already on disk,
// leaves the previous torrent in the client untracked (the claim clears
// torrent_id and import_path, and nothing removes the old source), and, since
// nothing blocklists the release, does it again every cycle with a grab
// notification each time. The state has a dedicated manual affordance, and the
// scheduler cannot tell whether the files are still there, so it leaves this
// one alone.
//
// The scheduler also applies a cooldown on top of this, so a release that
// keeps failing is not re-grabbed on every sweep.
func (d *Download) BlocksAutoRegrab() bool {
	if d == nil {
		return false
	}
	return !d.Status.IsDeadForAutoRegrab() && !d.IsOrphanedImport()
}

// DeadSince is when this download's last attempt ended, for a row that is
// dead. That is DeadAt, the stamp the repo writes at the moment of death
// (#2710).
//
// The fallback matters only for a row that died before migration 091 and was
// somehow not backfilled: completed_at, else grabbed_at, else added_at. Those
// are all stamps from the attempt's BEGINNING, so the fallback can only report
// a death as older than it was, never newer. It is the same approximation the
// backfill uses, kept here so a missing stamp cannot make a dead row
// permanently ineligible.
//
// db.DownloadRepo.RetryDeadForAutoGrab computes the same value in SQL as
// COALESCE(dead_at, completed_at, grabbed_at, added_at); keep the two in
// agreement.
func (d *Download) DeadSince() time.Time {
	switch {
	case d == nil:
		return time.Time{}
	case d.DeadAt != nil:
		return *d.DeadAt
	case d.CompletedAt != nil:
		return *d.CompletedAt
	case d.GrabbedAt != nil:
		return *d.GrabbedAt
	default:
		return d.AddedAt
	}
}

// Legacy status aliases — callers should prefer the typed State* constants in
// download_state.go. These are kept for any scanner comparisons that still use
// the old names; they will be removed in a future cleanup.
const (
	DownloadStatusDownloading = StateDownloading
	DownloadStatusCompleted   = StateCompleted
	DownloadStatusFailed      = StateFailed
	DownloadStatusImported    = StateImported
)
