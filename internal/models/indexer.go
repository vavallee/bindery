package models

import "time"

type Indexer struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	Type                    string `json:"type"`
	URL                     string `json:"url"`
	APIKey                  string `json:"apiKey"`
	Categories              []int  `json:"categories"`
	IncludeParentCategories bool   `json:"includeParentCategories"`
	Priority                int    `json:"priority"`
	Enabled                 bool   `json:"enabled"`
	SupportsSearch          bool   `json:"supportsSearch"`
	ProwlarrInstanceID      *int64 `json:"prowlarrInstanceId,omitempty"`
	ProwlarrIndexerID       *int   `json:"prowlarrIndexerId,omitempty"`
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
	SeedRatioSource string `json:"seedRatioSource,omitempty"`
	// Search health, written by the searcher rather than by the user (#1935).
	// All four are nil on an indexer that has never been searched.
	//
	// LastError is the message from the most recent failed search, cleared on
	// the next success, so a non-nil value means "the last thing we heard from
	// this indexer was a refusal". LastErrorCode is the Newznab code when the
	// indexer itself rejected us (1xx needs a human, 5xx clears on its own) and
	// nil for a transport-level failure.
	LastError     *string    `json:"lastError,omitempty"`
	LastErrorCode *int       `json:"lastErrorCode,omitempty"`
	LastFailureAt *time.Time `json:"lastFailureAt,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// NeedsAttention reports whether the last search against this indexer failed
// with something only a human can fix: Newznab's 1xx range (100 bad
// credentials, 101 account suspended, 102 VPN forbidden). A 5xx rate limit and
// a transport error both clear on their own and are deliberately excluded.
func (i Indexer) NeedsAttention() bool {
	if i.LastError == nil || i.LastErrorCode == nil {
		return false
	}
	return *i.LastErrorCode >= 100 && *i.LastErrorCode <= 199
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
