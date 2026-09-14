package abs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const (
	DefaultSourceID             = "default"
	importProgressResultsLimit  = 100
	SettingABSLastImportAt      = "abs.last_import_at"
	settingDefaultRootID        = "library.defaultRootFolderId"
	runEntityMetadataKind       = "abs_run_entity_metadata"
	runEntityMetadataVersion    = 1
	entityTypeAuthor            = "author"
	entityTypeBook              = "book"
	entityTypeSeries            = "series"
	entityTypeEdition           = "edition"
	providerAudiobookshelf      = "audiobookshelf"
	providerHardcover           = "hardcover"
	runStatusRunning            = "running"
	runStatusCompleted          = "completed"
	runStatusFailed             = "failed"
	runStatusRolledBack         = "rolled_back"
	itemOutcomeCreated          = "created"
	itemOutcomeLinked           = "linked"
	itemOutcomeUpdated          = "updated"
	itemOutcomeSkipped          = "skipped"
	itemOutcomeFailed           = "failed"
	reviewReasonUnmatchedAuthor = "unmatched_author"
	reviewReasonAmbiguousAuthor = "ambiguous_author"
	reviewReasonUnmatchedBook   = "unmatched_book"
	reviewReasonAmbiguousBook   = "ambiguous_book"
)

type importClientFactory func(baseURL, apiKey string) (enumerationClient, error)

type enumerateFunc func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error)

type enhancedHardcoverSeriesEnabledFunc func(context.Context) bool

type ImportConfig struct {
	SourceID   string
	BaseURL    string
	APIKey     string
	LibraryID  string
	LibraryIDs []string
	PathRemap  string
	Label      string
	Enabled    bool
	DryRun     bool
}

func (c ImportConfig) normalized() ImportConfig {
	c.SourceID = strings.TrimSpace(c.SourceID)
	if c.SourceID == "" {
		c.SourceID = DefaultSourceID
	}
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.LibraryID = strings.TrimSpace(c.LibraryID)
	c.LibraryIDs = normalizeLibraryIDs(c.LibraryID, c.LibraryIDs)
	if c.LibraryID == "" && len(c.LibraryIDs) > 0 {
		c.LibraryID = c.LibraryIDs[0]
	}
	c.PathRemap = strings.TrimSpace(c.PathRemap)
	c.Label = strings.TrimSpace(c.Label)
	if c.Label == "" {
		c.Label = "Audiobookshelf"
	}
	return c
}

func (c ImportConfig) Validate() error {
	c = c.normalized()
	if !c.Enabled {
		return errors.New("abs source is disabled")
	}
	if c.BaseURL == "" {
		return errors.New("abs base_url is empty")
	}
	if c.APIKey == "" {
		return errors.New("abs api_key is empty")
	}
	if _, err := NormalizeAPIKey(c.APIKey); err != nil {
		return err
	}
	if len(c.LibraryIDs) == 0 {
		return errors.New("abs library_ids is empty")
	}
	return nil
}

type ImportStats struct {
	LibrariesScanned     int `json:"librariesScanned"`
	PagesScanned         int `json:"pagesScanned"`
	ItemsSeen            int `json:"itemsSeen"`
	ItemsNormalized      int `json:"itemsNormalized"`
	ItemsDetailFetched   int `json:"itemsDetailFetched"`
	AuthorsCreated       int `json:"authorsCreated"`
	AuthorsLinked        int `json:"authorsLinked"`
	BooksCreated         int `json:"booksCreated"`
	BooksLinked          int `json:"booksLinked"`
	BooksUpdated         int `json:"booksUpdated"`
	SeriesCreated        int `json:"seriesCreated"`
	SeriesLinked         int `json:"seriesLinked"`
	EditionsAdded        int `json:"editionsAdded"`
	OwnedMarked          int `json:"ownedMarked"`
	PendingManual        int `json:"pendingManual"`
	ReviewQueued         int `json:"reviewQueued"`
	MetadataMatched      int `json:"metadataMatched"`
	MetadataRelinked     int `json:"metadataRelinked"`
	MetadataConflicts    int `json:"metadataConflicts"`
	MetadataAutoResolved int `json:"metadataAutoResolved"`
	Skipped              int `json:"skipped"`
	Failed               int `json:"failed"`

	dryRunSeriesExternalIDs map[string]struct{}
	dryRunSeriesTitles      map[string]struct{}
	dryRunSeriesMemberships map[string]struct{}
}

type ImportSourceSnapshot struct {
	SourceID   string   `json:"sourceId"`
	Label      string   `json:"label"`
	BaseURL    string   `json:"baseUrl"`
	LibraryID  string   `json:"libraryId"`
	LibraryIDs []string `json:"libraryIds,omitempty"`
	PathRemap  string   `json:"pathRemap,omitempty"`
	Enabled    bool     `json:"enabled"`
	DryRun     bool     `json:"dryRun"`
}

type ImportSummary struct {
	DryRun                bool              `json:"dryRun"`
	ResumedFromCheckpoint bool              `json:"resumedFromCheckpoint"`
	Checkpoint            *ImportCheckpoint `json:"checkpoint,omitempty"`
	Stats                 ImportStats       `json:"stats"`
	Error                 string            `json:"error,omitempty"`
}

type ImportItemResult struct {
	ItemID      string `json:"itemId"`
	Title       string `json:"title"`
	Outcome     string `json:"outcome"`
	Message     string `json:"message,omitempty"`
	MatchedBy   string `json:"matchedBy,omitempty"`
	AuthorID    int64  `json:"authorId,omitempty"`
	BookID      int64  `json:"bookId,omitempty"`
	SeriesCount int    `json:"seriesCount,omitempty"`
}

type ReviewFileMapping struct {
	Found   bool   `json:"found"`
	Message string `json:"message,omitempty"`
}

type ImportProgress struct {
	Running               bool               `json:"running"`
	RunID                 int64              `json:"runId,omitempty"`
	DryRun                bool               `json:"dryRun"`
	StartedAt             time.Time          `json:"startedAt"`
	FinishedAt            *time.Time         `json:"finishedAt,omitempty"`
	Processed             int                `json:"processed"`
	Message               string             `json:"message,omitempty"`
	Error                 string             `json:"error,omitempty"`
	ResumedFromCheckpoint bool               `json:"resumedFromCheckpoint"`
	Checkpoint            *ImportCheckpoint  `json:"checkpoint,omitempty"`
	Stats                 *ImportStats       `json:"stats,omitempty"`
	Results               []ImportItemResult `json:"results,omitempty"`
}

type PersistedImportRun struct {
	ID          int64                `json:"id"`
	SourceID    string               `json:"sourceId"`
	SourceLabel string               `json:"sourceLabel"`
	BaseURL     string               `json:"baseUrl"`
	LibraryID   string               `json:"libraryId"`
	Status      string               `json:"status"`
	DryRun      bool                 `json:"dryRun"`
	StartedAt   time.Time            `json:"startedAt"`
	FinishedAt  *time.Time           `json:"finishedAt,omitempty"`
	Source      ImportSourceSnapshot `json:"source"`
	Checkpoint  *ImportCheckpoint    `json:"checkpoint,omitempty"`
	Summary     ImportSummary        `json:"summary"`
}

type metadataMergeResult struct {
	Matched      int
	Relinked     int
	Conflicts    int
	AutoResolved int
	Messages     []string
}

type Importer struct {
	authors                *db.AuthorRepo
	aliases                *db.AuthorAliasRepo
	books                  *db.BookRepo
	editions               *db.EditionRepo
	series                 *db.SeriesRepo
	settings               *db.SettingsRepo
	runs                   *db.ABSImportRunRepo
	runEntities            *db.ABSImportRunEntityRepo
	provenance             *db.ABSProvenanceRepo
	reviews                *db.ABSReviewItemRepo
	conflicts              *db.ABSMetadataConflictRepo
	meta                   *metadata.Aggregator
	newClient              importClientFactory
	hardcoverSeriesEnabled enhancedHardcoverSeriesEnabledFunc
	userAgent              string
	enumerateFn            enumerateFunc
	rootFolders            *db.RootFolderRepo
	libraryDir             string
	audiobookDir           string

	// itemTimeout bounds the work for one ABS item; zero means
	// defaultItemTimeout. A field rather than a constant so tests can use a
	// short one (#2578).
	itemTimeout time.Duration

	// jobs, when set, tracks the detached import goroutine so process
	// shutdown can cancel and drain it before the database closes (#1458).
	// When nil (tests, non-wired callers) Start falls back to an untracked
	// goroutine on the caller's context.
	jobs *jobs.Group

	mu       sync.Mutex
	running  bool
	progress ImportProgress

	// upstreamAuthorCache memoizes upstream author-metadata lookups for the
	// duration of a single Run. Multiple books by the same author would
	// otherwise each re-issue the same provider search — and when that search
	// is slow or unreachable (OpenLibrary author search timing out for
	// romanized-CJK pen names, Hardcover returning 401) every repeat pays the
	// full per-request timeout again. Reset at the start of each Run so results
	// never leak across imports. Guarded by its own mutex, independent of mu.
	upstreamAuthorMu    sync.Mutex
	upstreamAuthorCache map[string]upstreamAuthorLookup
}

// upstreamAuthorLookup is one cached result of lookupUpstreamAuthor: the
// resolved author (nil when none matched), whether the match was ambiguous, and
// any error from the lookup. The error is cached deliberately — a name whose
// search timed out this run should not be retried for every subsequent book by
// that author.
type upstreamAuthorLookup struct {
	author    *models.Author
	ambiguous bool
	err       error
}

func (s ImportStats) String() string {
	data, _ := json.Marshal(s)
	return string(data)
}
