package models

import "time"

type Indexer struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Type               string    `json:"type"`
	URL                string    `json:"url"`
	APIKey             string    `json:"apiKey"`
	Categories         []int     `json:"categories"`
	Priority           int       `json:"priority"`
	Enabled            bool      `json:"enabled"`
	SupportsSearch     bool      `json:"supportsSearch"`
	ProwlarrInstanceID *int64    `json:"prowlarrInstanceId,omitempty"`
	ProwlarrIndexerID  *int      `json:"prowlarrIndexerId,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
