package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
)

// WithLibrary attaches the book repo the filtered books endpoints read and
// unmonitor (#2208). Without it they answer 503.
func (h *MetadataProfileHandler) WithLibrary(books *db.BookRepo) *MetadataProfileHandler {
	h.books = books
	return h
}

// profileFilteredBook is one library book the profile's filters would have
// kept out had they been on when the book was added.
type profileFilteredBook struct {
	BookID     int64  `json:"bookId"`
	Title      string `json:"title"`
	AuthorID   int64  `json:"authorId"`
	AuthorName string `json:"authorName,omitempty"`
	Reason     string `json:"reason"`
}

// filteredBooks loads the profile and the wanted, monitored books it governs
// that its stored row filters reject. It writes the error response itself
// and returns ok=false when the caller should stop.
func (h *MetadataProfileHandler) filteredBooks(w http.ResponseWriter, r *http.Request) ([]profileFilteredBook, bool) {
	if h.books == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "library is not available"})
		return nil, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return nil, false
	}
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return nil, false
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metadata profile not found"})
		return nil, false
	}
	books, err := h.books.ListWantedMonitoredByMetadataProfile(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return nil, false
	}
	out := make([]profileFilteredBook, 0)
	for i := range books {
		b := &books[i]
		reason := storedBookProfileFilter(p, b)
		if reason == "" {
			continue
		}
		item := profileFilteredBook{BookID: b.ID, Title: b.Title, AuthorID: b.AuthorID, Reason: reason}
		if b.Author != nil {
			item.AuthorName = b.Author.Name
		}
		out = append(out, item)
	}
	return out, true
}

// FilteredBooks previews the wanted, monitored books already in the library
// that this profile's filters screen out (#2208). The filters only screen new
// books during a sync, so turning one on never touched what was already
// there. Only the filters a stored row can be judged by are applied:
// SkipPartBooks and SkipMissingDate (see storedBookProfileFilter). Nothing
// changes here; UnmonitorFilteredBooks is the action.
func (h *MetadataProfileHandler) FilteredBooks(w http.ResponseWriter, r *http.Request) {
	books, ok := h.filteredBooks(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": books, "total": len(books)})
}

// UnmonitorFilteredBooks unmonitors books from the FilteredBooks preview. The
// body may name bookIds to act on a subset; without it every previewed book
// is unmonitored. Ids are checked against a fresh preview, so a stale or
// hand written list can never unmonitor a book the filters do not reject.
// Nothing is deleted and imported books are never touched: an unmonitored
// book stays in the library, leaves Wanted, and can be monitored again from
// its page.
func (h *MetadataProfileHandler) UnmonitorFilteredBooks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BookIDs []int64 `json:"bookIds"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	books, ok := h.filteredBooks(w, r)
	if !ok {
		return
	}
	var want map[int64]struct{}
	if len(body.BookIDs) > 0 {
		want = make(map[int64]struct{}, len(body.BookIDs))
		for _, id := range body.BookIDs {
			want[id] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(books))
	for _, b := range books {
		if want != nil {
			if _, ok := want[b.BookID]; !ok {
				continue
			}
		}
		ids = append(ids, b.BookID)
	}
	n, err := h.books.UnmonitorNotImported(r.Context(), ids)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	slog.Info("metadata profile: unmonitored books its filters screen out",
		"profileID", chi.URLParam(r, "id"), "unmonitored", n)
	writeJSON(w, http.StatusOK, map[string]int64{"unmonitored": n})
}
