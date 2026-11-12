package calibre

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// ImportStats summarises one import run. Exposed verbatim to the UI so
// users can see "added X authors, Y books, merged Z duplicates" after
// the progress bar finishes.
type ImportStats struct {
	AuthorsAdded     int `json:"authorsAdded"`
	AuthorsLinked    int `json:"authorsLinked"`
	BooksAdded       int `json:"booksAdded"`
	BooksUpdated     int `json:"booksUpdated"`
	EditionsAdded    int `json:"editionsAdded"`
	DuplicatesMerged int `json:"duplicatesMerged"`
	SeriesLinked     int `json:"seriesLinked"`
	// SeriesReused counts series links filed into a series another importer
	// or a metadata sync had already created, instead of a new row (#2173).
	SeriesReused   int `json:"seriesReused,omitempty"`
	SeriesFailures int `json:"seriesFailures,omitempty"`
	Skipped        int `json:"skipped"`
	// FormatsSkipped counts Calibre formats left untracked because Bindery
	// cannot use the file, such as the ORIGINAL_EPUB backup Polish keeps
	// (#3630).
	FormatsSkipped int `json:"formatsSkipped,omitempty"`
	// UnsupportedFilesRemoved counts book_files rows an earlier import created
	// for such formats that this run stopped tracking. Files on disk stay.
	UnsupportedFilesRemoved int `json:"unsupportedFilesRemoved,omitempty"`
}

// ImportProgress is the live poll shape for GET /calibre/import/status.
// The UI uses Running to decide whether to render a progress bar, Total
// + Processed to fill it, and Stats (populated on completion) for the
// summary panel.
type ImportProgress struct {
	Running    bool         `json:"running"`
	StartedAt  time.Time    `json:"startedAt"`
	Total      int          `json:"total"`
	Processed  int          `json:"processed"`
	Message    string       `json:"message,omitempty"`
	Error      string       `json:"error,omitempty"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty"`
	Stats      *ImportStats `json:"stats,omitempty"`
}

// Importer orchestrates a read-only Calibre library scan and upserts the
// extracted entities into Bindery. A single importer instance is safe for
// concurrent callers: Start is guarded by a mutex so two clicks on the
// "Import library" button yield one run plus a "already running" error.
type Importer struct {
	authors  *db.AuthorRepo
	aliases  *db.AuthorAliasRepo
	books    *db.BookRepo
	editions *db.EditionRepo
	settings *db.SettingsRepo

	// Run-tracking + rollback (issue #643). Optional — when any of these is
	// nil the importer behaves as before with no run record and no
	// snapshots, so test wiring that only needs the import path still works.
	runs       *db.CalibreImportRunRepo
	snapshots  *db.CalibreEntitySnapshotRepo
	provenance *db.CalibreProvenanceRepo

	// Series persistence (issue #905). Optional; nil disables series creation
	// during import (test wiring that doesn't care about series stays
	// minimal). Production wires this via WithSeries from cmd/bindery/main.go.
	series *db.SeriesRepo

	// covers is where a library's cover.jpg files are copied so the image
	// proxy can serve them (#2564). Nil means no cover is stored at all:
	// the importer never writes the library path into image_url, because
	// nothing in Bindery can serve a host path and the row would only look
	// populated.
	covers *covers.Store

	openReader func(libraryPath string) (readerIface, error)

	// liveCalibreIDs is the set of calibre ids in the library being imported,
	// collected before any book is bound. A row whose calibre_id is in it is
	// owned by another live Calibre book (#3631). Only touched by the import
	// goroutine.
	liveCalibreIDs map[int64]struct{}

	// listAuthors loads every author for name matching. A field so tests can
	// count calls; production uses authors.List.
	listAuthors func(ctx context.Context) ([]models.Author, error)

	authorIdx *authorIndex

	// seriesIdx groups every series by db.SeriesTitleKey for one run, loaded
	// the first time a Calibre series name has no row of its own (#2173).
	// seriesAmbiguous holds the names already logged as existing more than
	// once, so the hint is logged once per name per run. Only touched by the
	// import goroutine.
	seriesIdx       *seriesTitleIndex
	seriesAmbiguous map[string]struct{}

	mu       sync.Mutex
	running  bool
	progress ImportProgress
}

// readerIface captures the subset of *Reader the importer actually uses,
// so tests can inject a fake library without standing up a full SQLite
// fixture for every scenario.
type readerIface interface {
	Count(ctx context.Context) (int, error)
	Books(ctx context.Context, fn func(CalibreBook) error) error
	Close() error
}

// NewImporter wires the repos Bindery tracks Calibre entities into.
func NewImporter(
	authors *db.AuthorRepo,
	aliases *db.AuthorAliasRepo,
	books *db.BookRepo,
	editions *db.EditionRepo,
	settings *db.SettingsRepo,
) *Importer {
	return &Importer{
		listAuthors: authors.List,
		authors:     authors,
		aliases:     aliases,
		books:       books,
		editions:    editions,
		settings:    settings,
		openReader: func(lp string) (readerIface, error) {
			r, err := OpenReader(lp)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}

// WithRunTracking attaches the run/provenance/snapshot repos required for
// rollback (#643). Optional — without it the importer records nothing and
// rollback APIs return ErrRollbackUnavailable.
func (i *Importer) WithRunTracking(runs *db.CalibreImportRunRepo, snapshots *db.CalibreEntitySnapshotRepo, provenance *db.CalibreProvenanceRepo) *Importer {
	i.runs = runs
	i.snapshots = snapshots
	i.provenance = provenance
	return i
}

// WithSeries attaches the series repo so Calibre series memberships
// (issue #905) get persisted as series + series_books rows. When unset the
// importer behaves as before and skips series creation; this preserves
// test wiring that doesn't need series semantics.
func (i *Importer) WithSeries(series *db.SeriesRepo) *Importer {
	i.series = series
	return i
}

// WithCoverStore attaches the store each imported book's cover.jpg is copied
// into (#2564). Production wires <DataDir>/covers, the same store the image
// proxy serves bindery-cover: references from.
func (i *Importer) WithCoverStore(store *covers.Store) *Importer {
	i.covers = store
	return i
}

// Progress returns a snapshot of the current (or most recent) import.
// Safe to call at any time; when no import has run yet, the zero value
// Running=false / Total=0 is returned.
func (i *Importer) Progress() ImportProgress {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.progress
}

// Running reports whether an import is currently in flight.
func (i *Importer) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running
}

// ErrAlreadyRunning is returned when Start is called while a previous
// import is still executing. Maps to 409 Conflict at the API layer.
var ErrAlreadyRunning = errors.New("calibre import already running")

// Start kicks off an import in the background and returns immediately.
// The caller is expected to pass `context.WithoutCancel(r.Context())`
// so the import survives the HTTP request that triggered it — the
// v0.7.2 library-scan fix ships this pattern already.
//
// libraryPath is the Calibre library root containing metadata.db.
func (i *Importer) Start(ctx context.Context, libraryPath string) error {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return ErrAlreadyRunning
	}
	i.running = true
	i.progress = ImportProgress{
		Running:   true,
		StartedAt: time.Now().UTC(),
		Message:   "opening library…",
	}
	i.mu.Unlock()

	go i.run(ctx, libraryPath)
	return nil
}

// Run executes the import synchronously. Useful for the startup-sync
// path where main.go is already on its own goroutine and wants a blocking
// error return. Internally delegates to run() which handles progress and
// the running flag.
func (i *Importer) Run(ctx context.Context, libraryPath string) (*ImportStats, error) {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	i.running = true
	i.progress = ImportProgress{
		Running:   true,
		StartedAt: time.Now().UTC(),
		Message:   "opening library…",
	}
	i.mu.Unlock()

	stats := i.run(ctx, libraryPath)
	p := i.Progress()
	if p.Error != "" {
		return stats, errors.New(p.Error)
	}
	return stats, nil
}

func (i *Importer) run(ctx context.Context, libraryPath string) *ImportStats {
	stats := &ImportStats{}
	// Fresh author name index for this run, loaded on the first lookup (#1469).
	i.authorIdx = &authorIndex{}
	i.seriesIdx = &seriesTitleIndex{}
	i.seriesAmbiguous = map[string]struct{}{}
	defer func() {
		i.authorIdx = nil
		i.seriesIdx = nil
		i.seriesAmbiguous = nil
	}()
	var runID int64
	// Run record: only created on a real (non-dry-run) import. A dry run
	// today doesn't exist at the API surface, but the run field is here so
	// adding one later is a one-line change.
	if i.runs != nil {
		run := &models.CalibreImportRun{
			SourceID:         defaultSourceID,
			LibraryPath:      libraryPath,
			Status:           runStatusRunning,
			DryRun:           false,
			SourceConfigJSON: encodeSourceConfig(libraryPath),
			SummaryJSON:      "{}",
		}
		if err := i.runs.Create(ctx, run); err != nil {
			slog.Warn("calibre import: create run record failed", "error", err)
		} else {
			runID = run.ID
		}
	}
	failed := false
	defer func() {
		now := time.Now().UTC()
		i.mu.Lock()
		i.progress.Running = false
		i.progress.FinishedAt = &now
		i.progress.Stats = stats
		i.running = false
		i.mu.Unlock()
		if runID != 0 && i.runs != nil {
			status := runStatusCompleted
			if failed {
				status = runStatusFailed
			}
			// Use Background so we still close out the run even if the
			// calling ctx was cancelled mid-import.
			if err := i.runs.Finish(context.Background(), runID, status, stats); err != nil {
				slog.Warn("calibre import: finish run record failed", "runID", runID, "error", err)
			}
		}
	}()

	reader, err := i.openReader(libraryPath)
	if err != nil {
		failed = true
		i.fail(err)
		return stats
	}
	defer func() {
		if cerr := reader.Close(); cerr != nil {
			slog.Warn("calibre: close reader", "error", cerr)
		}
	}()

	total, err := reader.Count(ctx)
	if err != nil {
		failed = true
		i.fail(err)
		return stats
	}

	// Clear out what imports before #3630 tracked and this one no longer will.
	stats.UnsupportedFilesRemoved = i.removeUnsupportedBookFiles(ctx)
	i.setProgress(func(p *ImportProgress) {
		p.Total = total
		p.Message = fmt.Sprintf("scanning %d books…", total)
	})

	// One cheap pass first so path 3 of upsertBook can tell a calibre_id that
	// another live book owns from a stale one left by a removed or renumbered
	// book (#3631).
	live := make(map[int64]struct{}, total)
	if err := reader.Books(ctx, func(cb CalibreBook) error {
		live[cb.CalibreID] = struct{}{}
		return ctx.Err()
	}); err != nil {
		failed = true
		i.fail(err)
		return stats
	}
	i.liveCalibreIDs = live
	defer func() { i.liveCalibreIDs = nil }()

	err = reader.Books(ctx, func(cb CalibreBook) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		i.importOne(ctx, runID, cb, stats)
		i.setProgress(func(p *ImportProgress) { p.Processed++ })
		return nil
	})
	if err != nil {
		failed = true
		i.fail(err)
		return stats
	}

	if err := i.settings.Set(ctx, "calibre.last_import_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Warn("calibre import: persist last_import_at failed", "error", err)
	}
	slog.Info("calibre import complete",
		"runID", runID,
		"authorsAdded", stats.AuthorsAdded, "authorsLinked", stats.AuthorsLinked,
		"booksAdded", stats.BooksAdded, "booksUpdated", stats.BooksUpdated,
		"editionsAdded", stats.EditionsAdded, "duplicatesMerged", stats.DuplicatesMerged,
		"seriesLinked", stats.SeriesLinked, "seriesReused", stats.SeriesReused,
		"skipped", stats.Skipped, "formatsSkipped", stats.FormatsSkipped,
		"unsupportedFilesRemoved", stats.UnsupportedFilesRemoved)
	i.setProgress(func(p *ImportProgress) {
		p.Message = "done"
	})
	return stats
}

// importOne handles a single CalibreBook: resolve/create its author, then
// create or update the Bindery book row, then upsert one edition per
// format. Errors on any step are logged + counted as skipped so one bad
// record doesn't abort the whole library.
func (i *Importer) importOne(ctx context.Context, runID int64, cb CalibreBook, stats *ImportStats) {
	if len(cb.Authors) == 0 {
		slog.Warn("calibre import: book has no authors", "calibre_id", cb.CalibreID, "title", cb.Title)
		stats.Skipped++
		return
	}

	author, created, err := i.resolveAuthor(ctx, runID, cb.Authors[0])
	if err != nil {
		slog.Warn("calibre import: author resolve failed", "calibre_id", cb.CalibreID, "error", err)
		stats.Skipped++
		return
	}
	if created {
		stats.AuthorsAdded++
	} else {
		stats.AuthorsLinked++
	}
	// Co-authors after the first are deliberately not recorded. Bindery books
	// carry a single author_id, and the importer used to park the extra
	// credits in author_aliases pointing at the primary. That made every
	// co-author an alias of a person they merely shared a cover with, and
	// stopped them ever getting an author row of their own (#1684). Dropping
	// the credit loses nothing that was ever modelled; keeping it lost the
	// author.
	if len(cb.Authors) > 1 {
		slog.Debug("calibre import: co-authors not modelled, filing under the first credit",
			"calibre_id", cb.CalibreID, "author", author.Name, "coauthors", len(cb.Authors)-1)
	}

	book, newBook, err := i.upsertBook(ctx, runID, author, cb)
	if err != nil {
		slog.Warn("calibre import: book upsert failed", "calibre_id", cb.CalibreID, "error", err)
		stats.Skipped++
		return
	}
	// Copy the library's cover.jpg into the covers store once per Calibre
	// book (every format shares it) and give the book row that cover unless
	// a metadata provider already supplied one. Done before the after
	// snapshot below so a rollback can put the previous value back.
	coverRef := i.storeCover(cb)
	i.applyBookCover(ctx, book.row, coverRef)
	if newBook {
		stats.BooksAdded++
	} else {
		stats.BooksUpdated++
		// "DuplicatesMerged" counts the case where we found an existing
		// Bindery row (by title+author, not by calibre_id) and linked it
		// to this Calibre book. Distinguishing it from a plain re-import
		// helps the UI message "we folded N pre-existing books into
		// their Calibre counterparts".
		if book.mergedByTitle {
			stats.DuplicatesMerged++
		}
	}
	// Record provenance + after-snapshot for the book regardless of new/old
	// outcome. The before-snapshot was captured inside upsertBook on the
	// path that mutates an existing row.
	bookExternalID := calibreBookExternalID(cb.CalibreID)
	bookOutcome := outcomeUpdated
	if newBook {
		bookOutcome = outcomeCreated
	} else if book.mergedByTitle {
		bookOutcome = outcomeLinked
	}
	i.upsertProvenance(ctx, runID, entityTypeBook, bookExternalID, book.row.ID)
	if !newBook {
		i.recordBookAfterSnapshot(ctx, runID, bookExternalID, book.row.ID, bookOutcome, map[string]any{"matchedBy": book.matchedBy})
	} else {
		// For freshly-created books no before-snapshot exists; record an
		// empty-payload snapshot so the rollback list sees the entity and
		// knows to delete it.
		i.recordCreateSnapshot(ctx, runID, entityTypeBook, bookExternalID, book.row.ID)
	}

	for _, f := range cb.Formats {
		added, edition, err := i.upsertEdition(ctx, runID, book.row, cb, f, coverRef)
		if err != nil {
			slog.Warn("calibre import: edition upsert failed",
				"calibre_id", cb.CalibreID, "format", f.Format, "error", err)
			continue
		}
		if added {
			stats.EditionsAdded++
		}
		if edition != nil {
			editionExternalID := calibreEditionExternalID(cb.CalibreID, f.Format)
			i.upsertProvenance(ctx, runID, entityTypeEdition, editionExternalID, edition.ID)
			if added {
				i.recordCreateSnapshot(ctx, runID, entityTypeEdition, editionExternalID, edition.ID)
			}
		}
	}

	// Track the files themselves, not just the editions (#1635). The import
	// used to leave book_files untouched, so a Calibre-managed book had its
	// path recorded only in the legacy books.file_path column, or nowhere at
	// all on the freshly-created path. Anything reading tracked files saw a
	// book with none, and there was no supported way to re-point a stale row
	// for a Calibre library: a folder scan fights the Calibre-shaped layout,
	// and manual-import/match needs a downloadId a Calibre book never has.
	i.registerBookFiles(ctx, runID, book.row, cb, stats)

	// Series persistence (#905). Calibre's books_series_link plus series
	// table is read by the cursor as cb.Series; we propagate the membership
	// into Bindery's series + series_books shape so the Wanted / detail /
	// Hardcover-enhanced flows have the data to attach to. Skipped when no
	// series repo is wired (test fixtures) or no series was on the row.
	if i.series != nil && cb.Series != nil && cb.Series.Name != "" {
		i.attachBookToSeries(ctx, runID, book.row, cb.Series, stats)
	}
}

// calibreAudioFormats are the Calibre format names that make a file an
// audiobook rather than an ebook. Calibre records a format as an uppercase
// extension ("EPUB", "M4B"), so this is an extension test in Calibre's own
// spelling.
//
// Deliberately a local copy of internal/importer's audio extension set rather
// than a shared helper: internal/importer already imports this package, so
// depending on it here would be a cycle.
var calibreAudioFormats = map[string]bool{
	"MP3": true, "M4A": true, "M4B": true, "AAC": true,
	"FLAC": true, "OGG": true, "OPUS": true,
}

// calibreFormatMediaType classifies one Calibre format as the media type
// book_files stores it under.
func calibreFormatMediaType(format string) string {
	if calibreAudioFormats[strings.ToUpper(strings.TrimSpace(format))] {
		return models.MediaTypeAudiobook
	}
	return models.MediaTypeEbook
}

// registerBookFiles records the on-disk path of each format Calibre reports
// against the book, so the tracked path and the path the UI derives cannot
// drift apart (#1635).
//
// One row per format, not just the first. Calibre is the authority on what a
// book has, and an EPUB alongside an M4B is two formats of one book rather
// than a choice between them; registering only the first would leave the
// audiobook untracked on exactly the libraries most likely to hold both.
//
// The insert is OR IGNORE on a globally unique path, so re-running an import is
// a no-op and a path another book already owns is left alone rather than
// stolen. A file that has MOVED inside the Calibre library appends the new row
// beside the old one; which of the two the book then renders is decided by the
// file-tracking rules in #2186, not here.
//
// Ownership is narrowed the way #1868 narrowed series links: a row is claimed,
// and therefore eligible for rollback, only when this run is the one that
// inserted it. A path a real download placed, or an earlier run created, gets
// its provenance refreshed but is never unwound by this run's rollback.
//
// Only formats Bindery can use are tracked, judged by
// models.IsCalibreBookFilePath: the library scan's extension allowlist plus the
// few Calibre reading formats the scan never took (PRC, bare KEPUB, AAC), which
// earlier imports always tracked. Calibre lists every file
// it keeps, including the ORIGINAL_EPUB / ORIGINAL_AZW3 backups its Polish and
// Fix EPUB tools leave beside the book, and tracking those made each one an
// ebook file Bindery could not open, remap or deliver (#3630).
//
// Failures are logged and never abort the import: the book and its metadata
// are already committed, and losing the whole row over a file-tracking write
// would be a worse outcome than an untracked path.
func (i *Importer) registerBookFiles(ctx context.Context, runID int64, book *models.Book, cb CalibreBook, stats *ImportStats) {
	if i.books == nil || book == nil || book.ID == 0 {
		return
	}
	for _, f := range cb.Formats {
		path := strings.TrimSpace(f.AbsolutePath)
		if path == "" {
			continue
		}
		if !models.IsCalibreBookFilePath(path) {
			slog.Debug("calibre import: not tracking a format Bindery cannot use",
				"calibre_id", cb.CalibreID, "book_id", book.ID, "format", f.Format, "path", path)
			stats.FormatsSkipped++
			continue
		}
		created, err := i.books.AddBookFileIfMissing(ctx, book.ID, calibreFormatMediaType(f.Format), path)
		if err != nil {
			slog.Warn("calibre import: could not track book file",
				"calibre_id", cb.CalibreID, "book_id", book.ID, "format", f.Format, "path", path, "error", err)
			continue
		}
		externalID := calibreBookFileExternalID(path)
		i.upsertProvenance(ctx, runID, entityTypeBookFile, externalID, book.ID)
		if created {
			i.recordCreateSnapshot(ctx, runID, entityTypeBookFile, externalID, book.ID)
		}
	}
}

const bookFileExternalIDPrefix = "calibre:book-file:"

// removeUnsupportedBookFiles stops tracking the book_files rows earlier imports
// created for formats Bindery cannot use, before registerBookFiles learned to
// skip them (#3630). It returns how many rows it removed.
//
// Only rows the import's own records prove it created are touched: provenance
// still pointing at the row's book plus a "created" snapshot (see
// ListCreatedBookFiles). A row with the same extension that a download, a scan
// or the user added stays. The file on disk is never touched, and the row goes
// through UntrackFilePathForBook, which refreshes the book's status the way
// removing a file entry by hand does. The provenance goes with the row, so a
// later rollback of the run that created it reports it as already undone.
//
// Running at the start of every import is safe: once the rows are gone there
// is nothing left to match. The query is one pass over the created book_file
// snapshots, read through a partial index, with index lookups into provenance
// and book_files (see listCreatedBookFilesSQL): about 42ms for 10,000 books
// over 40 imports.
//
// Only rows from imports since v1.35.0 carry the created snapshot. Rows an
// import from v1.32.2 to v1.34.x created have provenance only and are never
// removed here; removing them by hand now sticks, because the import no
// longer adds them back.
func (i *Importer) removeUnsupportedBookFiles(ctx context.Context) int {
	if i.books == nil || i.provenance == nil {
		return 0
	}
	created, err := i.provenance.ListCreatedBookFiles(ctx, bookFileExternalIDPrefix)
	if err != nil {
		slog.Warn("calibre import: could not list book files earlier imports created", "error", err)
		return 0
	}
	removed := 0
	for _, f := range created {
		if models.IsCalibreBookFilePath(f.Path) {
			continue
		}
		ok, err := i.books.UntrackFilePathForBook(ctx, f.Path, f.BookID)
		if err != nil {
			slog.Warn("calibre import: could not stop tracking an unusable book file",
				"book_id", f.BookID, "path", f.Path, "error", err)
			continue
		}
		if !ok {
			continue
		}
		removed++
		slog.Debug("calibre import: stopped tracking a format Bindery cannot use",
			"book_id", f.BookID, "path", f.Path)
		if err := i.provenance.DeleteByExternal(ctx, f.SourceID, entityTypeBookFile, f.ExternalID); err != nil {
			slog.Warn("calibre import: could not drop provenance for an untracked book file",
				"book_id", f.BookID, "path", f.Path, "error", err)
		}
	}
	if removed > 0 {
		slog.Info("calibre import: stopped tracking book files Bindery cannot use", "removed", removed)
	}
	return removed
}

// calibreBookFileExternalID is the provenance key for a book_files row created
// during a Calibre import run. The path is the natural key: book_files.path is
// globally UNIQUE, and rollback deletes by path rather than by row id, which
// the provenance row's local_id (the book) does not carry.
func calibreBookFileExternalID(path string) string {
	return bookFileExternalIDPrefix + path
}

// parseCalibreBookFileExternalID recovers the on-disk path a book-file
// provenance key was built from.
func parseCalibreBookFileExternalID(externalID string) (path string, ok bool) {
	rest, found := strings.CutPrefix(externalID, bookFileExternalIDPrefix)
	if !found || rest == "" {
		return "", false
	}
	return rest, true
}

// attachBookToSeries finds or creates the Bindery series row for cb.Series
// (see resolveSeries) and links the book at the recorded position. Errors are
// logged at WARN and counted as skipped on the series side, but never abort
// the book import: a series-link failure is strictly less damaging than
// losing the book itself.
func (i *Importer) attachBookToSeries(ctx context.Context, runID int64, book *models.Book, cs *CalibreSeries, stats *ImportStats) {
	series, reused, err := i.resolveSeries(ctx, book, cs.Name)
	if err != nil {
		slog.Warn("calibre import: series upsert failed", "name", cs.Name, "error", err)
		stats.SeriesFailures++
		return
	}
	position := ""
	if cs.Position > 0 {
		position = strconv.FormatFloat(cs.Position, 'f', -1, 64)
	}
	// Link first so we learn whether this run actually created the
	// membership. Only a link this run created may be unwound by a
	// rollback — a membership the user (or an earlier run) already had must
	// survive. When the link already existed, refresh the position only:
	// rewriting primary_series here re-promoted a series the user had
	// demoted, on every re-import (#2525).
	created, err := i.series.LinkBookPreservingPrimary(ctx, series.ID, book.ID, position)
	if err == nil && !created {
		err = i.series.UpdateBookLinkPosition(ctx, series.ID, book.ID, position)
	}
	if err != nil {
		slog.Warn("calibre import: series link failed", "name", cs.Name, "book_id", book.ID, "error", err)
		stats.SeriesFailures++
		return
	}
	if runID != 0 {
		// Record provenance so a rollback can unlink the book without
		// orphaning the series. Series row deletion is best-effort: shared
		// series should survive a single-book rollback, so we only record
		// the link, not the series create. The snapshot is what puts the
		// link in rollback's work list, and it is only written when this run
		// created the membership. A series resolveSeries reused from another
		// importer or a metadata sync is covered by the same rule: nothing
		// the Calibre run records names the series row, so rolling the run
		// back can only take this book out of it, never delete it (#2173).
		externalID := calibreSeriesLinkExternalID(book.ID, series.ID)
		i.upsertProvenance(ctx, runID, entityTypeSeriesLink, externalID, book.ID)
		if created {
			i.recordCreateSnapshot(ctx, runID, entityTypeSeriesLink, externalID, book.ID)
		}
	}
	stats.SeriesLinked++
	if reused {
		stats.SeriesReused++
	}
}

// resolveSeries returns the series a Calibre series name files into, and
// whether it is a series this import reused rather than its own row (#2173).
//
// The Calibre row comes first: a series whose foreign id, or an alias of it,
// is "calibre:series:<name>" is the one earlier runs used. Otherwise a series
// that already exists under the same title is reused, provided it is plausibly
// the same series (see findReusableSeries), and the Calibre id is recorded as
// an alias of it so the next run lands there through the first step and the
// title match never runs again for that name. Only when neither finds a series
// is the Calibre row created, with a synthetic foreign id that a later
// metadata sync can promote to a real one without losing the link.
//
// Before #2173 the title step did not exist, so a Calibre import after an
// Audiobookshelf import or an author sync forked every series the two shared,
// and the Audiobookshelf import then logged each of them as an ambiguous match
// and could not link a Hardcover series to any of the books.
//
// There is no dry run on the Calibre import, so this always writes.
func (i *Importer) resolveSeries(ctx context.Context, book *models.Book, name string) (*models.Series, bool, error) {
	foreignID := calibreSeriesForeignID(name)
	existing, err := i.series.GetByForeignID(ctx, foreignID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	match, err := i.findReusableSeries(ctx, book, name)
	if err != nil {
		return nil, false, err
	}
	if match != nil {
		// The alias is a shortcut for later runs, not part of this book's
		// link: when it cannot be written the next run makes the same title
		// decision again, so a failure is logged and the link still goes
		// ahead. A Calibre rollback leaves the alias in place on purpose. It
		// only says which series this name resolves to, which is what the
		// title match would decide again, and it goes away with the series if
		// the user deletes it or follows it if the user merges it.
		if _, err := i.series.AddAlias(ctx, foreignID, match.ID); err != nil {
			slog.Warn("calibre import: could not record the calibre series id on the reused series",
				"name", name, "series_id", match.ID, "error", err)
		}
		slog.Debug("calibre import: filing into the existing series of the same title",
			"name", name, "series_id", match.ID, "series_foreign_id", match.ForeignID, "book_id", book.ID)
		return match, true, nil
	}
	series := &models.Series{ForeignID: foreignID, Title: name}
	if err := i.series.CreateOrGet(ctx, series); err != nil {
		return nil, false, err
	}
	if i.seriesIdx != nil {
		i.seriesIdx.add(*series)
	}
	return series, false, nil
}

// findReusableSeries returns the existing series a Calibre series name should
// file into, or nil when the Calibre import should use its own row.
//
// A candidate is a series whose title has the same db.SeriesTitleKey as name,
// the key the Audiobookshelf import matches by too. It is acceptable only when
// it already holds a book by this book's author, or holds no books at all.
// That is stricter than the Audiobookshelf import on purpose: a same titled
// series holding only other authors' books ("Foundation" by two different
// writers) is a different series, and filing into it would be wrong in a way
// the user cannot easily see.
//
// With one acceptable candidate, that is the series. With several, the one
// already holding this book wins, the same tie break the Audiobookshelf import
// uses. When even that does not decide, it returns nil and the caller falls
// back to the Calibre row it would have used before #2173, rather than pick
// one of the candidates. The candidates are already duplicates of each other.
// Guessing would file books into whichever one the guess favoured, so the
// user, who has to merge them on the Series page anyway, would find books
// split across rows by chance instead of by importer. Falling back keeps this
// run no worse than the old behaviour and leaves the merge with the user, who
// gets one INFO line per name saying so.
func (i *Importer) findReusableSeries(ctx context.Context, book *models.Book, name string) (*models.Series, error) {
	key := db.SeriesTitleKey(name)
	if key == "" || book == nil {
		return nil, nil
	}
	idx := i.seriesIdx
	if idx == nil {
		idx = &seriesTitleIndex{}
	}
	if err := idx.load(ctx, i.series); err != nil {
		return nil, err
	}
	var acceptable []models.Series
	for _, c := range idx.byKey[key] {
		books, byAuthor, err := i.series.SeriesAuthorEvidence(ctx, c.ID, book.AuthorID)
		if err != nil {
			return nil, err
		}
		if books == 0 || byAuthor > 0 {
			acceptable = append(acceptable, c)
		}
	}
	switch len(acceptable) {
	case 0:
		return nil, nil
	case 1:
		return &acceptable[0], nil
	}
	memberOf, err := i.series.GetSeriesIDsForBook(ctx, book.ID)
	if err != nil {
		return nil, err
	}
	var held []int
	for n := range acceptable {
		if slices.Contains(memberOf, acceptable[n].ID) {
			held = append(held, n)
		}
	}
	if len(held) == 1 {
		return &acceptable[held[0]], nil
	}
	i.logAmbiguousSeries(name, len(acceptable))
	return nil, nil
}

// logAmbiguousSeries tells the user, once per name per run, that a series
// exists more than once and the import kept its own row instead of choosing.
func (i *Importer) logAmbiguousSeries(name string, copies int) {
	k := strings.ToLower(strings.TrimSpace(name))
	if i.seriesAmbiguous != nil {
		if _, done := i.seriesAmbiguous[k]; done {
			return
		}
		i.seriesAmbiguous[k] = struct{}{}
	}
	slog.Info("calibre import: series exists more than once, filing into the calibre series; merge the copies on the Series page",
		"name", name, "copies", copies)
}

// seriesTitleIndex groups series by db.SeriesTitleKey so a run computes each
// stored title's key once instead of once per Calibre series (#2173). Rows
// this run creates are added as it goes; rows it reuses are already in it.
type seriesTitleIndex struct {
	loaded bool
	byKey  map[string][]models.Series
}

func (x *seriesTitleIndex) load(ctx context.Context, repo *db.SeriesRepo) error {
	if x.loaded {
		return nil
	}
	all, err := repo.ListTitles(ctx)
	if err != nil {
		return err
	}
	x.byKey = make(map[string][]models.Series, len(all))
	x.loaded = true
	for _, s := range all {
		x.add(s)
	}
	return nil
}

// add files s under its key. It does nothing before load, which reads s from
// the database anyway, or for a row already indexed.
func (x *seriesTitleIndex) add(s models.Series) {
	if !x.loaded || s.ID == 0 {
		return
	}
	key := db.SeriesTitleKey(s.Title)
	if key == "" {
		return
	}
	for _, have := range x.byKey[key] {
		if have.ID == s.ID {
			return
		}
	}
	x.byKey[key] = append(x.byKey[key], s)
}

// calibreSeriesForeignID synthesises a stable, namespaced foreign id for a
// Calibre-imported series. Real provider IDs (Hardcover, OpenLibrary) will
// overwrite this on later sync; until then the namespace prevents
// collisions with provider-issued IDs.
func calibreSeriesForeignID(name string) string {
	return "calibre:series:" + strings.ToLower(strings.TrimSpace(name))
}

// calibreSeriesLinkExternalID is the provenance key for a book-to-series
// link recorded during a Calibre import run. Combines both IDs so the
// rollback layer can unwind the link even after the series ID is recycled.
func calibreSeriesLinkExternalID(bookID, seriesID int64) string {
	return seriesLinkExternalIDPrefix + strconv.FormatInt(bookID, 10) + ":" + strconv.FormatInt(seriesID, 10)
}

const seriesLinkExternalIDPrefix = "calibre:series-link:"

// parseCalibreSeriesLinkExternalID recovers the (bookID, seriesID) pair a
// series-link provenance key was built from. Rollback needs the series ID,
// which the provenance row's local_id (the book) does not carry.
func parseCalibreSeriesLinkExternalID(externalID string) (bookID, seriesID int64, ok bool) {
	rest, found := strings.CutPrefix(externalID, seriesLinkExternalIDPrefix)
	if !found {
		return 0, 0, false
	}
	bookPart, seriesPart, found := strings.Cut(rest, ":")
	if !found {
		return 0, 0, false
	}
	bookID, err := strconv.ParseInt(bookPart, 10, 64)
	if err != nil || bookID <= 0 {
		return 0, 0, false
	}
	seriesID, err = strconv.ParseInt(seriesPart, 10, 64)
	if err != nil || seriesID <= 0 {
		return 0, 0, false
	}
	return bookID, seriesID, true
}

// recordCreateSnapshot records a marker row for an entity that was newly
// created during this run. Rollback treats outcome=="created" as a hard
// delete signal regardless of whether a before-snapshot exists.
func (i *Importer) recordCreateSnapshot(ctx context.Context, runID int64, entityType, externalID string, localID int64) {
	if runID == 0 || i.snapshots == nil {
		return
	}
	// A minimal envelope: no before/after payload, just kind+version so the
	// rollback decoder doesn't choke. The "created" outcome is what
	// rollback keys off; snapshot payload is irrelevant for delete cases.
	envelope := runEntityMetadataEnvelope{
		Kind:    runEntityMetadataKind,
		Version: runEntityMetadataVersion,
	}
	i.recordSnapshot(ctx, runID, externalID, entityType, localID, outcomeCreated, envelope)
}

func calibreBookExternalID(calibreID int64) string {
	return fmt.Sprintf("book:%d", calibreID)
}

func calibreAuthorExternalID(calibreID int64) string {
	return fmt.Sprintf("author:%d", calibreID)
}

func calibreAuthorForeignID(calibreID int64) string {
	return "calibre:" + calibreAuthorExternalID(calibreID)
}

func calibreEditionExternalID(calibreID int64, format string) string {
	return fmt.Sprintf("edition:%d:%s", calibreID, strings.ToUpper(format))
}

func encodeSourceConfig(libraryPath string) string {
	// Tiny payload; written via fmt.Sprintf rather than json.Marshal to
	// avoid pulling in encoding/json for one field. Path itself is escaped
	// via %q.
	return fmt.Sprintf(`{"libraryPath":%q}`, libraryPath)
}

// resolveAuthor returns the canonical Bindery author for the given Calibre
// author. Lookup order:
//  1. Exact name match on authors.name
//  2. Alias match via author_aliases.name, but only when the alias row is
//     itself trustworthy evidence of identity (see aliasBindsAuthor)
//  3. Create a fresh authors row.
//
// `created` is true only for case 3. Calibre's author names are already
// one-per-row in its metadata.db, so we trust them verbatim rather than
// re-splitting on commas/separators.
func (i *Importer) resolveAuthor(ctx context.Context, runID int64, ca CalibreAuthor) (*models.Author, bool, error) {
	name := strings.TrimSpace(ca.Name)
	if name == "" {
		return nil, false, errors.New("empty author name")
	}

	externalID := calibreAuthorExternalID(ca.CalibreID)
	foreignID := calibreAuthorForeignID(ca.CalibreID)

	if existing, err := i.authors.GetByAnyForeignID(ctx, foreignID); err != nil {
		return nil, false, err
	} else if existing != nil {
		i.recordAuthorBeforeSnapshot(ctx, runID, externalID, existing, outcomeLinked, map[string]any{"matchedBy": "identifier"})
		i.upsertProvenance(ctx, runID, entityTypeAuthor, externalID, existing.ID)
		i.recordAuthorAfterSnapshot(ctx, runID, externalID, existing.ID, outcomeLinked, map[string]any{"matchedBy": "identifier"})
		return existing, false, nil
	}

	if existing, err := i.findAuthorByName(ctx, name); err != nil {
		return nil, false, err
	} else if existing != nil {
		i.recordAuthorBeforeSnapshot(ctx, runID, externalID, existing, outcomeLinked, map[string]any{"matchedBy": "name"})
		if err := i.authors.UpsertAuthorIdentifier(ctx, existing.ID, foreignID); err != nil {
			return nil, false, err
		}
		i.upsertProvenance(ctx, runID, entityTypeAuthor, externalID, existing.ID)
		i.recordAuthorAfterSnapshot(ctx, runID, externalID, existing.ID, outcomeLinked, map[string]any{"matchedBy": "name"})
		return existing, false, nil
	}

	if alias, err := i.aliases.GetByName(ctx, name); err != nil {
		return nil, false, err
	} else if alias != nil {
		existing, err := i.authors.GetByID(ctx, alias.AuthorID)
		if err != nil {
			return nil, false, err
		}
		switch {
		case existing == nil:
			// Dangling alias (author deleted without cascade). Ignore it.
		case !aliasBindsAuthor(*alias, existing):
			// The alias is not evidence that this Calibre credit is the same
			// person, so it must not decide the author's identity (#1684).
			// Fall through to creating the real author; the row itself stays
			// put because indexer search still expands on it.
			slog.Info("calibre import: ignoring untrusted author alias",
				"name", name, "aliasAuthor", existing.Name, "aliasAuthorID", existing.ID)
		default:
			i.recordAuthorBeforeSnapshot(ctx, runID, externalID, existing, outcomeLinked, map[string]any{"matchedBy": "alias"})
			if err := i.authors.UpsertAuthorIdentifier(ctx, existing.ID, foreignID); err != nil {
				return nil, false, err
			}
			i.upsertProvenance(ctx, runID, entityTypeAuthor, externalID, existing.ID)
			i.recordAuthorAfterSnapshot(ctx, runID, externalID, existing.ID, outcomeLinked, map[string]any{"matchedBy": "alias"})
			return existing, false, nil
		}
	}

	author := &models.Author{
		ForeignID: foreignID,
		Name:      name,
		SortName:  firstNonEmpty(ca.Sort, sortNameFromFull(name)),
		Monitored: true,
		// Import-created authors start with a partial catalogue; a later
		// refresh/relink discovering the full back-catalogue must not
		// mass-monitor it (issue #1348).
		MonitorNewItems:  models.AuthorMonitorNewItemsNone,
		MetadataProvider: "calibre",
	}
	// An import carries no per-author monitor choice, so honour the install-wide
	// default rather than letting the column default stamp "all" (#1666).
	db.ApplyAuthorMonitorDefaults(ctx, i.settings, author)
	if err := i.authors.Create(ctx, author); err != nil {
		return nil, false, err
	}
	i.noteAuthorCreated(author)
	// Provenance + creation marker so rollback can delete this author back
	// out. No before-snapshot — author did not exist prior to this run.
	i.upsertProvenance(ctx, runID, entityTypeAuthor, externalID, author.ID)
	i.recordCreateSnapshot(ctx, runID, entityTypeAuthor, externalID, author.ID)
	return author, true, nil
}

// findAuthorByName returns the author whose name identifies the same person as
// name, or nil. AuthorRepo has no "list by name" helper so matching runs
// in-memory against a per-run authorIndex, loaded with one List() per import
// (#1469). The variant tier is still a linear pass over that slice.
//
// Matching is tiered: exact lowercase first (so an unambiguous byte match keeps
// winning), then exact-via-variants from textutil, which is NFC/NFD safe and
// understands dotted initials, last-first inversion and transliterated
// diacritics.
//
// This was the only author matching site in the tree that skipped textutil
// entirely — a raw `strings.ToLower(a) == strings.ToLower(b)` (#1647). ABS has
// a three-tier matcher and the API normalizes; Calibre byte-compared. Calibre's
// metadata.db holds whatever wrote it, and Calibre desktop on macOS emits NFD,
// so "Jörg Müller" imported from Calibre could not find the "Jörg Müller"
// already created by ABS or OpenLibrary. The alias fallback in resolveAuthor
// does not save it either: AuthorAliasRepo.LookupByName uses SQLite
// `LOWER(name) = LOWER(?)`, which folds ASCII only. The result was a duplicate
// author row on first import, recreated on every later import, with the books
// split across two author pages.
//
// Deliberately Exact-only: fuzzy tiers belong in the interactive paths, not in
// a bulk import that can silently merge hundreds of books under one author.
func (i *Importer) findAuthorByName(ctx context.Context, name string) (*models.Author, error) {
	idx := i.authorIdx
	if idx == nil {
		// Not inside a run (or a direct caller): a throwaway index, one List.
		idx = &authorIndex{}
	}
	if err := idx.load(ctx, i.listAuthors); err != nil {
		return nil, err
	}
	return idx.find(name), nil
}

// authorIndex is the per-run view of the authors table that findAuthorByName
// matches against, so a run loads it once instead of once per imported book
// (#1469). It is only touched from the single import goroutine.
type authorIndex struct {
	loaded bool
	all    []models.Author
	// byName maps the lowercased, trimmed name to the first author in List
	// order, which is what the old linear scan returned for the exact tier.
	byName map[string]int
}

func (x *authorIndex) load(ctx context.Context, list func(context.Context) ([]models.Author, error)) error {
	if x.loaded {
		return nil
	}
	all, err := list(ctx)
	if err != nil {
		return err
	}
	x.all = all
	x.byName = make(map[string]int, len(all))
	for idx := range all {
		x.addName(idx)
	}
	x.loaded = true
	return nil
}

func (x *authorIndex) addName(idx int) {
	key := strings.ToLower(strings.TrimSpace(x.all[idx].Name))
	if _, ok := x.byName[key]; !ok {
		x.byName[key] = idx
	}
}

func (x *authorIndex) find(name string) *models.Author {
	lower := strings.ToLower(strings.TrimSpace(name))
	if idx, ok := x.byName[lower]; ok {
		return &x.all[idx]
	}
	// Two distinct rows can both match via variants (a library already holding
	// both "J. R. R. Tolkien" and "Tolkien, J.R.R." as separate authors). Take
	// the lowest ID so repeated imports converge on one row instead of
	// alternating, and say so rather than merging silently.
	var best *models.Author
	for idx := range x.all {
		if textutil.MatchAuthorName(name, x.all[idx].Name).Kind != textutil.AuthorMatchExact {
			continue
		}
		if best == nil {
			best = &x.all[idx]
			continue
		}
		slog.Warn("calibre import: author name matches more than one existing author",
			"name", name, "chosen", best.Name, "chosen_id", best.ID, "also", x.all[idx].Name, "also_id", x.all[idx].ID)
		if x.all[idx].ID < best.ID {
			best = &x.all[idx]
		}
	}
	return best
}

// noteAuthorCreated adds an author this run just created to the index, so a
// later book credited to the same name finds it without reloading the table.
func (i *Importer) noteAuthorCreated(a *models.Author) {
	x := i.authorIdx
	if x == nil || !x.loaded {
		return
	}
	x.all = append(x.all, *a)
	x.addName(len(x.all) - 1)
}

// aliasBindsAuthor reports whether an author_aliases row is strong enough
// evidence to file an incoming Calibre author credit under the aliased
// author instead of creating that author in their own right.
//
// It exists because an alias table mixes two very different kinds of row.
// Rows carrying a source id came from an author merge or a provider record:
// somebody or something asserted "these are the same human", so they bind.
// Rows with no source id are unattributed, and historically that included
// every co-author the Calibre importer minted (#1684), so they only bind when
// the names themselves say the same person, which is what an alias table is
// for.
//
// The unattributed shapes that still bind are the two grounds in
// textutil.LatinAliasBinds, shared with the write path and the search path so
// the copies cannot drift apart again (#2419): a latin-script alias on a
// non-latin canonical name (what the add-author flow's saveAlternateNames
// writes, so "Murakami" on a release can reach "村上春樹"), and an alias that is
// the same name spelled differently ("Jo Nesbo" for "Jo Nesbø").
//
// The AuthorMatchExact half of that second ground overlaps the tier check
// below, which is harmless: this site keeps its own check because it also
// admits AuthorMatchFuzzyAuto, which the shared rule deliberately does not.
//
// This mirrors the ABS importer's trustedAuthorAlias, which the Calibre path
// never had.
func aliasBindsAuthor(alias models.AuthorAlias, canonical *models.Author) bool {
	if canonical == nil {
		return false
	}
	if strings.TrimSpace(alias.SourceOLID) != "" {
		return true
	}
	// FuzzyAuto and not just Exact: punctuation and spacing variants of one
	// name are the whole point of an alias table, and an exact-only test would
	// reject "RR Haywood" against "R.R. Haywood". Including it costs nothing
	// here because the tier covers near-typos of the same name, and a
	// co-author's name is not a near-typo of the person they wrote with, which
	// is the shape this function has to refuse.
	if kind := textutil.MatchAuthorName(alias.Name, canonical.Name).Kind; kind == textutil.AuthorMatchExact || kind == textutil.AuthorMatchFuzzyAuto {
		return true
	}
	return textutil.LatinAliasBinds(canonical.Name, alias.Name)
}

// bookUpsertResult carries whether a book row was newly created and
// whether it was discovered via title-match (as opposed to calibre_id).
// matchedBy is breadcrumbed into the snapshot envelope so rollback previews
// can report how the original match happened.
type bookUpsertResult struct {
	row           *models.Book
	mergedByTitle bool
	matchedBy     string
}

// upsertBook ensures a Bindery books row exists for the given Calibre
// book. Dedupe order:
//  1. books.calibre_id == cb.CalibreID   (pure re-import; update in place)
//  2. books.foreign_id == "calibre:book:N" (recover from a crash between
//     Create and SetCalibreID — foreign_id was written, calibre_id was not)
//  3. author_id + title match            (existing Bindery book adopted
//     into Calibre; link + update)
//  4. Create a fresh row with the Calibre id set.
//
// Returns (result, created). result.mergedByTitle is true only for path 3.
func (i *Importer) upsertBook(ctx context.Context, runID int64, author *models.Author, cb CalibreBook) (*bookUpsertResult, bool, error) {
	externalID := calibreBookExternalID(cb.CalibreID)

	// Path 1 — exact calibre_id match.
	existing, err := i.books.GetByCalibreID(ctx, cb.CalibreID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		// Snapshot-before-mutation: must run before applyBookFields, not
		// after, or rollback gets the post-import state as the "before" and
		// becomes a no-op.
		i.recordBookBeforeSnapshot(ctx, runID, externalID, existing, outcomeUpdated, map[string]any{"matchedBy": "calibre_id"})
		if err := i.applyBookFields(ctx, existing, cb); err != nil {
			return nil, false, err
		}
		return &bookUpsertResult{row: existing, matchedBy: "calibre_id"}, false, nil
	}

	// Path 2 — foreign_id match: book was created in a previous run but
	// calibre_id was never persisted (e.g. crash between Create and SetCalibreID).
	fid := "calibre:book:" + strconv.FormatInt(cb.CalibreID, 10)
	if existing, err := i.books.GetByForeignID(ctx, fid); err != nil {
		return nil, false, err
	} else if existing != nil {
		i.recordBookBeforeSnapshot(ctx, runID, externalID, existing, outcomeUpdated, map[string]any{"matchedBy": "foreign_id"})
		if existing.CalibreID == nil {
			if err := i.books.SetCalibreID(ctx, existing.ID, cb.CalibreID); err != nil {
				return nil, false, err
			}
			id := cb.CalibreID
			existing.CalibreID = &id
		}
		if err := i.applyBookFields(ctx, existing, cb); err != nil {
			return nil, false, err
		}
		return &bookUpsertResult{row: existing, matchedBy: "foreign_id"}, false, nil
	}

	// Path 3 — same author + canonical dedup key. Binds to a book the user (or
	// a previous ABS/Calibre/CWA ingest) already filed for the same work even
	// when the stored title differs by subtitle, case, bracketed qualifier, or
	// umlaut form. Previously this matched on raw
	// LOWER(title) SQL, which disagreed with the ABS importer's normalized
	// match and produced duplicate rows (#940).
	//
	// A candidate whose calibre_id belongs to another book that is still in
	// this library is a different Calibre entry (path 1 would have matched our
	// id otherwise), so binding would overwrite its identity and collapse two
	// library books into one (#3631). Those rows are skipped. A calibre_id that
	// is no longer in the library is stale (book removed and re-added, or the
	// library renumbered) and is rebound as before. Candidates are tried in id
	// order so the choice does not depend on the query plan; with none left a
	// fresh row is created below.
	//
	// Known residual: an unbound row with the same dedup key (for example one a
	// metadata provider created) is still adopted, as it always was.
	candidates, err := i.books.FindAllByAuthorAndDedupKey(ctx, author.ID, cb.Title)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(candidates, func(a, b int) bool { return candidates[a].ID < candidates[b].ID })
	for idx := range candidates {
		existing := &candidates[idx]
		if existing.CalibreID != nil {
			if _, live := i.liveCalibreIDs[*existing.CalibreID]; live {
				continue
			}
		}
		i.recordBookBeforeSnapshot(ctx, runID, externalID, existing, outcomeLinked, map[string]any{"matchedBy": "title"})
		if err := i.books.SetCalibreID(ctx, existing.ID, cb.CalibreID); err != nil {
			return nil, false, err
		}
		id := cb.CalibreID
		existing.CalibreID = &id
		if err := i.applyBookFields(ctx, existing, cb); err != nil {
			return nil, false, err
		}
		return &bookUpsertResult{row: existing, mergedByTitle: true, matchedBy: "title"}, false, nil
	}

	// Path 4 — create fresh.
	id := cb.CalibreID
	book := &models.Book{
		ForeignID:        "calibre:book:" + strconv.FormatInt(cb.CalibreID, 10),
		AuthorID:         author.ID,
		OwnerUserID:      author.OwnerUserID, // tenancy (#1457): inherit author's owner
		Title:            cb.Title,
		SortTitle:        firstNonEmpty(cb.SortTitle, cb.Title),
		ReleaseDate:      cb.PublishDate,
		Language:         cb.Language,
		Monitored:        true,
		Status:           models.BookStatusImported,
		AnyEditionOK:     true,
		MediaType:        models.MediaTypeEbook,
		MetadataProvider: "calibre",
		CalibreID:        &id,
	}
	if err := i.books.Create(ctx, book); err != nil {
		return nil, false, err
	}
	// Create does not write calibre_id directly — persist it separately.
	if err := i.books.SetCalibreID(ctx, book.ID, cb.CalibreID); err != nil {
		return nil, false, err
	}
	return &bookUpsertResult{row: book, matchedBy: "created"}, true, nil
}

// applyBookFields updates a pre-existing book row with fresh data from
// Calibre. We refresh the scope of columns the Calibre library actually
// governs (title, sort, release date, status, file path to the first
// format) and leave anything outside that scope intact so user-curated
// metadata is preserved.
func (i *Importer) applyBookFields(ctx context.Context, book *models.Book, cb CalibreBook) error {
	// Locked fields (#1237): a manual edit survives Calibre re-imports.
	if !book.IsFieldLocked(models.BookFieldTitle) {
		book.Title = cb.Title
		book.SortTitle = firstNonEmpty(cb.SortTitle, cb.Title)
	}
	if cb.PublishDate != nil && !book.IsFieldLocked(models.BookFieldReleaseDate) {
		book.ReleaseDate = cb.PublishDate
	}
	if path := firstTrackedFormatPath(cb); path != "" {
		book.FilePath = path
		book.Status = models.BookStatusImported
	}
	if cb.Language != "" && book.Language == "" && !book.IsFieldLocked(models.BookFieldLanguage) {
		book.Language = cb.Language
	}
	if book.MetadataProvider == "" {
		book.MetadataProvider = "calibre"
	}
	return i.books.Update(ctx, book)
}

// firstTrackedFormatPath is the path of cb's first format the import tracks
// as a book file, or "" when none qualifies. The legacy books.file_path
// column is what OPDS and the file download serve, so it must never be
// pointed at a format registerBookFiles skips: Formats[0] is often the
// ORIGINAL_EPUB backup of a polished book (#3630).
func firstTrackedFormatPath(cb CalibreBook) string {
	for _, f := range cb.Formats {
		path := strings.TrimSpace(f.AbsolutePath)
		if path != "" && models.IsCalibreBookFilePath(path) {
			return path
		}
	}
	return ""
}

// storeCover copies cb's cover.jpg into the covers store and returns the
// bindery-cover: reference, or "" when the book has no cover, no store is
// wired, or the file is not a usable image. Never returns the library path:
// before #2564 that is exactly what landed in editions.image_url, and it
// rendered as the SPA shell.
func (i *Importer) storeCover(cb CalibreBook) string {
	if i.covers == nil || strings.TrimSpace(cb.CoverPath) == "" {
		return ""
	}
	ref, err := i.covers.Put(cb.CoverPath)
	if err != nil {
		slog.Debug("calibre import: cover not stored", "calibre_id", cb.CalibreID, "path", cb.CoverPath, "error", err)
		return ""
	}
	return ref
}

// applyBookCover sets book.image_url to coverRef when the book has no cover
// a metadata provider could have supplied: an empty value, a stale host path,
// or an earlier stored reference (the library's cover may have changed). A
// provider URL is left alone; Refresh Metadata owns that field once it is
// populated, and the Calibre cover is only ever the fallback.
func (i *Importer) applyBookCover(ctx context.Context, book *models.Book, coverRef string) {
	if book == nil || coverRef == "" || book.ImageURL == coverRef {
		return
	}
	current := strings.TrimSpace(book.ImageURL)
	if current != "" && !filepath.IsAbs(current) && !covers.IsRef(current) {
		return
	}
	if err := i.books.SetImageURL(ctx, book.ID, coverRef); err != nil {
		slog.Warn("calibre import: book cover not set", "book_id", book.ID, "error", err)
		return
	}
	book.ImageURL = coverRef
}

// upsertEdition upserts one Bindery edition for a single Calibre format.
// Returns (added, edition, err) where added is true only when a brand-new
// row was created; the returned edition is the resulting row so caller can
// snapshot it. coverRef is the stored-cover reference for the book (see
// storeCover), shared by every format.
func (i *Importer) upsertEdition(ctx context.Context, runID int64, book *models.Book, cb CalibreBook, f CalibreFormat, coverRef string) (bool, *models.Edition, error) {
	if f.Format == "" {
		return false, nil, nil
	}
	foreignID := fmt.Sprintf("calibre:edition:%d:%s", cb.CalibreID, strings.ToUpper(f.Format))
	prior, err := i.editions.GetByForeignID(ctx, foreignID)
	if err != nil {
		return false, nil, err
	}
	if prior != nil {
		// Before-snapshot for an updated edition: rollback will skip these
		// (no field-level edition restore today) but we still want a row in
		// snapshots so list-by-run reports the touch.
		externalID := calibreEditionExternalID(cb.CalibreID, f.Format)
		i.recordEditionBeforeSnapshot(ctx, runID, externalID, prior, outcomeUpdated, nil)
	}

	isbn13 := ptrStringIfNonEmpty(cb.ISBN)
	lang := cb.Language
	if lang == "" {
		lang = "eng" // fallback when Calibre library has no language set
	}
	e := &models.Edition{
		ForeignID:   foreignID,
		BookID:      book.ID,
		Title:       cb.Title,
		ISBN13:      isbn13,
		Publisher:   "",
		PublishDate: cb.PublishDate,
		Format:      strings.ToUpper(f.Format),
		Language:    lang,
		ImageURL:    coverRef,
		IsEbook:     true,
		Monitored:   true,
	}
	if err := i.editions.Upsert(ctx, e); err != nil {
		return false, nil, err
	}
	// Upsert keeps the existing image_url when the new one is empty, which
	// is right for a provider cover but wrong for the host path an older
	// importer stored: that value can never be served, so clear it rather
	// than let it outlive the fix.
	if coverRef == "" && prior != nil && filepath.IsAbs(prior.ImageURL) {
		if err := i.editions.SetImageURL(ctx, e.ID, ""); err != nil {
			return false, nil, err
		}
	}
	// Re-fetch to get the assigned ID (Upsert may have created or updated).
	stored, lookupErr := i.editions.GetByForeignID(ctx, foreignID)
	if lookupErr != nil {
		return prior == nil, nil, lookupErr
	}
	return prior == nil, stored, nil
}

// RunSync implements scheduler.CalibreSyncer. It reads the library path from
// the settings table and runs a full import; errors and progress are logged
// and stored on the importer's progress state so the UI can surface them
// via the existing /calibre/import/status polling endpoint.
//
// RunSync is intentionally fire-and-forget from the scheduler's perspective:
// if Calibre is unconfigured or already running, it logs and returns without
// blocking the job loop.
func (i *Importer) RunSync(ctx context.Context) {
	// Opt-in gate (calibre.library_import_enabled): the scheduled sync must not
	// run when the operator has library import turned off, even if a library
	// path is still configured. Existing installs are backfilled to enabled by
	// migration 070 so this never disables a working import.
	if s, _ := i.settings.Get(ctx, "calibre.library_import_enabled"); s == nil || !strings.EqualFold(s.Value, "true") {
		slog.Debug("calibre scheduler sync: library import disabled — skipping")
		return
	}

	libraryPath := ""
	if s, _ := i.settings.Get(ctx, "calibre.library_path"); s != nil {
		libraryPath = s.Value
	}
	if libraryPath == "" {
		slog.Debug("calibre scheduler sync: library_path not set — skipping")
		return
	}

	if _, err := i.Run(ctx, libraryPath); err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			slog.Debug("calibre scheduler sync: already running — skipping")
		} else {
			slog.Warn("calibre scheduler sync failed", "error", err)
		}
	}
}

func (i *Importer) fail(err error) {
	slog.Error("calibre import failed", "error", err)
	i.setProgress(func(p *ImportProgress) {
		p.Error = err.Error()
		p.Message = "failed"
	})
}

func (i *Importer) setProgress(mutate func(*ImportProgress)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	mutate(&i.progress)
}

// firstNonEmpty returns the first non-blank string from its args, or "".
func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// sortNameFromFull converts "First Last" → "Last, First" as a last-resort
// sort key when Calibre doesn't supply one. Delegates to textutil.SortName so
// this file cannot drift from every other sort-name producer (#2363).
func sortNameFromFull(name string) string {
	return textutil.SortName(name)
}

func ptrStringIfNonEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
