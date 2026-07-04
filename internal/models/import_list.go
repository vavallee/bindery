package models

import "time"

type ImportList struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	APIKey           string `json:"apiKey"`
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
	RootFolderID     *int64 `json:"rootFolderId"`
	QualityProfileID *int64 `json:"qualityProfileId"`
	MonitorNew       bool   `json:"monitorNew"`
	AutoAdd          bool   `json:"autoAdd"`
	Enabled          bool   `json:"enabled"`
	// MediaType pins the format that books synced from this list are created
	// as: "ebook", "audiobook", or "both". Empty means "unset" — keep the
	// media type derived from the source (e.g. Hardcover edition availability).
	MediaType  string     `json:"mediaType"`
	LastSyncAt *time.Time `json:"lastSyncAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type ImportListExclusion struct {
	ID         int64     `json:"id"`
	ForeignID  string    `json:"foreignId"`
	Title      string    `json:"title"`
	AuthorName string    `json:"authorName"`
	CreatedAt  time.Time `json:"createdAt"`
}
