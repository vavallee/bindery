package api

import "net/http"

// SeriesExclusions lists the series the user took this book out of (#2554):
// GET /book/{id}/series-exclusions. Each names the series as it is now, and
// the position the book had, so the book page can show it and offer Restore.
func (h *BookHandler) SeriesExclusions(w http.ResponseWriter, r *http.Request) {
	book, ok := h.loadOwnedBook(w, r)
	if !ok {
		return
	}
	if h.series == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	list, err := h.series.ListBookSeriesExclusions(r.Context(), book.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
