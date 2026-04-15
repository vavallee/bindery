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
	Skipped          int `json:"skipped"`
}

// ImportProgress is the live poll shape for GET /calibre/import/status.
// The UI uses Running to decide whether to render a progress bar, Total
// + Processed to fill it, and Stats (populated on completion) for the
// summary panel.
type ImportProgress struct {
	Running   bool      `json:"running"`
	StartedAt time.Time `json:"startedAt"`
	Total     int       `json:"total"`
	Processed int       `json:"processed"`
	Message   string    `json:"message,omitempty"`
	Error     string    `json:"error,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
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
	defer func() {
		now := time.Now().UTC()
		i.mu.Lock()
		i.progress.Running = false
		i.progress.FinishedAt = &now
		i.progress.Stats = stats
		i.running = false
		i.mu.Unlock()
	}()

	reader, err := i.openReader(libraryPath)
	if err != nil {
		i.fail(err)
		return stats
	}
	defer reader.Close()

	total, err := reader.Count(ctx)
	if err != nil {
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
		i.importOne(ctx, cb, stats)
		i.setProgress(func(p *ImportProgress) { p.Processed++ })
		return nil
	})
	if err != nil {
		i.fail(err)
		return stats
	}

	if err := i.settings.Set(ctx, "calibre.last_import_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Warn("calibre import: persist last_import_at failed", "error", err)
	}
	slog.Info("calibre import complete",
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
func (i *Importer) importOne(ctx context.Context, cb CalibreBook, stats *ImportStats) {
	if len(cb.Authors) == 0 {
		slog.Warn("calibre import: book has no authors", "calibre_id", cb.CalibreID, "title", cb.Title)
		stats.Skipped++
		return
	}

	author, created, err := i.resolveAuthor(ctx, cb.Authors[0])
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

	book, newBook, err := i.upsertBook(ctx, author, cb)
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

	for _, f := range cb.Formats {
		added, err := i.upsertEdition(ctx, book.row, cb, f)
		if err != nil {
			slog.Warn("calibre import: edition upsert failed",
				"calibre_id", cb.CalibreID, "format", f.Format, "error", err)
			continue
		}
		if added {
			stats.EditionsAdded++
		}
	}
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
func (i *Importer) resolveAuthor(ctx context.Context, ca CalibreAuthor) (*models.Author, bool, error) {
	name := strings.TrimSpace(ca.Name)
	if name == "" {
		return nil, false, errors.New("empty author name")
	}

	if existing, err := i.findAuthorByName(ctx, name); err != nil {
		return nil, false, err
	} else if existing != nil {
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
			return existing, false, nil
		}
	}

	author := &models.Author{
		ForeignID:        "calibre:author:" + strconv.FormatInt(ca.CalibreID, 10),
		Name:             name,
		SortName:         firstNonEmpty(ca.Sort, sortNameFromFull(name)),
		Monitored:        true,
		MetadataProvider: "calibre",
	}
	if err := i.authors.Create(ctx, author); err != nil {
		return nil, false, err
	}
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
type bookUpsertResult struct {
	row           *models.Book
	mergedByTitle bool
}

// upsertBook ensures a Bindery books row exists for the given Calibre
// book. Dedupe order:
//  1. books.calibre_id == cb.CalibreID   (pure re-import; update in place)
//  2. author_id + title match            (existing Bindery book adopted
//                                          into Calibre; link + update)
//  3. Create a fresh row with the Calibre id set.
//
// Returns (result, created). result.mergedByTitle is true only for path 2.
func (i *Importer) upsertBook(ctx context.Context, author *models.Author, cb CalibreBook) (*bookUpsertResult, bool, error) {
	// Path 1 — exact calibre_id match.
	existing, err := i.books.GetByCalibreID(ctx, cb.CalibreID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if err := i.applyBookFields(ctx, existing, cb); err != nil {
			return nil, false, err
		}
		return &bookUpsertResult{row: existing}, false, nil
	}

	// Path 2 — same author + title.
	if existing, err := i.books.FindByAuthorAndTitle(ctx, author.ID, cb.Title); err != nil {
		return nil, false, err
	} else if existing != nil {
		if err := i.books.SetCalibreID(ctx, existing.ID, cb.CalibreID); err != nil {
			return nil, false, err
		}
		id := cb.CalibreID
		existing.CalibreID = &id
		if err := i.applyBookFields(ctx, existing, cb); err != nil {
			return nil, false, err
		}
		return &bookUpsertResult{row: existing, mergedByTitle: true}, false, nil
	}

	// Path 3 — create fresh.
	id := cb.CalibreID
	book := &models.Book{
		ForeignID:        "calibre:book:" + strconv.FormatInt(cb.CalibreID, 10),
		AuthorID:         author.ID,
		Title:            cb.Title,
		SortTitle:        firstNonEmpty(cb.SortTitle, cb.Title),
		ReleaseDate:      cb.PublishDate,
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
	return &bookUpsertResult{row: book}, true, nil
}

// applyBookFields updates a pre-existing book row with fresh data from
// Calibre. We refresh the scope of columns the Calibre library actually
// governs (title, sort, release date, status, file path to the first
// format) and leave anything outside that scope intact so user-curated
// metadata is preserved.
func (i *Importer) applyBookFields(ctx context.Context, book *models.Book, cb CalibreBook) error {
	book.Title = cb.Title
	book.SortTitle = firstNonEmpty(cb.SortTitle, cb.Title)
	if cb.PublishDate != nil {
		book.ReleaseDate = cb.PublishDate
	}
	if len(cb.Formats) > 0 && cb.Formats[0].AbsolutePath != "" {
		book.FilePath = cb.Formats[0].AbsolutePath
		book.Status = models.BookStatusImported
	}
	if book.MetadataProvider == "" {
		book.MetadataProvider = "calibre"
	}
	return i.books.Update(ctx, book)
}

// upsertEdition upserts one Bindery edition for a single Calibre format.
// Returns (added, err) where added is true only when a brand-new row was
// created.
func (i *Importer) upsertEdition(ctx context.Context, book *models.Book, cb CalibreBook, f CalibreFormat) (bool, error) {
	if f.Format == "" {
		return false, nil
	}
	foreignID := fmt.Sprintf("calibre:edition:%d:%s", cb.CalibreID, strings.ToUpper(f.Format))
	prior, err := i.editions.GetByForeignID(ctx, foreignID)
	if err != nil {
		return false, err
	}

	isbn13 := ptrStringIfNonEmpty(cb.ISBN)
	e := &models.Edition{
		ForeignID:   foreignID,
		BookID:      book.ID,
		Title:       cb.Title,
		ISBN13:      isbn13,
		Publisher:   "",
		PublishDate: cb.PublishDate,
		Format:      strings.ToUpper(f.Format),
		Language:    "eng",
		ImageURL:    cb.CoverPath,
		IsEbook:     true,
		Monitored:   true,
	}
	if err := i.editions.Upsert(ctx, e); err != nil {
		return false, err
	}
	return prior == nil, nil
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
