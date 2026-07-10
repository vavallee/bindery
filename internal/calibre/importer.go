package calibre

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
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
	SeriesFailures   int `json:"seriesFailures,omitempty"`
	Skipped          int `json:"skipped"`
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

	openReader func(libraryPath string) (readerIface, error)

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
		authors:  authors,
		aliases:  aliases,
		books:    books,
		editions: editions,
		settings: settings,
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
	i.setProgress(func(p *ImportProgress) {
		p.Total = total
		p.Message = fmt.Sprintf("scanning %d books…", total)
	})

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
		"skipped", stats.Skipped)
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
	i.recordSecondaryAuthors(ctx, author.ID, cb.Authors[1:], stats)

	book, newBook, err := i.upsertBook(ctx, runID, author, cb)
	if err != nil {
		slog.Warn("calibre import: book upsert failed", "calibre_id", cb.CalibreID, "error", err)
		stats.Skipped++
		return
	}
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
		added, edition, err := i.upsertEdition(ctx, runID, book.row, cb, f)
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

	// Series persistence (#905). Calibre's books_series_link plus series
	// table is read by the cursor as cb.Series; we propagate the membership
	// into Bindery's series + series_books shape so the Wanted / detail /
	// Hardcover-enhanced flows have the data to attach to. Skipped when no
	// series repo is wired (test fixtures) or no series was on the row.
	if i.series != nil && cb.Series != nil && cb.Series.Name != "" {
		i.attachBookToSeries(ctx, runID, book.row, cb.Series, stats)
	}
}

// attachBookToSeries upserts the Bindery series row for cb.Series and links
// the book at the recorded position. The series row uses a synthetic
// foreign ID of "calibre:series:<name>" so subsequent reruns find and reuse
// it (CreateOrGet semantics) and any later metadata-provider sync can
// promote the foreign ID to a real one without losing the link. Errors are
// logged at WARN and counted as skipped on the series side, but never abort
// the book import: a series-link failure is strictly less damaging than
// losing the book itself.
func (i *Importer) attachBookToSeries(ctx context.Context, runID int64, book *models.Book, cs *CalibreSeries, stats *ImportStats) {
	series := &models.Series{
		ForeignID: calibreSeriesForeignID(cs.Name),
		Title:     cs.Name,
	}
	if err := i.series.CreateOrGet(ctx, series); err != nil {
		slog.Warn("calibre import: series upsert failed", "name", cs.Name, "error", err)
		stats.SeriesFailures++
		return
	}
	position := ""
	if cs.Position > 0 {
		position = strconv.FormatFloat(cs.Position, 'f', -1, 64)
	}
	if err := i.series.UpsertBookLink(ctx, series.ID, book.ID, position, true); err != nil {
		slog.Warn("calibre import: series link failed", "name", cs.Name, "book_id", book.ID, "error", err)
		stats.SeriesFailures++
		return
	}
	if runID != 0 {
		// Record provenance so a rollback can unlink the book without
		// orphaning the series. Series row deletion is best-effort: shared
		// series should survive a single-book rollback, so we only record
		// the link, not the series create.
		i.upsertProvenance(ctx, runID, entityTypeSeriesLink,
			calibreSeriesLinkExternalID(book.ID, series.ID), book.ID)
	}
	stats.SeriesLinked++
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
	return "calibre:series-link:" + strconv.FormatInt(bookID, 10) + ":" + strconv.FormatInt(seriesID, 10)
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
//  2. Alias match via author_aliases.name
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

	if aliasID, err := i.aliases.LookupByName(ctx, name); err != nil {
		return nil, false, err
	} else if aliasID != nil {
		existing, err := i.authors.GetByID(ctx, *aliasID)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
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
	if err := i.authors.Create(ctx, author); err != nil {
		return nil, false, err
	}
	// Provenance + creation marker so rollback can delete this author back
	// out. No before-snapshot — author did not exist prior to this run.
	i.upsertProvenance(ctx, runID, entityTypeAuthor, externalID, author.ID)
	i.recordCreateSnapshot(ctx, runID, entityTypeAuthor, externalID, author.ID)
	return author, true, nil
}

// findAuthorByName returns the first author whose name matches
// case-insensitively, or nil. AuthorRepo has no "list by name" helper so
// we scan in-memory — acceptable because resolveAuthor runs once per
// Calibre book and List() is already O(authors) per call; for a typical
// library the author count is a few hundred.
//
// TODO(post-v0.8.1): add an indexed lookup to AuthorRepo if library sizes
// grow beyond a few thousand authors.
func (i *Importer) findAuthorByName(ctx context.Context, name string) (*models.Author, error) {
	all, err := i.authors.List(ctx)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(name)
	for idx := range all {
		if strings.ToLower(all[idx].Name) == lower {
			return &all[idx], nil
		}
	}
	return nil, nil
}

// recordSecondaryAuthors adds every co-author after the first to the
// canonical author's alias list, so future imports that see the same
// co-author-as-primary resolve back to the same Bindery row.
func (i *Importer) recordSecondaryAuthors(ctx context.Context, canonicalID int64, extras []CalibreAuthor, _ *ImportStats) {
	for _, ca := range extras {
		name := strings.TrimSpace(ca.Name)
		if name == "" {
			continue
		}
		if err := i.aliases.Create(ctx, &models.AuthorAlias{AuthorID: canonicalID, Name: name}); err != nil {
			// Non-fatal: an alias can collide with a real author. Log and
			// move on — the primary ingest already succeeded.
			slog.Debug("calibre import: alias record skipped", "name", name, "error", err)
		}
	}
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
	if existing, err := i.books.FindByAuthorAndDedupKey(ctx, author.ID, cb.Title); err != nil {
		return nil, false, err
	} else if existing != nil {
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
	if len(cb.Formats) > 0 && cb.Formats[0].AbsolutePath != "" {
		book.FilePath = cb.Formats[0].AbsolutePath
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

// upsertEdition upserts one Bindery edition for a single Calibre format.
// Returns (added, edition, err) where added is true only when a brand-new
// row was created; the returned edition is the resulting row so caller can
// snapshot it.
func (i *Importer) upsertEdition(ctx context.Context, runID int64, book *models.Book, cb CalibreBook, f CalibreFormat) (bool, *models.Edition, error) {
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
		ImageURL:    cb.CoverPath,
		IsEbook:     true,
		Monitored:   true,
	}
	if err := i.editions.Upsert(ctx, e); err != nil {
		return false, nil, err
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
// sort key when Calibre doesn't supply one. Matches the helper used
// elsewhere in the API layer (api.sortName).
func sortNameFromFull(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return name
	}
	last := fields[len(fields)-1]
	rest := strings.Join(fields[:len(fields)-1], " ")
	return last + ", " + rest
}

func ptrStringIfNonEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
