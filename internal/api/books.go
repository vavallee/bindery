package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type BookHandler struct {
	books     *db.BookRepo
	meta      *metadata.Aggregator
	lookup    BookMetaLookup // used by Rebind; defaults to meta when non-nil
	history   *db.HistoryRepo
	searcher  BookSearcher
	settings  *db.SettingsRepo
	downloads *db.DownloadRepo
	authors   *db.AuthorRepo
	series    *db.SeriesRepo
	editions  *db.EditionRepo
	roots     *LibraryRoots // optional: library-root containment for delete

	editionFetcher bookhydrate.EditionFetcher

	// lifetimeCtx is the process-lifecycle context, cancelled on server
	// shutdown so the auto-grab goroutine spawned by Update (when status
	// flips to wanted) does not leak past Server.Shutdown. Falls back to
	// context.Background() when not set; mirrors BulkHandler / AuthorHandler
	// (see #846).
	lifetimeCtx context.Context
}

// WithLifetimeCtx attaches the process-lifecycle context so the status-flip
// auto-grab goroutine cancels on shutdown. A nil ctx is tolerated and ignored.
func (h *BookHandler) WithLifetimeCtx(ctx context.Context) *BookHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
// Centralised so spawn sites can swap out context.WithoutCancel(r.Context())
// for a shutdown-aware ctx without each site rewriting the fallback rule.
func (h *BookHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

func NewBookHandler(books *db.BookRepo, meta *metadata.Aggregator, history *db.HistoryRepo, searcher BookSearcher) *BookHandler {
	h := &BookHandler{books: books, meta: meta, history: history, searcher: searcher}
	if meta != nil {
		h.lookup = meta
	}
	return h
}

// WithMetaLookup overrides the BookMetaLookup used by Rebind. Useful in tests
// to inject a stub without a real HTTP client.
func (h *BookHandler) WithMetaLookup(l BookMetaLookup) *BookHandler {
	h.lookup = l
	return h
}

// WithSettings wires in the settings repo so the book handler can consult the
// global autoGrab.enabled kill-switch.
func (h *BookHandler) WithSettings(settings *db.SettingsRepo) *BookHandler {
	h.settings = settings
	return h
}

// WithDownloads wires in the download repo so the book handler can clean up
// download records when a book is deleted with ?deleteFiles=true.
func (h *BookHandler) WithDownloads(d *db.DownloadRepo) *BookHandler {
	h.downloads = d
	return h
}

// WithAuthors wires in the author repo so Rebind can detect author mismatches.
func (h *BookHandler) WithAuthors(a *db.AuthorRepo) *BookHandler {
	h.authors = a
	return h
}

// WithSeries wires in the series repo so Rebind can re-link series membership.
func (h *BookHandler) WithSeries(s *db.SeriesRepo) *BookHandler {
	h.series = s
	return h
}

// WithEditionHydration wires edition persistence for confident Hardcover
// metadata identities.
func (h *BookHandler) WithEditionHydration(editions *db.EditionRepo) *BookHandler {
	h.editions = editions
	return h
}

// WithRoots wires the library-root containment checker used by the delete
// handlers to refuse on-disk removal of paths outside the configured library.
// A nil value disables the check (the default; preserves legacy test wiring).
func (h *BookHandler) WithRoots(r *LibraryRoots) *BookHandler {
	h.roots = r
	return h
}

// WithEditionFetcher overrides the edition fetcher used by tests.
func (h *BookHandler) WithEditionFetcher(fetcher bookhydrate.EditionFetcher) *BookHandler {
	h.editionFetcher = fetcher
	return h
}

func (h *BookHandler) hydrateHardcoverEditions(ctx context.Context, book *models.Book, provider string) {
	if book == nil || h.editions == nil {
		return
	}
	providerName := firstNonEmpty(provider, book.MetadataProvider)
	fetcher := h.editionFetcher
	if fetcher == nil && h.meta != nil {
		fetcher = func(ctx context.Context, foreignID string) ([]models.Edition, error) {
			if strings.EqualFold(strings.TrimSpace(providerName), "hardcover") {
				return h.meta.GetEditionsFromProvider(ctx, "hardcover", foreignID)
			}
			return h.meta.GetEditions(ctx, foreignID)
		}
	}
	bookhydrate.HydrateHardcoverEditions(ctx, bookhydrate.Options{
		Book:          book,
		Provider:      providerName,
		Editions:      h.editions,
		Books:         h.books,
		FetchEditions: fetcher,
		Enricher:      h.meta,
	})
}

// EnrichAudiobook fetches audnex data for the book's ASIN and updates
// narrator, duration, cover, and description on the record. Requires the
// book to be media_type=audiobook with an ASIN already set.
func (h *BookHandler) EnrichAudiobook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). 404 (not 403) on mismatch.
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	if book.MediaType != models.MediaTypeAudiobook && book.MediaType != models.MediaTypeBoth {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "book is not an audiobook"})
		return
	}
	if book.ASIN == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set ASIN before enriching"})
		return
	}
	if h.meta == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metadata provider unavailable"})
		return
	}
	if err := h.meta.EnrichAudiobook(r.Context(), book); err != nil {
		slog.Warn("audnex enrich failed", "bookId", book.ID, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	h.tryMapAudiobookMetadataByASIN(r.Context(), book)
	if err := h.books.Update(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cleanBookDescription(book)
	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) tryMapAudiobookMetadataByASIN(ctx context.Context, book *models.Book) {
	if h == nil || h.meta == nil || h.authors == nil || book == nil || strings.TrimSpace(book.ASIN) == "" {
		return
	}
	target, err := h.meta.GetCanonicalBookByASIN(ctx, book.ASIN)
	if err != nil {
		slog.Debug("asin metadata map skipped", "bookId", book.ID, "asin", book.ASIN, "error", err)
		return
	}
	if target == nil || strings.TrimSpace(target.ForeignID) == "" {
		return
	}
	if existing, err := h.books.GetByForeignID(ctx, target.ForeignID); err != nil {
		slog.Warn("asin metadata map conflict check failed", "bookId", book.ID, "foreignId", target.ForeignID, "error", err)
		return
	} else if existing != nil && existing.ID != book.ID {
		return
	}
	currentAuthor, err := h.authors.GetByID(ctx, book.AuthorID)
	if err != nil || currentAuthor == nil {
		if err != nil {
			slog.Warn("asin metadata map author lookup failed", "bookId", book.ID, "authorId", book.AuthorID, "error", err)
		}
		return
	}
	if !bookMapAuthorMatches(currentAuthor, target.Author) {
		return
	}
	fallbackDescription := book.Description
	fallbackImageURL := book.ImageURL
	preserveBookStateForMetadataMap(book, target)
	if book.Description == "" {
		book.Description = fallbackDescription
	}
	if book.ImageURL == "" {
		book.ImageURL = fallbackImageURL
	}
}

// bookListResponse is the paginated wrapper returned by List. Replaces the
// pre-Wave-2 bare `[]models.Book` shape; clients must unwrap `items` to get
// the rows. See PR #E for the breaking-change disclosure.
type bookListResponse struct {
	Items  []models.Book `json:"items"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// bookListDefaultLimit is the default page size for an unparameterised List
// request; bookListMaxLimit is the hard cap so a client cannot ask for
// limit=10_000_000 and OOM the server building one big JSON blob.
const (
	bookListDefaultLimit = 100
	bookListMaxLimit     = 500
)

func (h *BookHandler) List(w http.ResponseWriter, r *http.Request) {
	authorID := r.URL.Query().Get("authorId")
	status := r.URL.Query().Get("status")
	includeExcluded := r.URL.Query().Get("includeExcluded") == "true"
	limit, offset := parseLimitOffset(r, bookListDefaultLimit, bookListMaxLimit)

	var (
		books []models.Book
		total int
		err   error
	)

	switch {
	case authorID != "":
		// Filtered-by-author lists are bounded (max a few hundred per author)
		// so we paginate in memory rather than threading LIMIT/OFFSET through
		// every ListByAuthor* variant. The repo call still pulls one author's
		// catalogue; the slice is cheap.
		id, _ := strconv.ParseInt(authorID, 10, 64)
		if includeExcluded {
			books, err = h.books.ListByAuthorIncludingExcluded(r.Context(), id)
		} else {
			books, err = h.books.ListByAuthor(r.Context(), id)
		}
		books, total = pageBooks(books, limit, offset)
	case status != "":
		// Status-filtered lists (wanted/imported/etc.) can be large on a 50k
		// library but they back the dashboard widgets, which always fetch the
		// whole set today. Keep that behaviour for now and slice for the new
		// envelope; a follow-up can add a SQL-paginated ListByStatusPage if
		// the audit shows the wanted/imported pages dominate at scale.
		if includeExcluded {
			books, err = h.books.ListByStatusIncludingExcluded(r.Context(), status)
		} else {
			books, err = h.books.ListByStatus(r.Context(), status)
		}
		books, total = pageBooks(books, limit, offset)
	default:
		if includeExcluded {
			books, err = h.books.ListIncludingExcluded(r.Context())
			books, total = pageBooks(books, limit, offset)
		} else {
			// Default-path SQL pagination: the books list is the hottest
			// 50k-row scan in the app, so push LIMIT/OFFSET to SQLite where
			// idx_books_sort_title (migration 047) keeps the sort cheap.
			books, total, err = h.books.ListPage(r.Context(), 0, limit, offset)
		}
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if books == nil {
		books = []models.Book{}
	}
	for i := range books {
		cleanBookDescription(&books[i])
		proxyBookImages(&books[i])
	}
	writeJSON(w, http.StatusOK, bookListResponse{
		Items:  books,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// pageBooks slices a fully-loaded book list by (limit, offset) and returns
// the page along with the unsliced total. Used for the filtered List paths
// where the underlying repo method does not yet take a page argument.
func pageBooks(in []models.Book, limit, offset int) ([]models.Book, int) {
	total := len(in)
	if offset >= total {
		return []models.Book{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return in[offset:end], total
}

func (h *BookHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	book, err := h.books.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	proxyBookImages(book)
	cleanBookDescription(book)
	h.attachBookFiles(r.Context(), book)
	writeJSON(w, http.StatusOK, book)
}

func cleanBookDescription(book *models.Book) {
	if book != nil {
		book.Description = textutil.CleanDescription(book.Description)
	}
}

// attachBookFiles populates book.BookFiles from the book_files table.
// Called on single-book responses so the frontend can display all tracked files.
func (h *BookHandler) attachBookFiles(ctx context.Context, book *models.Book) {
	if files, err := h.books.ListFiles(ctx, book.ID); err == nil {
		book.BookFiles = files
	}
}

func (h *BookHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	oldStatus := book.Status

	// Note: file_path is deliberately NOT accepted here. It's set by the
	// importer once a grab lands, and letting clients write it arbitrarily
	// would let an API caller later trigger os.RemoveAll on that path via
	// DELETE /book/{id}?deleteFiles=true or DELETE /book/{id}/file.
	var req struct {
		Monitored *bool   `json:"monitored"`
		Status    *string `json:"status"`
		MediaType *string `json:"mediaType"`
		ASIN      *string `json:"asin"`
		Narrator  *string `json:"narrator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Monitored != nil {
		book.Monitored = *req.Monitored
	}
	if req.Status != nil {
		switch *req.Status {
		case models.BookStatusWanted, models.BookStatusDownloading,
			models.BookStatusDownloaded, models.BookStatusImported,
			models.BookStatusSkipped:
			book.Status = *req.Status
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be one of 'wanted', 'downloading', 'downloaded', 'imported', 'skipped'"})
			return
		}
	}
	if req.MediaType != nil {
		switch *req.MediaType {
		case models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
			book.MediaType = *req.MediaType
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook', 'audiobook', or 'both'"})
			return
		}
	}
	if req.ASIN != nil {
		book.ASIN = *req.ASIN
	}
	if req.Narrator != nil {
		book.Narrator = *req.Narrator
	}

	if err := h.books.Update(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Fire an immediate indexer search when a book transitions into wanted
	// status (e.g. "Delete file" flips imported → wanted, or a manual status
	// edit). Gate on searcher to keep tests that don't wire it nil-safe.
	// Detach the request context so the search outlives the HTTP response
	// but keeps any request-scoped values.
	if h.searcher != nil && req.Status != nil &&
		*req.Status == models.BookStatusWanted && oldStatus != models.BookStatusWanted {
		b := *book
		bgCtx := h.bgCtx()
		// Respect the global auto-grab kill-switch.
		autoGrabEnabled := true
		if h.settings != nil {
			if s, _ := h.settings.Get(bgCtx, "autoGrab.enabled"); s != nil && s.Value == "false" {
				autoGrabEnabled = false
			}
		}
		if autoGrabEnabled {
			go h.searcher.SearchAndGrabBook(bgCtx, b)
		}
	}

	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). Pre-fetch the book so the ownership
	// check can run before any destructive work; 404 on mismatch or missing
	// row so non-owners cannot probe for existence. The handler's existing
	// `?deleteFiles=true` branch re-fetches when it needs the legacy file
	// columns; the extra lookup is cheap and keeps the diff localised.
	if existing, err := h.books.GetByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if existing == nil || !auth.CheckOwnership(r.Context(), existing.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Opt-in `?deleteFiles=true` also removes every on-disk file tracked in
	// book_files before dropping the record. Each path is first run through
	// the library-root containment check (Wave 1 / Bundle B) so a tampered
	// or mis-imported `file_path` outside any configured root cannot redirect
	// the on-disk delete. Skipped paths are WARN-logged; the DB delete below
	// still proceeds — the row is going away regardless, and a loud refusal
	// is better than silently walking outside the library.
	if r.URL.Query().Get("deleteFiles") == "true" {
		files, _ := h.books.ListFiles(r.Context(), id)
		for _, f := range files {
			if _, err := safeRemoveBookPath(r.Context(), h.roots, f.Path, "", "id", id); err != nil {
				slog.Warn("book delete: failed to remove file", "id", id, "path", f.Path, "error", err)
			}
		}
		// Fallback for books imported before the book_files migration.
		if len(files) == 0 {
			if book, _ := h.books.GetByID(r.Context(), id); book != nil {
				for _, p := range []string{book.EbookFilePath, book.AudiobookFilePath, book.FilePath} {
					if p != "" {
						if _, err := safeRemoveBookPath(r.Context(), h.roots, p, "", "id", id); err != nil {
							slog.Warn("book delete: failed to remove legacy file", "id", id, "path", p, "error", err)
						}
					}
				}
			}
		}
		// Clean up any pending/completed download records for this book so the
		// queue does not show stale entries after a full book deletion.
		if h.downloads != nil {
			if err := h.downloads.DeleteByBook(r.Context(), id); err != nil {
				slog.Warn("book delete: failed to clean download records", "id", id, "error", err)
			}
		}
	}
	if err := h.books.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteFile removes the on-disk file(s) backing an imported book and
// flips the book's status back to `wanted` so it re-appears on the Wanted page.
//
// An optional `?format=ebook` or `?format=audiobook` query param scopes the
// deletion to one format; omitting it deletes all files for the book.
// Files are enumerated from book_files; the legacy single-path columns are
// checked as a fallback for books imported before the migration.
func (h *BookHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Tier-1 cross-user IDOR guard (D1). Fetch the book up-front so the
	// ownership check runs before any file enumeration or destructive work.
	if existing, err := h.books.GetByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if existing == nil || !auth.CheckOwnership(r.Context(), existing.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	format := r.URL.Query().Get("format") // optional: "ebook" | "audiobook"

	// Enumerate files from book_files for this book.
	allFiles, err := h.books.ListFiles(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Filter to the requested format(s).
	var toDelete []string
	for _, f := range allFiles {
		if format == "" || f.Format == format {
			toDelete = append(toDelete, f.Path)
		}
	}

	// Fallback for books imported before the book_files migration.
	if len(toDelete) == 0 {
		book, err := h.books.GetByID(r.Context(), id)
		if err != nil || book == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
			return
		}
		if format == "" || format == models.MediaTypeEbook {
			if book.EbookFilePath != "" { //nolint:staticcheck
				toDelete = append(toDelete, book.EbookFilePath) //nolint:staticcheck
			} else if book.FilePath != "" && format == models.MediaTypeEbook {
				toDelete = append(toDelete, book.FilePath)
			}
		}
		if format == "" || format == models.MediaTypeAudiobook {
			if book.AudiobookFilePath != "" { //nolint:staticcheck
				toDelete = append(toDelete, book.AudiobookFilePath) //nolint:staticcheck
			}
		}
		// Legacy single file_path (no format qualifier).
		if format == "" && len(toDelete) == 0 && book.FilePath != "" {
			toDelete = append(toDelete, book.FilePath)
		}
		if len(toDelete) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "book has no file to delete"})
			return
		}
	}

	// Remove files from disk and from book_files. When a format filter is
	// supplied, the stem-sibling sweep is scoped to that format so deleting
	// the ebook does not also destroy the audiobook sibling (and vice versa).
	// Paths failing the library-root containment check are skipped (with a
	// WARN log); the book_files row is still deregistered so the orphaned
	// path stops surfacing in subsequent reads. Returning 500 instead would
	// strand the row permanently behind a path the user cannot delete.
	var deletedPaths []string
	for _, p := range toDelete {
		skipped, err := safeRemoveBookPath(r.Context(), h.roots, p, format, "id", id)
		if err != nil {
			slog.Error("failed to remove book file", "id", id, "path", p, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !skipped {
			deletedPaths = append(deletedPaths, p)
		}
		if _, err := h.books.RemoveBookFile(r.Context(), p); err != nil {
			slog.Warn("failed to deregister book file", "id", id, "path", p, "error", err)
		}
	}

	// Re-load the book to get the refreshed status and file paths.
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	h.attachBookFiles(r.Context(), book)
	cleanBookDescription(book)

	if h.history != nil {
		data, err := json.Marshal(map[string]any{"paths": deletedPaths})
		if err != nil {
			slog.Warn("failed to marshal deleted paths history event", "book_id", id, "error", err)
		} else if err := h.history.Create(r.Context(), &models.HistoryEvent{
			BookID:      &book.ID,
			EventType:   models.HistoryEventBookFileDeleted,
			SourceTitle: book.Title,
			Data:        string(data),
		}); err != nil {
			slog.Warn("failed to create deleted paths history event", "book_id", id, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, book)
}

// removeBookPathScoped deletes a file or directory at p. Audiobooks are stored
// as folders (multi-part mp3/m4b + cover + cue); ebooks are single files.
//
// For single files it also sweeps sibling files in the same directory that
// share the same basename (stem) — this handles dual-format downloads where
// epub + mobi land in one folder. The sweep is scoped by `format`:
//
//   - format == ""         — sweep every same-stem recognised book file
//     (used for full-book deletes where every format goes anyway).
//   - format == "ebook"    — sweep only same-stem *ebook* siblings; the
//     audiobook sibling (.m4b/.mp3/...) is left intact.
//   - format == "audiobook"— sweep only same-stem *audiobook* siblings; the
//     ebook sibling is left intact.
//
// This prevents a `?format=ebook` delete from also destroying the audiobook
// that happens to share a stem in the same folder.
//
// Returns nil if the path no longer exists — the net state is the same.
func removeBookPathScoped(p, format string) error {
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(p)
	}

	// Sweep sibling book files with the same stem in the parent directory.
	parent := filepath.Dir(p)
	stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	entries, readErr := os.ReadDir(parent)
	if readErr == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if !importer.IsBookFile(n) {
				continue
			}
			// When a format filter is supplied, only sweep siblings whose
			// extension belongs to that format. The explicitly targeted file
			// p itself always matches its own format, so it is still removed.
			if !sweepMatchesFormat(n, format) {
				continue
			}
			s := strings.TrimSuffix(n, filepath.Ext(n))
			if !strings.EqualFold(s, stem) {
				continue
			}
			if rmErr := os.Remove(filepath.Join(parent, n)); rmErr != nil && !os.IsNotExist(rmErr) { // #nosec G304 G703 -- parent + stem both from importer-sanitized DB row; #865 plans defense-in-depth root-check
				slog.Warn("book delete: failed to remove sibling file", "path", filepath.Join(parent, n), "error", rmErr)
			}
		}
	} else {
		// ReadDir failed — fall back to deleting only the target file.
		if err := os.Remove(p); err != nil { // #nosec G304 G703 -- p is from book_files row written by importer's sanitizePath; #865 plans defense-in-depth root-check
			return err
		}
	}

	// Clean up parent directory if it is now empty.
	remaining, err := os.ReadDir(parent)
	if err == nil && len(remaining) == 0 {
		_ = os.Remove(parent) // #nosec G304 G703 -- parent = filepath.Dir of importer-sanitized DB row; #865 plans defense-in-depth root-check
	}
	return nil
}

// sweepMatchesFormat reports whether the sibling file `name` belongs to the
// requested format and is therefore eligible for the stem sweep. An empty
// format means no scoping — every recognised book file matches.
func sweepMatchesFormat(name, format string) bool {
	switch format {
	case "":
		return true
	case models.MediaTypeAudiobook:
		return importer.IsAudioTagFile(name)
	case models.MediaTypeEbook:
		return !importer.IsAudioTagFile(name)
	default:
		// Unknown format filter: do not sweep siblings — only the explicitly
		// enumerated paths get removed by the caller.
		return false
	}
}

func (h *BookHandler) ListWanted(w http.ResponseWriter, r *http.Request) {
	var books []models.Book
	var err error
	if r.URL.Query().Get("includeExcluded") == "true" {
		books, err = h.books.ListByStatusIncludingExcluded(r.Context(), models.BookStatusWanted)
	} else {
		books, err = h.books.ListByStatus(r.Context(), models.BookStatusWanted)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if books == nil {
		books = []models.Book{}
	}
	for i := range books {
		cleanBookDescription(&books[i])
	}
	writeJSON(w, http.StatusOK, books)
}

// Rebind updates a book's foreign_id and metadata_provider, then re-fetches
// metadata from that provider. This lets users correct a wrong metadata match
// (e.g. an omnibus instead of the standalone Book 1) without deleting and
// re-adding the book.
//
// Request body:
//
//	{
//	  "provider":   "openlibrary"|"hardcover",
//	  "foreign_id": "<id>",
//	  "force":      false   // set true to override an author-mismatch warning
//	}
//
// Returns 409 when the upstream record belongs to a different author unless
// force=true is passed. Returns the updated book JSON on success.
func (h *BookHandler) Rebind(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	var req struct {
		Provider  string `json:"provider"`
		ForeignID string `json:"foreign_id"`
		Force     bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.Provider = strings.TrimSpace(strings.ToLower(req.Provider))
	req.ForeignID = strings.TrimSpace(req.ForeignID)
	if req.Provider == "" || req.ForeignID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and foreign_id are required"})
		return
	}
	switch req.Provider {
	case "openlibrary", "hardcover":
		// valid
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider must be 'openlibrary' or 'hardcover'"})
		return
	}

	if h.lookup == nil {
		writeJSON(w, http.StatusFailedDependency, map[string]string{"error": "metadata aggregator not configured"})
		return
	}

	upstream, err := h.lookup.GetBookFromProvider(r.Context(), req.Provider, req.ForeignID)
	if err != nil {
		slog.Warn("rebind: upstream fetch failed", "bookId", book.ID, "provider", req.Provider, "foreignId", req.ForeignID, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if upstream == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no record found for that provider and foreign ID"})
		return
	}

	// Author-mismatch guard: if the upstream record belongs to a different
	// author than the current book row, reject unless the caller passes force=true.
	// This prevents a typo in the foreign ID from silently corrupting authorship.
	if !req.Force && h.authors != nil && upstream.Author != nil && upstream.Author.ForeignID != "" {
		currentAuthor, authErr := h.authors.GetByID(r.Context(), book.AuthorID)
		if authErr == nil && currentAuthor != nil && currentAuthor.ForeignID != upstream.Author.ForeignID {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":           "author mismatch: upstream record belongs to a different author",
				"current_author":  currentAuthor.Name,
				"upstream_author": upstream.Author.Name,
				"force_required":  true,
			})
			return
		}
	}

	// Snapshot old identifiers for the audit trail before mutating the book.
	oldProvider := book.MetadataProvider
	oldForeignID := book.ForeignID

	// Update the book with the new foreign ID and refreshed metadata.
	// Preserve fields managed by the user or the import pipeline (status,
	// monitored, file paths, media type, ASIN, narrator, calibre_id).
	book.ForeignID = req.ForeignID
	book.MetadataProvider = req.Provider
	if upstream.Title != "" {
		book.Title = upstream.Title
		book.SortTitle = upstream.SortTitle
	}
	if upstream.OriginalTitle != "" {
		book.OriginalTitle = upstream.OriginalTitle
	}
	if upstream.Description != "" {
		book.Description = upstream.Description
	}
	if upstream.ImageURL != "" {
		book.ImageURL = upstream.ImageURL
	}
	if upstream.ReleaseDate != nil {
		book.ReleaseDate = upstream.ReleaseDate
	}
	if len(upstream.Genres) > 0 {
		book.Genres = upstream.Genres
	}
	if upstream.AverageRating > 0 {
		book.AverageRating = upstream.AverageRating
		book.RatingsCount = upstream.RatingsCount
	}
	if upstream.Language != "" {
		book.Language = upstream.Language
	}
	now := time.Now().UTC()
	book.LastMetadataRefreshAt = &now

	if err := h.books.Update(r.Context(), book); err != nil {
		// A UNIQUE constraint means another book row already owns this foreign_id.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a different book already uses that foreign ID"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.hydrateHardcoverEditions(r.Context(), book, req.Provider)

	// Re-link series membership: remove all existing links for this book, then
	// attach whatever the upstream record declares.
	if h.series != nil {
		if existingIDs, serErr := h.series.GetSeriesIDsForBook(r.Context(), book.ID); serErr == nil {
			for _, sid := range existingIDs {
				_ = h.series.UnlinkBook(r.Context(), sid, book.ID)
			}
		}
		for _, ref := range upstream.SeriesRefs {
			s := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
			if err := h.series.CreateOrGet(r.Context(), s); err != nil {
				slog.Warn("rebind: failed to upsert series", "series", ref.Title, "error", err)
				continue
			}
			if err := h.series.LinkBook(r.Context(), s.ID, book.ID, ref.Position, ref.Primary); err != nil {
				slog.Warn("rebind: failed to link book to series", "book", book.Title, "series", ref.Title, "error", err)
			}
		}
	}

	// Audit trail: record old and new identifiers so the operation is reversible
	// by hand and visible in the activity history.
	if h.history != nil {
		type rebindData struct {
			OldProvider  string `json:"oldProvider"`
			OldForeignID string `json:"oldForeignId"`
			NewProvider  string `json:"newProvider"`
			NewForeignID string `json:"newForeignId"`
		}
		if data, jerr := json.Marshal(rebindData{oldProvider, oldForeignID, req.Provider, req.ForeignID}); jerr == nil {
			bookID := book.ID
			_ = h.history.Create(r.Context(), &models.HistoryEvent{
				BookID:      &bookID,
				EventType:   models.HistoryEventBookRebound,
				SourceTitle: book.Title,
				Data:        string(data),
			})
		}
	}

	h.attachBookFiles(r.Context(), book)
	cleanBookDescription(book)
	proxyBookImages(book)
	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) ToggleExcluded(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	newVal := !book.Excluded
	if err := h.books.SetExcluded(r.Context(), id, newVal); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	book.Excluded = newVal
	cleanBookDescription(book)
	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) MapMetadata(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if h.meta == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metadata provider unavailable"})
		return
	}
	if h.authors == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "author repository unavailable"})
		return
	}
	var req struct {
		ForeignBookID string `json:"foreignBookId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.ForeignBookID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignBookId is required"})
		return
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1).
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	target, err := h.meta.GetBook(r.Context(), req.ForeignBookID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "upstream book not found"})
		return
	}
	if target.ForeignID == "" {
		target.ForeignID = req.ForeignBookID
	}
	currentAuthor, err := h.authors.GetByID(r.Context(), book.AuthorID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !bookMapAuthorMatches(currentAuthor, target.Author) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "target book author does not match current author"})
		return
	}

	preserveBookStateForMetadataMap(book, target)
	if err := h.books.Update(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.hydrateHardcoverEditions(r.Context(), book, book.MetadataProvider)
	updated, err := h.books.GetByID(r.Context(), book.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.attachBookFiles(r.Context(), updated)
	cleanBookDescription(updated)
	proxyBookImages(updated)
	writeJSON(w, http.StatusOK, updated)
}

func preserveBookStateForMetadataMap(book *models.Book, target *models.Book) {
	id := book.ID
	authorID := book.AuthorID
	monitored := book.Monitored
	status := book.Status
	anyEditionOK := book.AnyEditionOK
	selectedEditionID := book.SelectedEditionID
	filePath := book.FilePath
	mediaType := book.MediaType
	narrator := book.Narrator
	durationSeconds := book.DurationSeconds
	asin := book.ASIN
	calibreID := book.CalibreID
	createdAt := book.CreatedAt
	ebookFilePath := book.EbookFilePath
	audiobookFilePath := book.AudiobookFilePath
	excluded := book.Excluded

	book.ForeignID = target.ForeignID
	book.Title = target.Title
	book.SortTitle = firstNonEmpty(target.SortTitle, target.Title)
	book.OriginalTitle = target.OriginalTitle
	book.Description = target.Description
	book.ImageURL = target.ImageURL
	book.ReleaseDate = target.ReleaseDate
	book.Genres = target.Genres
	if book.Genres == nil {
		book.Genres = []string{}
	}
	book.AverageRating = target.AverageRating
	book.RatingsCount = target.RatingsCount
	book.Language = target.Language
	book.MetadataProvider = firstNonEmpty(target.MetadataProvider, metadataProviderFromForeignID(target.ForeignID))

	book.ID = id
	book.AuthorID = authorID
	book.Monitored = monitored
	book.Status = status
	book.AnyEditionOK = anyEditionOK
	book.SelectedEditionID = selectedEditionID
	book.FilePath = filePath
	book.MediaType = mediaType
	book.Narrator = narrator
	book.DurationSeconds = durationSeconds
	book.ASIN = asin
	book.CalibreID = calibreID
	book.CreatedAt = createdAt
	book.EbookFilePath = ebookFilePath
	book.AudiobookFilePath = audiobookFilePath
	book.Excluded = excluded
}

func bookMapAuthorMatches(current, target *models.Author) bool {
	if current == nil || target == nil {
		return false
	}
	if strings.TrimSpace(current.ForeignID) != "" &&
		strings.TrimSpace(target.ForeignID) != "" &&
		strings.TrimSpace(current.ForeignID) == strings.TrimSpace(target.ForeignID) {
		return true
	}
	if authorNameAutoMatches(current.Name, target.Name) {
		return true
	}
	for _, alias := range target.AlternateNames {
		if authorNameAutoMatches(current.Name, alias) {
			return true
		}
	}
	return false
}

func authorNameAutoMatches(a, b string) bool {
	match := textutil.MatchAuthorName(a, b)
	return match.Kind == textutil.AuthorMatchExact || match.Kind == textutil.AuthorMatchFuzzyAuto
}

func metadataProviderFromForeignID(foreignID string) string {
	switch {
	case strings.HasPrefix(foreignID, "gb:"):
		return "googlebooks"
	case strings.HasPrefix(foreignID, "hc:"):
		return "hardcover"
	case strings.HasPrefix(foreignID, "dnb:"):
		return "dnb"
	default:
		return "openlibrary"
	}
}
