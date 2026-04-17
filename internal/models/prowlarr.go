package models

import "time"

// ProwlarrInstance holds the connection config for a Prowlarr server.
type ProwlarrInstance struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	APIKey        string     `json:"apiKey"`
	SyncOnStartup bool       `json:"syncOnStartup"`
	Enabled       bool       `json:"enabled"`
	LastSyncAt    *time.Time `json:"lastSyncAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}
