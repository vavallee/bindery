package models

import "time"

type DownloadClient struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	APIKey    string    `json:"apiKey"`
	UseSSL    bool      `json:"useSsl"`
	URLBase   string    `json:"urlBase"`
	Category  string    `json:"category"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Username and Password are used by download clients that authenticate with
	// credentials rather than an API key (e.g. qBittorrent, Transmission).
	// These fields are persisted in dedicated download_clients table columns.
	Username string `json:"username"`
	Password string `json:"password"`
}

type Download struct {
	ID               int64      `json:"id"`
	GUID             string     `json:"guid"`
	BookID           *int64     `json:"bookId"`
	EditionID        *int64     `json:"editionId"`
	IndexerID        *int64     `json:"indexerId"`
	DownloadClientID *int64     `json:"downloadClientId"`
	Title            string     `json:"title"`
	NZBURL           string     `json:"nzbUrl"`
	Size             int64      `json:"size"`
	SABnzbdNzoID     *string    `json:"sabnzbdNzoId"`
	TorrentID        *string    `json:"torrentId"`
	Status           string     `json:"status"`
	Protocol         string     `json:"protocol"`
	Quality          string     `json:"quality"`
	IndexerFlags     string     `json:"indexerFlags"`
	ErrorMessage     string     `json:"errorMessage"`
	AddedAt          time.Time  `json:"addedAt"`
	GrabbedAt        *time.Time `json:"grabbedAt"`
	CompletedAt      *time.Time `json:"completedAt"`
	ImportedAt       *time.Time `json:"importedAt"`
}

const (
	DownloadStatusQueued      = "queued"
	DownloadStatusDownloading = "downloading"
	DownloadStatusPaused      = "paused"
	DownloadStatusCompleted   = "completed"
	DownloadStatusFailed      = "failed"
	DownloadStatusImported    = "imported"
	DownloadStatusDeleted     = "deleted"
)
