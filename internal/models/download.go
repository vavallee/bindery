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
	RemoveOnImport    bool                  `json:"removeOnImport"`
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
// this, and db.DownloadRepo.RetryFailed and RetryOrphanedImport repeat it in
// SQL. Keep them in agreement.
func (d *Download) IsOrphanedImport() bool {
	return d != nil && d.Status == StateImported && d.BookID == nil
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
