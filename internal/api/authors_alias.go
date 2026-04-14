package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// AuthorAliasHandler owns the alias-list + merge endpoints. Split out from
// AuthorHandler so the merge dependency (a cross-table DB operation) isn't
// smuggled into every caller that only wants CRUD on a single author.
type AuthorAliasHandler struct {
	authors *db.AuthorRepo
	aliases *db.AuthorAliasRepo
}

func NewAuthorAliasHandler(authors *db.AuthorRepo, aliases *db.AuthorAliasRepo) *AuthorAliasHandler {
	return &AuthorAliasHandler{authors: authors, aliases: aliases}
}

// List returns every alias pointing at the given canonical author id.
// GET /api/v1/author/{id}/aliases
func (h *AuthorAliasHandler) List(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	author, err := h.authors.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if author == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found"})
		return
	}
	aliases, err := h.aliases.ListByAuthor(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if aliases == nil {
		aliases = []models.AuthorAlias{}
	}
	writeJSON(w, http.StatusOK, aliases)
}

// Merge collapses the given source author into the target author in the URL.
// Books are reparented, the source's name + OL id are preserved as aliases,
// and the source row is deleted. All in one transaction.
//
// POST /api/v1/author/{id}/merge  {"sourceId": 123}
//
// The {id} in the URL is the *target* (canonical) author — this keeps the
// "merge INTO the author you're looking at" mental model that most UIs use
// (Radarr, Sonarr, JIRA all do it that way).
func (h *AuthorAliasHandler) Merge(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var req struct {
		SourceID          int64 `json:"sourceId"`
		OverwriteDefaults *bool `json:"overwriteDefaults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.SourceID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sourceId required"})
		return
	}
	if req.SourceID == targetID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source and target must differ"})
		return
	}

	// Validate both exist up-front so the UI gets a 404 instead of a 500.
	// The transaction inside Merge re-reads them under lock anyway, so this
	// is only a preflight.
	for _, id := range []int64{req.SourceID, targetID} {
		a, err := h.authors.GetByID(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if a == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "author not found: " + strconv.FormatInt(id, 10)})
			return
		}
	}

	overwrite := true
	if req.OverwriteDefaults != nil {
		overwrite = *req.OverwriteDefaults
	}

	result, err := h.aliases.Merge(r.Context(), req.SourceID, targetID, db.MergeOptions{OverwriteDefaults: overwrite})
	if err != nil {
		slog.Warn("merge authors failed", "sourceId", req.SourceID, "targetId", targetID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("merged authors", "sourceId", req.SourceID, "targetId", targetID,
		"booksReparented", result.BooksReparented, "aliasesMigrated", result.AliasesMigrated,
		"aliasesCreated", result.AliasesCreated)
	writeJSON(w, http.StatusOK, result)
}
