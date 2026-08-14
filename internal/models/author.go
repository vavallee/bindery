// Package models defines the domain types shared across the API, database,
// scheduler, and indexer layers.
package models

import (
	"strings"
	"time"
)

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
	// MonitorNewItems governs works discovered AFTER the initial catalogue
	// sync — the refresh paths, not the add flow (issue #1348). "all"
	// (default) follows MonitorMode as before; "none" creates
	// newly-discovered works unmonitored, so a metadata refresh can never
	// mass-monitor a back-catalogue and trigger a search storm. Importers
	// create authors with "none": import-created authors start with a
	// partial catalogue, which made the first refresh after an import the
	// classic detonation point.
	MonitorNewItems   string `json:"monitorNewItems"`
	QualityProfileID  *int64 `json:"qualityProfileId"`
	MetadataProfileID *int64 `json:"metadataProfileId"`
	RootFolderID      *int64 `json:"rootFolderId"`
	// AudiobookRootFolderID overrides the audiobook destination for this
	// author. Distinct from RootFolderID, which only routes ebooks: keeping
	// them separate ensures an ebook root folder never redirects audiobooks
	// (#421). Nil falls back to the global audiobook library dir.
	AudiobookRootFolderID *int64     `json:"audiobookRootFolderId"`
	MetadataProvider      string     `json:"metadataProvider"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`

	// OwnerUserID is the per-user ownership column added in migration 025.
	// Zero means "no recorded owner" (legacy pre-backfill rows or rows
	// imported without a user context); CheckOwnership treats that as
	// visible to every authenticated caller. The field is not exposed in
	// JSON: it is an internal scoping hint, not API contract.
	OwnerUserID int64 `json:"-"`

	// Joined data
	Books      []Book        `json:"books,omitempty"`
	Statistics *AuthorStats  `json:"statistics,omitempty"`
	Aliases    []AuthorAlias `json:"aliases,omitempty"`
	// MonitoredSeriesIDs is the user-selected subset of series the author is
	// pinned to when MonitorMode == AuthorMonitorModeSeries (#810). Populated
	// by the author Get handler so the edit modal can preselect chips.
	MonitoredSeriesIDs []int64 `json:"monitoredSeriesIds,omitempty"`
	// LastSync reports what the most recent catalogue sync did with the works
	// the provider returned (#1889). Populated by the author Get handler from
	// an in-process record, so it is absent until this process has synced the
	// author at least once.
	LastSync *AuthorSyncSummary `json:"lastSync,omitempty"`

	// Transient: populated from the metadata provider during add/refresh; not stored in DB.
	// Used to seed author_aliases so non-latin primary names get latin-script alternates.
	AlternateNames []string `json:"-"`
}

// AuthorSyncSummary is the outcome of one catalogue sync: how many of the
// provider's works became books, and how many were dropped by which filter.
//
// It exists because the drops were invisible (#1889). The allowed-languages
// filter logged one Debug line per rejected work and the run's totals landed in
// a single Info line, so an author whose catalogue was mostly filtered away
// looked exactly like an author who only ever wrote a few books — one reporter
// lost 65 books from a single author and only found out by going looking in the
// logs, which a rootless container does not even hand them.
type AuthorSyncSummary struct {
	CompletedAt time.Time `json:"completedAt"`
	// Total is the number of works the providers returned, before filtering.
	Total            int `json:"total"`
	Added            int `json:"added"`
	SkippedLanguage  int `json:"skippedLanguage"`
	SkippedJunk      int `json:"skippedJunk"`
	SkippedMediaType int `json:"skippedMediaType"`
	// SkippedNotAccepted is the number of works a refresh found but did not
	// add because the author isn't taking newly-discovered books (unmonitored,
	// or Monitor new items = None). Reported for the same reason as the rest:
	// "the refresh added nothing" needs to say why, or it reads as a failure
	// (#1815).
	SkippedNotAccepted int `json:"skippedNotAccepted,omitempty"`
	// AllowedLanguages is the language set the run actually applied, so the UI
	// can name the setting that did the dropping rather than making the user
	// guess which profile the author is on. Empty means "no language filter".
	AllowedLanguages []string `json:"allowedLanguages,omitempty"`
	// UnknownLanguageFail records the profile's unknown_language_behavior, the
	// half of the filter that surprises people: with it on, every work the
	// provider gave no language for is dropped too.
	UnknownLanguageFail bool `json:"unknownLanguageFail,omitempty"`
	// SkippedLanguageSample is the first few titles the language filter
	// dropped, capped so a prolific author's rejected tail can't bloat the
	// author payload. Enough to recognise whether the profile is set the way
	// the user meant.
	SkippedLanguageSample []AuthorSyncSkippedBook `json:"skippedLanguageSample,omitempty"`
}

// AuthorSyncSkippedBook is one dropped work, named so the user can tell a
// mis-set filter from a correctly-set one.
type AuthorSyncSkippedBook struct {
	Title string `json:"title"`
	// Language is the code the provider reported, empty when it reported none
	// (the unknown-language case).
	Language string `json:"language"`
}

// SkippedTotal is the number of provider works this sync dropped for any
// reason. Zero means nothing was filtered out.
//
// It matches what the author page's sync notice counts — the question there is
// "is there anything to explain?", and a refresh that declined to add 83 works
// has plenty. It deliberately does NOT match the log-level gate in
// fetchAuthorBooks, which excludes SkippedNotAccepted: a metadata filter
// silently discarding a catalogue is surprising and logs at Warn, while the
// discovery policy is something the user configured and already logged once.
// Two questions, two counts.
func (s *AuthorSyncSummary) SkippedTotal() int {
	if s == nil {
		return 0
	}
	return s.SkippedLanguage + s.SkippedJunk + s.SkippedMediaType + s.SkippedNotAccepted
}

// AuthorProviderFromForeignID returns the metadata provider implied by a
// Bindery author foreign ID. IDs without a known prefix are treated as
// OpenLibrary, matching the long-standing authors.foreign_id convention.
func AuthorProviderFromForeignID(foreignID string) string {
	foreignID = strings.TrimSpace(strings.ToLower(foreignID))
	switch {
	case strings.HasPrefix(foreignID, "gb:"):
		return "googlebooks"
	case strings.HasPrefix(foreignID, "hc:"):
		return "hardcover"
	case strings.HasPrefix(foreignID, "dnb:"):
		return "dnb"
	case strings.HasPrefix(foreignID, "calibre:"):
		return "calibre"
	case strings.HasPrefix(foreignID, "abs:"):
		return "audiobookshelf"
	default:
		return "openlibrary"
	}
}

// CanReplaceAuthorIdentity reports whether automated metadata enrichment may
// promote a different upstream foreign ID into authors.foreign_id.
func CanReplaceAuthorIdentity(author *Author) bool {
	if author == nil {
		return false
	}
	provider := strings.TrimSpace(strings.ToLower(author.MetadataProvider))
	foreignID := strings.TrimSpace(strings.ToLower(author.ForeignID))
	return foreignID == "" ||
		strings.HasPrefix(foreignID, "abs:") ||
		strings.HasPrefix(foreignID, "calibre:") ||
		provider == "audiobookshelf" ||
		provider == "calibre"
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

	// MonitorNewItems values (issue #1348): "all" defers to MonitorMode for
	// refresh-discovered works (previous behaviour), "none" leaves them
	// unmonitored.
	AuthorMonitorNewItemsAll  = "all"
	AuthorMonitorNewItemsNone = "none"

	DefaultAuthorMonitorNewItems = AuthorMonitorNewItemsAll
)

func IsAuthorMonitorModeValid(mode string) bool {
	switch mode {
	case AuthorMonitorModeAll, AuthorMonitorModeFuture, AuthorMonitorModeLatest, AuthorMonitorModeNone, AuthorMonitorModeSeries:
		return true
	default:
		return false
	}
}

func IsAuthorMonitorNewItemsValid(v string) bool {
	return v == AuthorMonitorNewItemsAll || v == AuthorMonitorNewItemsNone
}

// NormalizeAuthorMonitorNewItems maps empty/unknown values to the default so
// rows written by pre-#1348 code paths (or hand-edited databases) behave like
// the historical "follow MonitorMode" semantics.
func NormalizeAuthorMonitorNewItems(v string) string {
	if IsAuthorMonitorNewItemsValid(v) {
		return v
	}
	return DefaultAuthorMonitorNewItems
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

// AuthorIdentifier links any known provider/import author ID to the canonical
// local author row. authors.foreign_id remains the primary identity.
type AuthorIdentifier struct {
	AuthorID  int64     `json:"authorId"`
	Provider  string    `json:"provider"`
	ForeignID string    `json:"foreignAuthorId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
