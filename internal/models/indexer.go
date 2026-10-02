package models

import "time"

type Indexer struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
	// APIKeyConfigured is a response-only field: the API blanks APIKey on the
	// way out and sets this instead, so the client can render "a key is set"
	// without ever receiving the credential. It is never persisted (the
	// indexers repo enumerates its columns) and anything a client sends in it
	// is ignored.
	APIKeyConfigured        bool   `json:"apiKeyConfigured"`
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
	// SeedTimeMinutes is the per-indexer total seeding time limit (#2206): stop
	// seeding a torrent grabbed from this indexer after this many minutes. nil
	// means "no override" so the download client keeps its own rule. Honoured
	// by qBittorrent only; Transmission, Deluge and rTorrent have no per
	// torrent total seed time and the adapter logs that it was not applied.
	SeedTimeMinutes *int `json:"seedTimeMinutes,omitempty"`
	// SeedTimeSource is the provenance of SeedTimeMinutes, with the same values
	// and rules as SeedRatioSource: Prowlarr's per-indexer seedTime fills it
	// unless the user owns it. It is tracked separately from the ratio so a
	// user owned ratio does not stop Prowlarr from filling the seed time.
	SeedTimeSource string `json:"seedTimeSource,omitempty"`
	// InactiveSeedTimeMinutes is the per-indexer inactive seeding limit
	// (#2206): stop seeding after this many minutes without upload activity.
	// nil means "no override". Honoured by qBittorrent (4.6 and later) and
	// Transmission (seedIdleLimit). Prowlarr has no equivalent setting, so it
	// is only ever set by the user and carries no provenance.
	InactiveSeedTimeMinutes *int `json:"inactiveSeedTimeMinutes,omitempty"`
	// DailyQueryLimit caps how many requests Bindery will send this indexer in
	// the last 24 hours (#2312). nil, and any value <= 0, means no cap.
	//
	// Requests are counted in hourly blocks and the window boundary rounds down
	// to the hour, so the block straddling the 24-hour mark is counted in full
	// and the effective window is up to 25 hours. The limit therefore binds
	// slightly early rather than slightly late, which is the right direction
	// for a cap whose job is not to overspend an allowance Bindery does not
	// own.
	//
	// The unit is outbound HTTP requests, not books: one book on one indexer
	// costs a single request when the structured tier-1 query matches and up to
	// eight when it falls all the way through the text tiers and the
	// transliteration retry. Counting books would understate the cost by up to
	// 8x, which is the opposite of useful when the whole point is not to exceed
	// a tracker's allowance.
	//
	// Requests served from the per-client query cache never leave the process
	// and are not counted. Neither is the Settings "Test" button, which builds
	// its own client outside the searcher — it costs two requests and stays
	// available precisely when an indexer has capped out and someone is trying
	// to work out why.
	DailyQueryLimit *int `json:"dailyQueryLimit,omitempty"`
	// DailyQueriesUsed is a response-only field, like APIKeyConfigured: the API
	// fills it from the query-count table so the Indexers tab can show how much
	// of the cap is spent. It is never persisted and anything a client sends in
	// it is ignored. nil on an indexer with no cap set.
	DailyQueriesUsed *int `json:"dailyQueriesUsed,omitempty"`
	// Search health, written by the searcher rather than by the user (#1935).
	// All four are nil on an indexer that has never been searched.
	//
	// LastError is the message from the most recent failed search, cleared on
	// the next success, so a non-nil value means "the last thing we heard from
	// this indexer was a refusal". LastErrorCode is the Newznab code when the
	// indexer itself rejected us (1xx needs a human, 5xx clears on its own) and
	// nil for a transport-level failure.
	// CooldownUntil and CooldownReason are response-only: the API fills them
	// from the searcher when it is holding off on this indexer after a rate
	// limit (#1934, #2635), so the Indexers tab can say when searches resume.
	// Never persisted; anything a client sends in them is ignored.
	CooldownUntil  *time.Time `json:"cooldownUntil,omitempty"`
	CooldownReason *string    `json:"cooldownReason,omitempty"`
	LastError      *string    `json:"lastError,omitempty"`
	LastErrorCode  *int       `json:"lastErrorCode,omitempty"`
	LastFailureAt  *time.Time `json:"lastFailureAt,omitempty"`
	LastSuccessAt  *time.Time `json:"lastSuccessAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
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

// Provenance values for Indexer.SeedRatioSource, and for Indexer.SeedTimeSource
// with the same meaning applied to the seed time.
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
