package models

import "time"

type Indexer struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Type               string `json:"type"`
	URL                string `json:"url"`
	APIKey             string `json:"apiKey"`
	Categories         []int  `json:"categories"`
	Priority           int    `json:"priority"`
	Enabled            bool   `json:"enabled"`
	SupportsSearch     bool   `json:"supportsSearch"`
	ProwlarrInstanceID *int64 `json:"prowlarrInstanceId,omitempty"`
	ProwlarrIndexerID  *int   `json:"prowlarrIndexerId,omitempty"`
	// SeedRatio is the per-indexer seed-ratio override applied to torrents
	// grabbed from this indexer. nil means "no override" (the download client
	// keeps its global rule); an explicit -1 is the unlimited sentinel
	// (Prowlarr/qBittorrent convention). The downloader adapters translate the
	// value into each client's own API shape.
	SeedRatio *float64  `json:"seedRatio,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
