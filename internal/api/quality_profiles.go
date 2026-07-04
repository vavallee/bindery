package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type QualityProfileHandler struct {
	profiles *db.QualityProfileRepo
}

func NewQualityProfileHandler(profiles *db.QualityProfileRepo) *QualityProfileHandler {
	return &QualityProfileHandler{profiles: profiles}
}

func (h *QualityProfileHandler) List(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.profiles.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if profiles == nil {
		profiles = []models.QualityProfile{}
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (h *QualityProfileHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.profiles.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quality profile not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). The Put / Delete routes for this
	// resource are already RequireAdmin (see cmd/bindery/main.go), so only
	// the read path needs the per-user gate here.
	if !auth.CheckOwnership(r.Context(), p.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quality profile not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *QualityProfileHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	p.ID = 0
	if msg := validateQualityProfile(&p); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	taken, err := h.profiles.NameExists(r.Context(), p.Name, 0)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a quality profile with that name already exists"})
		return
	}
	if err := h.profiles.Create(r.Context(), &p); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *QualityProfileHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.profiles.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quality profile not found"})
		return
	}
	var p models.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	p.ID = id
	if msg := validateQualityProfile(&p); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	taken, err := h.profiles.NameExists(r.Context(), p.Name, id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a quality profile with that name already exists"})
		return
	}
	if err := h.profiles.Update(r.Context(), &p); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *QualityProfileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.profiles.Delete(r.Context(), id); err != nil {
		if inUse, ok := db.AsInUseError(err); ok {
			names, _ := h.profiles.AuthorNamesUsing(r.Context(), id, 5)
			msg := formatInUseMessage(inUse.AuthorCount, names)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":       msg,
				"authorCount": inUse.AuthorCount,
				"authorNames": names,
			})
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "quality profile not found"})
			return
		}
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// formatInUseMessage builds a single-line, user-readable explanation of why
// the delete was refused. The frontend surfaces this string in an alert/inline
// error — the structured fields (authorCount, authorNames) are also returned
// for callers that want to render their own UI.
func formatInUseMessage(count int, names []string) string {
	if count == 1 && len(names) == 1 {
		return "in use by author " + names[0]
	}
	if len(names) > 0 && count <= len(names) {
		return "in use by " + strconv.Itoa(count) + " authors: " + strings.Join(names, ", ")
	}
	if len(names) > 0 {
		return "in use by " + strconv.Itoa(count) + " authors including " + strings.Join(names, ", ")
	}
	return "in use by " + strconv.Itoa(count) + " authors"
}

// validateQualityProfile enforces the rules called out in the issue:
//   - name non-empty
//   - at least one allowed format
//   - cutoff is among the allowed formats (i.e. an item with allowed=true)
//   - no duplicate format names in the preference order
//
// The profile is normalised in place (trimmed name, lowercased format keys).
// Returns "" when the profile is valid; otherwise a user-facing message.
func validateQualityProfile(p *models.QualityProfile) string {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return "name required"
	}
	if len(p.Items) == 0 {
		return "at least one format is required"
	}
	seen := make(map[string]struct{}, len(p.Items))
	var allowedCount int
	cutoff := strings.ToLower(strings.TrimSpace(p.Cutoff))
	p.Cutoff = cutoff
	if cutoff == "" {
		return "cutoff required"
	}
	var cutoffAllowed bool
	for i, it := range p.Items {
		q := strings.ToLower(strings.TrimSpace(it.Quality))
		if q == "" {
			return "format name cannot be empty"
		}
		if _, dup := seen[q]; dup {
			return "duplicate format in preference order: " + q
		}
		seen[q] = struct{}{}
		p.Items[i].Quality = q
		if it.Allowed {
			allowedCount++
			if q == cutoff {
				cutoffAllowed = true
			}
		}
	}
	if allowedCount == 0 {
		return "at least one allowed format is required"
	}
	if !cutoffAllowed {
		return "cutoff must be one of the allowed formats"
	}
	return ""
}
