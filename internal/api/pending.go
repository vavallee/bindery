package api

import (
	"encoding/json"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// PendingHandler serves the pending releases API.
type PendingHandler struct {
	pending   *db.PendingReleaseRepo
	queue     *QueueHandler
	downloads *db.DownloadRepo
	books     *db.BookRepo
}

// NewPendingHandler creates a PendingHandler.
func NewPendingHandler(pending *db.PendingReleaseRepo, queue *QueueHandler, downloads *db.DownloadRepo, books *db.BookRepo) *PendingHandler {
	return &PendingHandler{pending: pending, queue: queue, downloads: downloads, books: books}
}

// List returns all pending releases.
func (h *PendingHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.pending.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if items == nil {
		items = []models.PendingRelease{}
	}
	writeJSON(w, http.StatusOK, items)
}

// Delete dismisses a pending release.
func (h *PendingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.pending.DeleteByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Grab force-grabs a pending release, bypassing delay profile.
func (h *PendingHandler) Grab(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	pr, err := h.pending.GetByID(r.Context(), id)
	if err != nil || pr == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending release not found"})
		return
	}

	// Deserialize the stored release JSON to get the full grab details.
	var stored grabRequest
	if err := json.Unmarshal([]byte(pr.ReleaseJSON), &stored); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid stored release: " + err.Error()})
		return
	}

	if h.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue handler not available"})
		return
	}

	// Look up the book's media type.
	mediaType := "ebook"
	if h.books != nil {
		if book, err := h.books.GetByID(r.Context(), pr.BookID); err == nil && book != nil {
			mediaType = book.MediaType
		}
	}
	stored.BookID = &pr.BookID
	stored.MediaType = mediaType

	dl, err := h.queue.grab(r.Context(), stored)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Remove from pending on successful grab.
	_ = h.pending.DeleteByGUID(r.Context(), pr.GUID)

	writeJSON(w, http.StatusCreated, dl)
}
