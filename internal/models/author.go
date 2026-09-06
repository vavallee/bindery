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

	// ProviderIdentifiers carries the author_identifiers rows for this author,
	// keyed by provider, so a caller that has already loaded them can hand the
	// metadata layer the identity it needs without giving it database access.
	//
	// Transport-only, like Book.ProviderISBNs: there is no column, nothing
	// persists it, and a zero value simply means "the caller did not look
	// them up". Populated by the author sync so supplemental providers can
	// query by identity rather than by name (#1734).
	ProviderIdentifiers map[string]string `json:"-"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`

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

	// ProviderMismatch is stamped on add-flow responses when the record being
	// linked routes its catalogue syncs to a provider other than the
	// configured primary (#2237): with metadata.primary_provider = hardcover,
	// a Hardcover search miss makes the add flow pick a record from another
	// provider, and the author then syncs from that provider forever.
	// Transport-only; nothing persists it.
	ProviderMismatch *AuthorProviderMismatch `json:"providerMismatch,omitempty"`
}

// AuthorProviderMismatch reports that an author's catalogue will sync from a
// different provider than the one configured as primary (#2237). Both fields
// carry normalized provider names ("openlibrary", "hardcover", ...).
type AuthorProviderMismatch struct {
	PrimaryProvider string `json:"primaryProvider"`
	LinkedProvider  string `json:"linkedProvider"`
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
	Total int `json:"total"`
	Added int `json:"added"`
	// Matched is the number of works that resolved to a book already in the
	// library, by foreign id, by Hardcover id, or by normalised title. These
	// are the works a refresh UPDATES rather than creates: ratings, covers,
	// genres, re-parenting and dual-format merges all happen on this path.
	//
	// It exists because it was the missing term (#2449). Total counts every
	// work the providers returned, so on a well-established author almost all
	// of it lands here, and with no field naming it, Total minus Added minus
	// the Skipped* counts read as a hole big enough to look like data loss.
	// It is not a hole, it is the normal case with no name. Reported for every
	// sync, not just ones that skipped something.
	Matched int `json:"matched"`
	// Failed is the number of works that reached the create step and did not
	// become a book because the write failed. Unlike every Skipped* field this
	// is not a policy outcome, it is the one genuine silent loss the sync can
	// suffer, and before #2449 it existed only as a Warn line that a rootless
	// container never shows the user. Same argument as #1889.
	Failed           int `json:"failed,omitempty"`
	SkippedLanguage  int `json:"skippedLanguage"`
	SkippedJunk      int `json:"skippedJunk"`
	SkippedMediaType int `json:"skippedMediaType"`
	// SkippedNotAccepted is the number of works a refresh found but did not
	// add because the author isn't taking newly-discovered books (unmonitored,
	// or Monitor new items = None). Reported for the same reason as the rest:
	// "the refresh added nothing" needs to say why, or it reads as a failure
	// (#1815).
	SkippedNotAccepted int `json:"skippedNotAccepted,omitempty"`
	// SkippedExcluded is the number of newly discovered works dropped because
	// an excluded book under this author already carries the same title. The
	// notice deliberately does not render it: it explains works the user did
	// not expect to lose, and a hand excluded book is not one of those. It is
	// carried in the struct anyway so Total reconciles (#2449); leaving it out
	// of the JSON entirely is what made the arithmetic look broken.
	SkippedExcluded int `json:"skippedExcluded,omitempty"`
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

	// The five fields below back the metadata-profile filters wired into
	// author sync by PRs #1968, #2005, #2006, #2007, and #2008. Landed here
	// first (#1889-shaped review feedback, vavallee) so those PRs rebase onto
	// fields and UI that already exist rather than each adding — and each
	// needing rebased around the others adding — the same summary/notice
	// plumbing independently. Every count has a matching *Sample, same
	// reasoning as SkippedLanguageSample: a bare count doesn't say which
	// books vanished, and Debug logs aren't reachable in a rootless
	// container.

	// SkippedPartBooks is the number of works dropped as box sets, omnibuses,
	// signed-copy cartons, or slash-separated anthologies by the metadata
	// profile's SkipPartBooks setting.
	SkippedPartBooks       int                     `json:"skippedPartBooks,omitempty"`
	SkippedPartBooksSample []AuthorSyncSkippedBook `json:"skippedPartBooksSample,omitempty"`
	// SkippedMissingDate is the number of works dropped because they carried
	// no release date and the metadata profile's SkipMissingDate setting is
	// enabled.
	SkippedMissingDate       int                     `json:"skippedMissingDate,omitempty"`
	SkippedMissingDateSample []AuthorSyncSkippedBook `json:"skippedMissingDateSample,omitempty"`
	// SkippedMinPopularity is the number of works dropped because their
	// RatingsCount fell below the metadata profile's MinPopularity floor.
	// A work that hasn't released yet is exempt.
	SkippedMinPopularity       int                     `json:"skippedMinPopularity,omitempty"`
	SkippedMinPopularitySample []AuthorSyncSkippedBook `json:"skippedMinPopularitySample,omitempty"`
	// SkippedMinPages is the number of works dropped because every edition
	// with a known page count fell below the metadata profile's MinPages
	// floor. A work whose editions report no page count at all is treated as
	// unknown, not zero, and passes through unfiltered.
	SkippedMinPages       int                     `json:"skippedMinPages,omitempty"`
	SkippedMinPagesSample []AuthorSyncSkippedBook `json:"skippedMinPagesSample,omitempty"`
	// SkippedMissingISBN is the number of works dropped because none of
	// their editions carried an ISBN-13 or ISBN-10, with the metadata
	// profile's SkipMissingISBN setting enabled.
	SkippedMissingISBN       int                     `json:"skippedMissingIsbn,omitempty"`
	SkippedMissingISBNSample []AuthorSyncSkippedBook `json:"skippedMissingIsbnSample,omitempty"`
}

// AccountedFor is the number of works the summary can name an outcome for.
// Every provider work leaves the sync loop through exactly one of these, so a
// correct run has AccountedFor() == Total and the difference, if any, is the
// count of works that fell out of a path nobody added a counter to.
//
// Two tests hold this: api.TestAuthorSyncSummaryReconciles runs the real sync
// loop over a catalogue that trips every filter and asserts Unaccounted() is
// zero, and TestAuthorSyncSummaryAccountedForCoversEveryCounter in this package
// walks the struct by reflection so a counter added without being summed here
// fails at build time rather than on someone's author page. That is the actual
// point of the method: the counters drifted apart silently for long enough that
// a reader with the numbers in front of them concluded books were being lost
// (#2449).
func (s *AuthorSyncSummary) AccountedFor() int {
	if s == nil {
		return 0
	}
	// Built on SkippedTotal rather than re-listing the Skipped* fields, so a
	// new filter can only be added to one sum and forgotten from the other if
	// it is also forgotten from the notice, where it would be obvious.
	// SkippedExcluded is added back here because SkippedTotal deliberately
	// leaves it out.
	return s.Added + s.Matched + s.Failed + s.SkippedTotal() + s.SkippedExcluded
}

// Unaccounted is Total minus everything the summary can explain. Zero on a
// correct run. Never negative in practice, but returned signed so a test that
// double counts fails loudly rather than underflowing to a huge positive.
func (s *AuthorSyncSummary) Unaccounted() int {
	if s == nil {
		return 0
	}
	return s.Total - s.AccountedFor()
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
//
// web/src/components/AuthorSyncNotice.tsx computes its own "{{count}} of
// {{total}} works" heading sum independently in TypeScript rather than
// reading this value over the wire — the two have to be kept in step by
// hand. Adding a Skipped* field here means adding it to that sum too.
//
// SkippedExcluded is the one Skipped* field NOT counted here, for the same
// reason the notice does not render it: the user excluded that book on
// purpose. AccountedFor adds it back, because reconciling Total is a
// different question from deciding what to put on the page (#2449).
func (s *AuthorSyncSummary) SkippedTotal() int {
	if s == nil {
		return 0
	}
	return s.SkippedLanguage + s.SkippedJunk + s.SkippedMediaType + s.SkippedNotAccepted +
		s.SkippedPartBooks + s.SkippedMissingDate + s.SkippedMinPopularity + s.SkippedMinPages + s.SkippedMissingISBN
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

// ProviderIdentity returns this author's foreign id for the named provider, and
// whether one is known.
//
// The primary ForeignID wins when it already belongs to that provider;
// otherwise it comes from the author_identifiers rows the caller loaded into
// ProviderIdentifiers. A relink moves the primary id to the new provider and
// keeps the old one as an identifier, so both places are worth checking
// (#1734).
func (a Author) ProviderIdentity(provider string) (string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return "", false
	}
	if AuthorProviderFromForeignID(a.ForeignID) == provider {
		if id := strings.TrimSpace(a.ForeignID); id != "" {
			return id, true
		}
	}
	for p, id := range a.ProviderIdentifiers {
		if strings.ToLower(strings.TrimSpace(p)) != provider {
			continue
		}
		if id = strings.TrimSpace(id); id != "" {
			return id, true
		}
	}
	return "", false
}
