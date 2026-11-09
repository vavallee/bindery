package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/searchruns"
)

// SearchRunHandler serves GET /search/runs/{id}: how one search ended, for a
// caller that started it and holds the run id its response carried (#3133).
type SearchRunHandler struct {
	runs *searchruns.Store
}

func NewSearchRunHandler(runs *searchruns.Store) *SearchRunHandler {
	return &SearchRunHandler{runs: runs}
}

// Get answers 404 for an id that is unknown, expired, forgotten by a restart,
// or belongs to someone the caller may not see, with the same body for all of
// them so the answer says nothing about other users' runs. Visibility is
// auth.ListScopeUserID, as for the other owner scoped lists: admins and API
// key callers read every run, a user reads their own, and a run with no owner
// (a scheduled one) is admin only.
func (h *SearchRunHandler) Get(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runs.Get(chi.URLParam(r, "id"), auth.ListScopeUserID(r.Context()))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "search run not found"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// startSearchRun registers a run for book when store is wired and returns the
// run id and a context carrying it. With no store it returns "" and ctx as is.
func startSearchRun(ctx context.Context, store *searchruns.Store, book *models.Book, origin indexer.SearchOrigin) (string, context.Context) {
	id := store.Start(book.OwnerUserID, book.ID, string(origin))
	return id, searchruns.WithRunID(ctx, id)
}
