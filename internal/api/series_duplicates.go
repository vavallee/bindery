package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/seriesmatch"
)

// maxDuplicateMergeGroups caps one bulk merge request. Each group is its own
// transaction, so the cap bounds the request's run time, not its safety.
const maxDuplicateMergeGroups = 5000

// Duplicates lists the series that share a title as the Audiobookshelf
// import compares them (#2173): GET /series/duplicates. Each pair is a series
// the import cannot pick between, so its items cannot link a Hardcover
// series until the pair is merged. Admin only: it reads every series and
// every book's author, whoever owns them.
func (h *SeriesHandler) Duplicates(w http.ResponseWriter, r *http.Request) {
	groups, err := h.series.ListTitleDuplicates(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// DuplicateMergeGroup is one group of a bulk duplicate merge request.
type DuplicateMergeGroup struct {
	KeepID    int64   `json:"keepId"`
	SourceIDs []int64 `json:"sourceIds"`
}

// DuplicateMergeResult is what happened to one group. Plan is the merge it
// carried out, or with dryRun the one it would; Error says why the group was
// left as it was.
type DuplicateMergeResult struct {
	KeepID    int64               `json:"keepId"`
	SourceIDs []int64             `json:"sourceIds"`
	OK        bool                `json:"ok"`
	Error     string              `json:"error,omitempty"`
	Plan      *db.SeriesMergePlan `json:"plan,omitempty"`
}

// MergeDuplicates merges groups of duplicate series in one request (#2173):
// POST /series/duplicates/merge with {"groups":[{"keepId":1,"sourceIds":[2]}],
// "dryRun":true}. Each group goes through the single series merge
// (db.SeriesRepo.Merge, one transaction per group), so a group is merged
// whole or not at all, and one failing group does not stop the others. A
// group is refused when a series in it does not exist, appears in an earlier
// group, or does not share the keep series' title as the Audiobookshelf
// import compares titles: this endpoint clears duplicates, it is not a
// general purpose merge.
func (h *SeriesHandler) MergeDuplicates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Groups []DuplicateMergeGroup `json:"groups"`
		DryRun bool                  `json:"dryRun"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	switch {
	case len(body.Groups) == 0:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no groups to merge"})
		return
	case len(body.Groups) > maxDuplicateMergeGroups:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("at most %d groups per request", maxDuplicateMergeGroups)})
		return
	}

	merge := h.series.Merge
	if body.DryRun {
		merge = h.series.PlanMerge
	}
	used := map[int64]bool{}
	results := make([]DuplicateMergeResult, 0, len(body.Groups))
	merged, failed := 0, 0
	for _, g := range body.Groups {
		res := DuplicateMergeResult{KeepID: g.KeepID, SourceIDs: g.SourceIDs}
		if res.SourceIDs == nil {
			res.SourceIDs = []int64{}
		}
		msg, err := h.checkDuplicateGroup(r, g, used)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		if msg == "" {
			plan, err := merge(r.Context(), g.KeepID, g.SourceIDs, "")
			switch {
			case errors.Is(err, sql.ErrNoRows):
				msg = "series not found"
			case errors.Is(err, db.ErrSeriesMergeInvalid):
				msg = err.Error()
			case err != nil:
				// A failed write rolled its transaction back, so the group
				// is untouched; say so and carry on with the rest.
				slog.Warn("series duplicates: merge failed", "keep", g.KeepID, "sources", g.SourceIDs, "error", err)
				msg = "merge failed: " + err.Error()
			default:
				res.OK, res.Plan = true, plan
			}
		}
		// Every id of a group is spent once it has been tried, merged or not,
		// so a later group naming one of them is refused rather than run
		// against a series an earlier group deleted or was meant to.
		used[g.KeepID] = true
		for _, id := range g.SourceIDs {
			used[id] = true
		}
		if res.OK {
			merged++
		} else {
			res.Error = msg
			failed++
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dryRun":  body.DryRun,
		"merged":  merged,
		"failed":  failed,
		"results": results,
	})
}

// checkDuplicateGroup returns why a group cannot be merged, or "" when it can.
func (h *SeriesHandler) checkDuplicateGroup(r *http.Request, g DuplicateMergeGroup, used map[int64]bool) (string, error) {
	if len(g.SourceIDs) == 0 {
		return "no series to merge", nil
	}
	if used[g.KeepID] {
		return fmt.Sprintf("series %d is already in another group", g.KeepID), nil
	}
	keepTitle, ok, err := h.series.SeriesTitle(r.Context(), g.KeepID)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("series %d does not exist", g.KeepID), nil
	}
	key := seriesmatch.LookupKey(keepTitle)
	seen := map[int64]bool{}
	for _, id := range g.SourceIDs {
		switch {
		case id == g.KeepID:
			return "a series cannot be merged into itself", nil
		case seen[id]:
			return fmt.Sprintf("series %d listed twice", id), nil
		case used[id]:
			return fmt.Sprintf("series %d is already in another group", id), nil
		}
		seen[id] = true
		title, ok, err := h.series.SeriesTitle(r.Context(), id)
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("series %d does not exist", id), nil
		}
		if key == "" || seriesmatch.LookupKey(title) != key {
			return fmt.Sprintf("%q is not a duplicate of %q", title, keepTitle), nil
		}
	}
	return "", nil
}
