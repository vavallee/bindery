package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// pendingItem wraps a pending release with a linkable book + author projection
// so the UI can deep-link each row to /book/:id and /author/:id. The embedded
// PendingRelease keeps the response a flat array (backward compatible); `book`
// is added alongside.
type pendingItem struct {
	models.PendingRelease
	Book *bookRef `json:"book,omitempty"`
}

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

// List returns all pending releases. With EnforceTenancy on, the list is
// filtered to releases for books owned by the calling user. pending_releases
// has no owner column of its own — ownership is derived via JOIN to
// books.owner_user_id (see PendingReleaseRepo.ListForUser).
func (h *PendingHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.listForCaller(r)
	if err != nil {
		writeServerError(w, r, err)
		return
	}

	out := make([]pendingItem, len(items))
	bookIDs := make([]int64, 0, len(items))
	for i, it := range items {
		// The stored release blob is the raw indexer SearchResult, whose nzbUrl
		// the newznab client signed with the indexer apikey. The queue and
		// search responses strip that credential before it reaches a client
		// (it is an admin-only setting); this list has to do the same. The DB
		// copy keeps the key so force-grab can still re-send the signed URL.
		it.ReleaseJSON = redactReleaseJSON(it.ReleaseJSON)
		out[i] = pendingItem{PendingRelease: it}
		if it.BookID != 0 {
			bookIDs = append(bookIDs, it.BookID)
		}
	}
	// Best-effort enrichment: a book lookup failure still serves the list.
	if refs, err := loadBookRefs(r.Context(), h.books, bookIDs); err != nil {
		slog.Warn("pending: failed to load book refs", "error", err)
	} else {
		for i := range out {
			if out[i].BookID != 0 {
				out[i].Book = refs[out[i].BookID]
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// redactReleaseJSON removes the indexer apikey from the nzbUrl inside a stored
// pending-release blob. The blob is decoded into a generic map rather than into
// newznab.SearchResult so that a field the struct does not know about survives
// the round-trip; anything that fails to decode is returned untouched, since a
// blob we cannot parse is also one we cannot vouch for editing.
func redactReleaseJSON(raw string) string {
	if raw == "" {
		return raw
	}
	var release map[string]any
	if err := json.Unmarshal([]byte(raw), &release); err != nil {
		return raw
	}
	rawURL, ok := release["nzbUrl"].(string)
	if !ok {
		return raw
	}
	redacted := newznab.RedactDownloadURL(rawURL)
	if redacted == rawURL {
		return raw
	}
	release["nzbUrl"] = redacted
	out, err := json.Marshal(release)
	if err != nil {
		return raw
	}
	return string(out)
}

func (h *PendingHandler) listForCaller(r *http.Request) ([]models.PendingRelease, error) {
	ctx := r.Context()
	if auth.EnforceTenancy() && auth.UserRoleFromContext(ctx) != "admin" {
		if uid := auth.UserIDFromContext(ctx); uid != 0 {
			return h.pending.ListForUser(ctx, uid)
		}
	}
	return h.pending.List(ctx)
}

// Delete dismisses a pending release.
func (h *PendingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	// Per-user scoping (D3): pending_releases.book_id -> books.owner_user_id
	// is the only ownership signal. Resolve and gate before deleting.
	owner, exists, err := h.pending.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending release not found"})
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending release not found"})
		return
	}
	if err := h.pending.DeleteByID(r.Context(), id); err != nil {
		writeServerError(w, r, err)
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

	// Per-user scoping (D3): re-resolve owner via JOIN so we honour the same
	// 404-on-mismatch policy as Delete. pr.BookID is non-null in practice but
	// GetOwnerByID handles the LEFT JOIN gracefully.
	owner, _, err := h.pending.GetOwnerByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !auth.CheckOwnership(r.Context(), owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pending release not found"})
		return
	}

	// Deserialize the stored release JSON to get the full grab details.
	var stored grabRequest
	if err := json.Unmarshal([]byte(pr.ReleaseJSON), &stored); err != nil {
		writeServerError(w, r, err)
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
		if errors.Is(err, errAlreadyGrabbed) {
			// err carries alreadyGrabbedDetail's explanation (#1955).
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeServerError(w, r, err)
		return
	}

	// Remove from pending on successful grab. A surviving entry can be
	// re-approved and re-grabbed by the scheduler — duplicate downloads with
	// no visible cause — so a failed delete is worth a warning.
	if err := h.pending.DeleteByGUID(r.Context(), pr.GUID); err != nil {
		slog.Warn("failed to remove pending release after grab", "guid", pr.GUID, "book_id", pr.BookID, "error", err)
	}

	writeJSON(w, http.StatusCreated, dl)
}
