// Package models defines the domain types shared across the API, database,
// scheduler, and indexer layers.
package models

import "time"

type Author struct {
	ID                 int64   `json:"id"`
	ForeignID          string  `json:"foreignAuthorId"`
	Name               string  `json:"authorName"`
	SortName           string  `json:"sortName"`
	Description        string  `json:"description"`
	ImageURL           string  `json:"imageUrl"`
	Disambiguation     string  `json:"disambiguation"`
	RatingsCount       int     `json:"ratingsCount"`
	AverageRating      float64 `json:"averageRating"`
	Monitored          bool    `json:"monitored"`
	MonitorMode        string  `json:"monitorMode"`
	MonitorLatestCount int     `json:"monitorLatestCount"`
	QualityProfileID   *int64  `json:"qualityProfileId"`
	MetadataProfileID  *int64  `json:"metadataProfileId"`
	RootFolderID       *int64  `json:"rootFolderId"`
	// AudiobookRootFolderID overrides the audiobook destination for this
	// author. Distinct from RootFolderID, which only routes ebooks: keeping
	// them separate ensures an ebook root folder never redirects audiobooks
	// (#421). Nil falls back to the global audiobook library dir.
	AudiobookRootFolderID *int64     `json:"audiobookRootFolderId"`
	MetadataProvider      string     `json:"metadataProvider"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`

	// Joined data
	Books      []Book        `json:"books,omitempty"`
	Statistics *AuthorStats  `json:"statistics,omitempty"`
	Aliases    []AuthorAlias `json:"aliases,omitempty"`
	// MonitoredSeriesIDs is the user-selected subset of series the author is
	// pinned to when MonitorMode == AuthorMonitorModeSeries (#810). Populated
	// by the author Get handler so the edit modal can preselect chips.
	MonitoredSeriesIDs []int64 `json:"monitoredSeriesIds,omitempty"`

	// Transient: populated from the metadata provider during add/refresh; not stored in DB.
	// Used to seed author_aliases so non-latin primary names get latin-script alternates.
	AlternateNames []string `json:"-"`
}

const (
	AuthorMonitorModeAll    = "all"
	AuthorMonitorModeFuture = "future"
	AuthorMonitorModeLatest = "latest"
	AuthorMonitorModeNone   = "none"
	// AuthorMonitorModeSeries restricts monitoring to books belonging to a
	// user-selected subset of the author's series (#810). The selection lives
	// in the author_monitored_series join table rather than overloading
	// series.monitored, which is a separate global-watchlist flag.
	AuthorMonitorModeSeries = "series"

	DefaultAuthorMonitorMode        = AuthorMonitorModeAll
	DefaultAuthorMonitorLatestCount = 1
)

func IsAuthorMonitorModeValid(mode string) bool {
	switch mode {
	case AuthorMonitorModeAll, AuthorMonitorModeFuture, AuthorMonitorModeLatest, AuthorMonitorModeNone, AuthorMonitorModeSeries:
		return true
	default:
		return false
	}
}

// IsAuthorMonitorModeValidAsGlobalDefault returns true when mode is acceptable
// as the install-wide default. Series mode is excluded because a per-author
// series selection has no sensible global value.
func IsAuthorMonitorModeValidAsGlobalDefault(mode string) bool {
	return IsAuthorMonitorModeValid(mode) && mode != AuthorMonitorModeSeries
}

type AuthorStats struct {
	BookCount      int `json:"bookCount"`
	AvailableBooks int `json:"availableBookCount"`
	WantedBooks    int `json:"wantedBookCount"`
}

// AuthorAlias is an alternate name that resolves to a canonical Author.
// Populated by the merge flow so duplicates like "RR Haywood" and
// "R.R. Haywood" collapse into a single row without losing the original
// OpenLibrary id.
type AuthorAlias struct {
	ID         int64     `json:"id"`
	AuthorID   int64     `json:"authorId"`
	Name       string    `json:"name"`
	SourceOLID string    `json:"sourceOlId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}
