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
	ImportRetryCount int           `json:"importRetryCount"`
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
// scheduler adds a cooldown on top of it so a release that keeps failing is
// not re-grabbed on every sweep.
func (d *Download) BlocksRegrab() bool {
	if d == nil {
		return false
	}
	return !d.Status.IsDeadForRegrab() && !d.IsOrphanedImport()
}

// LastActivityAt is the most recent moment this download is known to have
// moved: its completion, else its grab, else the moment the row was written.
// downloads has no updated_at column, so this is the best available answer to
// "how long ago did this attempt end", and it is what the scheduler's re-grab
// cooldown measures. db.DownloadRepo.RetryDeadForAutoGrab computes the same
// value in SQL as COALESCE(completed_at, grabbed_at, added_at); keep the two
// in agreement.
func (d *Download) LastActivityAt() time.Time {
	switch {
	case d == nil:
		return time.Time{}
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
