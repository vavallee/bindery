package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type ABSConflictHandler struct {
	conflicts *db.ABSMetadataConflictRepo
	authors   *db.AuthorRepo
	books     *db.BookRepo
}

type absConflictResponse struct {
	ID                   int64  `json:"id"`
	SourceID             string `json:"sourceId"`
	LibraryID            string `json:"libraryId"`
	ItemID               string `json:"itemId"`
	EntityType           string `json:"entityType"`
	LocalID              int64  `json:"localId"`
	EntityName           string `json:"entityName"`
	FieldName            string `json:"fieldName"`
	FieldLabel           string `json:"fieldLabel"`
	ABSValue             string `json:"absValue"`
	UpstreamValue        string `json:"upstreamValue"`
	AppliedSource        string `json:"appliedSource"`
	AppliedValue         string `json:"appliedValue"`
	PreferredSource      string `json:"preferredSource"`
	AuthorRelinkEligible bool   `json:"authorRelinkEligible"`
	ResolutionStatus     string `json:"resolutionStatus"`
	UpdatedAt            string `json:"updatedAt"`
}

type absConflictListResponse struct {
	Items  []absConflictResponse `json:"items"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

func NewABSConflictHandler(conflicts *db.ABSMetadataConflictRepo, authors *db.AuthorRepo, books *db.BookRepo) *ABSConflictHandler {
	return &ABSConflictHandler{conflicts: conflicts, authors: authors, books: books}
}

func (h *ABSConflictHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 50, 100)
	if h.conflicts == nil {
		writeJSON(w, http.StatusOK, absConflictListResponse{
			Items:  []absConflictResponse{},
			Total:  0,
			Limit:  limit,
			Offset: offset,
		})
		return
	}
	conflicts, total, err := h.conflicts.ListPaginated(r.Context(), limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	out := make([]absConflictResponse, 0, len(conflicts))
	for _, conflict := range conflicts {
		item, err := h.decorateConflict(r, &conflict)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, absConflictListResponse{
		Items:  out,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

func (h *ABSConflictHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	if h.conflicts == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "abs conflict store not configured"})
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	conflict, err := h.conflicts.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if conflict == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "conflict not found"})
		return
	}

	var req struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	source := strings.TrimSpace(req.Source)
	switch source {
	case abs.MetadataSourceABS, abs.MetadataSourceUpstream:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source must be 'abs' or 'upstream'"})
		return
	}
	// Atomically claim the conflict to prevent a TOCTOU race where two
	// concurrent Resolve calls both read "pending", apply different entity
	// values, and both write "resolved" — leaving the entity and status record
	// disagreeing on which source won.
	claimed, err := h.conflicts.Claim(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !claimed {
		// Re-read and return the current state so the client can see the
		// winning resolution without treating it as an error.
		current, err := h.conflicts.GetByID(r.Context(), id)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		if current == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "conflict not found"})
			return
		}
		item, err := h.decorateConflict(r, current)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if err := h.applyConflictChoice(r, conflict, source); err != nil {
		_ = h.conflicts.Unclaim(r.Context(), id)
		writeServerError(w, r, err)
		return
	}
	conflict.AppliedSource = source
	conflict.PreferredSource = source
	conflict.ResolutionStatus = "resolved"
	if err := h.conflicts.Upsert(r.Context(), conflict); err != nil {
		writeServerError(w, r, err)
		return
	}
	item, err := h.decorateConflict(r, conflict)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *ABSConflictHandler) applyConflictChoice(r *http.Request, conflict *models.ABSMetadataConflict, source string) error {
	value := conflict.UpstreamValue
	if source == abs.MetadataSourceABS {
		value = conflict.ABSValue
	}
	now := time.Now().UTC()
	switch conflict.EntityType {
	case "author":
		author, err := h.authors.GetByID(r.Context(), conflict.LocalID)
		if err != nil {
			return err
		}
		if author == nil {
			return nil
		}
		if err := abs.ApplyAuthorConflictValue(author, conflict.FieldName, value); err != nil {
			return err
		}
		author.LastMetadataRefreshAt = &now
		return h.authors.Update(r.Context(), author)
	case "book":
		book, err := h.books.GetByID(r.Context(), conflict.LocalID)
		if err != nil {
			return err
		}
		if book == nil {
			return nil
		}
		if err := abs.ApplyBookConflictValue(book, conflict.FieldName, value); err != nil {
			return err
		}
		book.LastMetadataRefreshAt = &now
		return h.books.Update(r.Context(), book)
	default:
		return nil
	}
}

func (h *ABSConflictHandler) decorateConflict(r *http.Request, conflict *models.ABSMetadataConflict) (absConflictResponse, error) {
	resp := absConflictResponse{
		ID:               conflict.ID,
		SourceID:         conflict.SourceID,
		LibraryID:        conflict.LibraryID,
		ItemID:           conflict.ItemID,
		EntityType:       conflict.EntityType,
		LocalID:          conflict.LocalID,
		FieldName:        conflict.FieldName,
		FieldLabel:       abs.ConflictFieldLabel(conflict.FieldName),
		ABSValue:         conflict.ABSValue,
		UpstreamValue:    conflict.UpstreamValue,
		AppliedSource:    conflict.AppliedSource,
		PreferredSource:  conflict.PreferredSource,
		ResolutionStatus: conflict.ResolutionStatus,
		UpdatedAt:        conflict.UpdatedAt.Format(time.RFC3339),
	}
	switch conflict.AppliedSource {
	case abs.MetadataSourceABS:
		resp.AppliedValue = conflict.ABSValue
	case abs.MetadataSourceUpstream:
		resp.AppliedValue = conflict.UpstreamValue
	}
	switch conflict.EntityType {
	case "author":
		author, err := h.authors.GetByID(r.Context(), conflict.LocalID)
		if err != nil {
			return absConflictResponse{}, err
		}
		if author != nil {
			resp.EntityName = author.Name
			resp.AuthorRelinkEligible = canRelinkAuthorToUpstream(author)
		}
	case "book":
		book, err := h.books.GetByID(r.Context(), conflict.LocalID)
		if err != nil {
			return absConflictResponse{}, err
		}
		if book != nil {
			resp.EntityName = book.Title
		}
	}
	if resp.EntityName == "" {
		resp.EntityName = conflict.ItemID
	}
	return resp, nil
}
