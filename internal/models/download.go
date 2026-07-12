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
	Username string `json:"username"`
	Password string `json:"password"`
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
