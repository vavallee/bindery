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

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type BookHandler struct {
	books     *db.BookRepo
	meta      *metadata.Aggregator
	history   *db.HistoryRepo
	searcher  BookSearcher
	settings  *db.SettingsRepo
	downloads *db.DownloadRepo
}

func NewBookHandler(books *db.BookRepo, meta *metadata.Aggregator, history *db.HistoryRepo, searcher BookSearcher) *BookHandler {
	return &BookHandler{books: books, meta: meta, history: history, searcher: searcher}
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
	if book.MediaType != models.MediaTypeAudiobook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "book is not an audiobook"})
		return
	}
	if book.ASIN == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set ASIN before enriching"})
		return
	}
	if err := h.meta.EnrichAudiobook(r.Context(), book); err != nil {
		slog.Warn("audnex enrich failed", "bookId", book.ID, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if err := h.books.Update(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cleanBookDescription(book)
	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) List(w http.ResponseWriter, r *http.Request) {
	var books []models.Book
	var err error

	authorID := r.URL.Query().Get("authorId")
	status := r.URL.Query().Get("status")
	includeExcluded := r.URL.Query().Get("includeExcluded") == "true"

	switch {
	case authorID != "":
		id, _ := strconv.ParseInt(authorID, 10, 64)
		if includeExcluded {
			books, err = h.books.ListByAuthorIncludingExcluded(r.Context(), id)
		} else {
			books, err = h.books.ListByAuthor(r.Context(), id)
		}
	case status != "":
		if includeExcluded {
			books, err = h.books.ListByStatusIncludingExcluded(r.Context(), status)
		} else {
			books, err = h.books.ListByStatus(r.Context(), status)
		}
	default:
		if includeExcluded {
			books, err = h.books.ListIncludingExcluded(r.Context())
		} else {
			books, err = h.books.List(r.Context())
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
	writeJSON(w, http.StatusOK, books)
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
		book.Status = *req.Status
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
		bgCtx := context.WithoutCancel(r.Context())
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
	// Opt-in `?deleteFiles=true` also removes every on-disk file tracked in
	// book_files before dropping the record.
	if r.URL.Query().Get("deleteFiles") == "true" {
		files, _ := h.books.ListFiles(r.Context(), id)
		for _, f := range files {
			if err := removeBookPath(f.Path); err != nil {
				slog.Warn("book delete: failed to remove file", "id", id, "path", f.Path, "error", err)
			}
		}
		// Fallback for books imported before the book_files migration.
		if len(files) == 0 {
			if book, _ := h.books.GetByID(r.Context(), id); book != nil {
				for _, p := range []string{book.EbookFilePath, book.AudiobookFilePath, book.FilePath} {
					if p != "" {
						if err := removeBookPath(p); err != nil {
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

	// Remove files from disk and from book_files.
	var deletedPaths []string
	for _, p := range toDelete {
		if err := removeBookPath(p); err != nil {
			slog.Error("failed to remove book file", "id", id, "path", p, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		deletedPaths = append(deletedPaths, p)
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

// removeBookPath deletes a file or directory at p. Audiobooks are stored as
// folders (multi-part mp3/m4b + cover + cue); ebooks are single files.
// For single files it also sweeps any sibling files in the same directory
// that share the same basename (stem) and have a recognised book extension —
// this handles dual-format downloads where epub + mobi land in one folder.
// Returns nil if the path no longer exists — the net state is the same.
func removeBookPath(p string) error {
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
			s := strings.TrimSuffix(n, filepath.Ext(n))
			if !strings.EqualFold(s, stem) {
				continue
			}
			if rmErr := os.Remove(filepath.Join(parent, n)); rmErr != nil && !os.IsNotExist(rmErr) { //nosec G304 -- derived from DB-stored path
				slog.Warn("book delete: failed to remove sibling file", "path", filepath.Join(parent, n), "error", rmErr)
			}
		}
	} else {
		// ReadDir failed — fall back to deleting only the target file.
		if err := os.Remove(p); err != nil { //nosec G304 -- p is a DB-stored path written by the import pipeline, not user input
			return err
		}
	}

	// Clean up parent directory if it is now empty.
	remaining, err := os.ReadDir(parent)
	if err == nil && len(remaining) == 0 {
		_ = os.Remove(parent) //nosec G304 -- derived from DB-stored path, not user input
	}
	return nil
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

// ToggleExcluded flips the excluded flag on a book. The response body is the
// updated book so the UI can refresh in place without a second round-trip.
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

	newVal := !book.Excluded
	if err := h.books.SetExcluded(r.Context(), id, newVal); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	book.Excluded = newVal
	cleanBookDescription(book)
	writeJSON(w, http.StatusOK, book)
}
