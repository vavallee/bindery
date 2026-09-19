package api

import (
	"net/http"

	"github.com/vavallee/bindery/internal/duplicates"
)

// duplicateCandidatesResponse is the payload for GET
// /author/{id}/duplicate-candidates (#1970).
type duplicateCandidatesResponse struct {
	AuthorID int64              `json:"authorId"`
	Groups   []duplicates.Group `json:"groups"`
	Count    int                `json:"count"`
}

// DuplicateCandidates implements GET /author/{id}/duplicate-candidates
// (#1970): a read-only, human-reviewed duplicate-title report for one
// author's catalogue. It scans every book row — excluded rows included, so a
// group whose members have already been excluded can be re-shown as "all
// excluded" instead of vanishing — keeps only groups with at least two
// non-excluded members, and returns them annotated with the rule(s) that
// matched so the UI can explain each group in plain language.
//
// The endpoint writes nothing: acting on a candidate is the existing exclude
// route (PUT /book/{id}/exclude), which the UI calls only after a human
// confirms. That is the whole safety model of #1970 — aggressive detection,
// human in the loop, no silent merges.
func (h *AuthorHandler) DuplicateCandidates(w http.ResponseWriter, r *http.Request) {
	author, ok := h.loadOwnedAuthor(w, r)
	if !ok {
		return
	}
	books, err := h.books.ListByAuthorIncludingExcluded(r.Context(), author.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	groups := duplicates.Scan(books)
	active := groups[:0]
	for i := range groups {
		n := 0
		for _, m := range groups[i].Members {
			if !m.Excluded {
				n++
			}
		}
		if n >= 2 {
			active = append(active, groups[i])
		}
	}
	if active == nil {
		active = []duplicates.Group{}
	}
	writeJSON(w, http.StatusOK, duplicateCandidatesResponse{
		AuthorID: author.ID,
		Groups:   active,
		Count:    len(active),
	})
}
