package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/db"
)

// Merge merges one or more series into this one (#2554): POST
// /series/{id}/merge with {"sourceIds": [...], "title": "...", "dryRun": true}.
// A dry run returns the plan and changes nothing; otherwise the merge is
// applied in one transaction and the plan it carried out is returned. An
// optional title renames the target. See db.SeriesRepo.Merge for what moves.
func (h *SeriesHandler) Merge(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body struct {
		SourceIDs []int64 `json:"sourceIds"`
		Title     string  `json:"title"`
		DryRun    bool    `json:"dryRun"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	title := ""
	if strings.TrimSpace(body.Title) != "" {
		var msg string
		if title, msg = validateSeriesTitle(body.Title); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
	}
	merge := h.series.Merge
	if body.DryRun {
		merge = h.series.PlanMerge
	}
	plan, err := merge(r.Context(), id, body.SourceIDs, title)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "series not found"})
	case errors.Is(err, db.ErrSeriesMergeInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case err != nil:
		writeServerError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, plan)
	}
}
