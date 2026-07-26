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
	SeedRatio *float64 `json:"seedRatio,omitempty"`
	// FreeleechOnly restricts this indexer's AUTOMATIC grabs to releases that
	// cost no download ratio (torznab downloadvolumefactor == 0). Non-freeleech
	// releases are not hidden: they are held in pending_releases for manual
	// approval, so bulk and scheduled searches stay ratio-safe while the user
	// can still deliberately pay the cost on a book they care about.
	// Interactive search is unaffected — it builds its own specification set.
	// Intended for private trackers; leave off for public trackers and usenet,
	// where there is no ratio economy.
	FreeleechOnly bool `json:"freeleechOnly"`
	// SeedRatioSource records who last wrote SeedRatio, so the Prowlarr syncer
	// (#1065) can auto-populate the ratio without clobbering an explicit user
	// choice. See the SeedRatioSource* constants. The empty string means "unset"
	// and is eligible for Prowlarr auto-population.
	SeedRatioSource string    `json:"seedRatioSource,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Provenance values for Indexer.SeedRatioSource.
const (
	// SeedRatioSourceUnset means no one has set a seed-ratio override; the
	// Prowlarr syncer may auto-populate it.
	SeedRatioSourceUnset = ""
	// SeedRatioSourceProwlarr means the override was auto-populated from
	// Prowlarr's per-indexer seedCriteria.seedRatio. A later Prowlarr change may
	// refresh it.
	SeedRatioSourceProwlarr = "prowlarr"
	// SeedRatioSourceUser means the user set, cleared, or toggled the override
	// via the UI. The Prowlarr syncer must never overwrite a user-owned value.
	SeedRatioSourceUser = "user"
)
