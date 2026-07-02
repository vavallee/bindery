package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/models"
)

// calibreRunsAPI is the subset of *calibre.Importer this handler depends
// on, narrowed for testability.
type calibreRunsAPI interface {
	RecentRuns(ctx context.Context, limit int) ([]models.CalibreImportRun, error)
	PreviewRollback(ctx context.Context, runID int64) (*calibre.RollbackResult, error)
	Rollback(ctx context.Context, runID int64) (*calibre.RollbackResult, error)
}

// CalibreRunsHandler serves the run-listing + rollback endpoints introduced
// in #643. Sits alongside CalibreImportHandler / CalibreSyncHandler rather
// than folding into them so each handler maps cleanly to one URL prefix.
type CalibreRunsHandler struct {
	importer calibreRunsAPI
}

func NewCalibreRunsHandler(importer calibreRunsAPI) *CalibreRunsHandler {
	return &CalibreRunsHandler{importer: importer}
}

// List returns the most recent Calibre import runs. Pagination is via
// ?limit (default 20, max 100).
func (h *CalibreRunsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := parseLimitOffset(r, 20, 100)
	if limit <= 0 {
		limit = 20
	}
	runs, err := h.importer.RecentRuns(r.Context(), limit)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if runs == nil {
		runs = []models.CalibreImportRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *CalibreRunsHandler) RollbackPreview(w http.ResponseWriter, r *http.Request) {
	runID, err := calibreRunID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := h.importer.PreviewRollback(r.Context(), runID)
	switch {
	case errors.Is(err, calibre.ErrRunNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, calibre.ErrRollbackUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *CalibreRunsHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	runID, err := calibreRunID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := h.importer.Rollback(r.Context(), runID)
	switch {
	case errors.Is(err, calibre.ErrRunNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, calibre.ErrAlreadyRolledBack):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, calibre.ErrRollbackUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func calibreRunID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(chi.URLParam(r, "runID"))
	if raw == "" {
		return 0, errors.New("run id is required")
	}
	runID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || runID <= 0 {
		return 0, errors.New("invalid run id")
	}
	return runID, nil
}
