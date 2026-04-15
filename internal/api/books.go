package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type BookHandler struct {
	books    *db.BookRepo
	meta     *metadata.Aggregator
	history  *db.HistoryRepo
	searcher BookSearcher
	settings *db.SettingsRepo
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
	writeJSON(w, http.StatusOK, book)
}

func (h *BookHandler) List(w http.ResponseWriter, r *http.Request) {
	var books []models.Book
	var err error

	authorID := r.URL.Query().Get("authorId")
	status := r.URL.Query().Get("status")

	switch {
	case authorID != "":
		id, _ := strconv.ParseInt(authorID, 10, 64)
		books, err = h.books.ListByAuthor(r.Context(), id)
	case status != "":
		books, err = h.books.ListByStatus(r.Context(), status)
	default:
		books, err = h.books.List(r.Context())
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if books == nil {
		books = []models.Book{}
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
	writeJSON(w, http.StatusOK, book)
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
		if *req.MediaType != models.MediaTypeEbook && *req.MediaType != models.MediaTypeAudiobook {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be 'ebook' or 'audiobook'"})
			return
		}
		book.MediaType = *req.MediaType
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
	// Opt-in `?deleteFiles=true` also removes the on-disk file or folder
	// before dropping the record, so the user doesn't have to delete the
	// file separately after removing the book.
	if r.URL.Query().Get("deleteFiles") == "true" {
		if book, _ := h.books.GetByID(r.Context(), id); book != nil && book.FilePath != "" {
			if err := removeBookPath(book.FilePath); err != nil {
				slog.Warn("book delete: failed to remove files", "id", id, "path", book.FilePath, "error", err)
			}
		}
	}
	if err := h.books.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteFile removes the on-disk file or folder backing an imported book,
// clears the stored file_path, and flips the status back to `wanted` so the
// book re-appears on the Wanted page. The book record itself is kept. Used
// to clean up bad grabs (wrong edition, corrupt files, mis-tagged metadata)
// without losing the author/book association or its history.
func (h *BookHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	if book.FilePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "book has no file to delete"})
		return
	}

	oldPath := book.FilePath
	if err := removeBookPath(oldPath); err != nil {
		slog.Error("failed to remove book path", "id", id, "path", oldPath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	book.FilePath = ""
	book.Status = models.BookStatusWanted
	if err := h.books.Update(r.Context(), book); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if h.history != nil {
		data, _ := json.Marshal(map[string]string{"path": oldPath})
		_ = h.history.Create(r.Context(), &models.HistoryEvent{
			BookID:      &book.ID,
			EventType:   models.HistoryEventBookFileDeleted,
			SourceTitle: book.Title,
			Data:        string(data),
		})
	}

	writeJSON(w, http.StatusOK, book)
}

// removeBookPath deletes a file or directory at p. Audiobooks are stored as
// folders (multi-part mp3/m4b + cover + cue); ebooks are single files.
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
	return os.Remove(p)
}

func (h *BookHandler) ListWanted(w http.ResponseWriter, r *http.Request) {
	books, err := h.books.ListByStatus(r.Context(), models.BookStatusWanted)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if books == nil {
		books = []models.Book{}
	}
	writeJSON(w, http.StatusOK, books)
}
