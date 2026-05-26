package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/models"
)

type stubCalibreRuns struct {
	runs          []models.CalibreImportRun
	preview       *calibre.RollbackResult
	previewErr    error
	rollback      *calibre.RollbackResult
	rollbackErr   error
	lastPreviewID int64
	lastApplyID   int64
}

func (s *stubCalibreRuns) RecentRuns(_ context.Context, _ int) ([]models.CalibreImportRun, error) {
	return s.runs, nil
}

func (s *stubCalibreRuns) PreviewRollback(_ context.Context, runID int64) (*calibre.RollbackResult, error) {
	s.lastPreviewID = runID
	if s.previewErr != nil {
		return nil, s.previewErr
	}
	if s.preview == nil {
		return &calibre.RollbackResult{RunID: runID, Preview: true}, nil
	}
	return s.preview, nil
}

func (s *stubCalibreRuns) Rollback(_ context.Context, runID int64) (*calibre.RollbackResult, error) {
	s.lastApplyID = runID
	if s.rollbackErr != nil {
		return nil, s.rollbackErr
	}
	if s.rollback == nil {
		return &calibre.RollbackResult{RunID: runID, Applied: true, Status: "rolled_back"}, nil
	}
	return s.rollback, nil
}

func newCalibreRunsRouter(stub *stubCalibreRuns) http.Handler {
	r := chi.NewRouter()
	h := NewCalibreRunsHandler(stub)
	r.Get("/calibre/runs", h.List)
	r.Get("/calibre/runs/{runID}/rollback/preview", h.RollbackPreview)
	r.Post("/calibre/runs/{runID}/rollback", h.Rollback)
	return r
}

func TestCalibreRunsHandler_List(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{
		runs: []models.CalibreImportRun{{
			ID:          7,
			SourceID:    "default",
			LibraryPath: "/lib",
			Status:      "completed",
			StartedAt:   time.Now().UTC(),
		}},
	}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calibre/runs?limit=5", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []models.CalibreImportRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Errorf("unexpected response: %+v", got)
	}
}

func TestCalibreRunsHandler_ListEmpty(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calibre/runs", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got == "null\n" || got == "null" {
		t.Errorf("empty list serialized as null; want []. got=%q", got)
	}
}

func TestCalibreRunsHandler_RollbackPreview(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{
		preview: &calibre.RollbackResult{RunID: 42, Preview: true, Stats: calibre.RollbackStats{ActionsPlanned: 3}},
	}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calibre/runs/42/rollback/preview", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastPreviewID != 42 {
		t.Errorf("preview run id forwarded = %d, want 42", stub.lastPreviewID)
	}
	var got calibre.RollbackResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Stats.ActionsPlanned != 3 {
		t.Errorf("actionsPlanned = %d, want 3", got.Stats.ActionsPlanned)
	}
}

func TestCalibreRunsHandler_RollbackPreviewRunNotFound(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{previewErr: calibre.ErrRunNotFound}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calibre/runs/999/rollback/preview", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCalibreRunsHandler_RollbackApply(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{
		rollback: &calibre.RollbackResult{RunID: 8, Applied: true, Status: "rolled_back"},
	}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/calibre/runs/8/rollback", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastApplyID != 8 {
		t.Errorf("apply run id forwarded = %d, want 8", stub.lastApplyID)
	}
}

func TestCalibreRunsHandler_RollbackApplyAlreadyRolledBack(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{rollbackErr: calibre.ErrAlreadyRolledBack}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/calibre/runs/8/rollback", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestCalibreRunsHandler_RollbackApplyUnavailable(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{rollbackErr: calibre.ErrRollbackUnavailable}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/calibre/runs/8/rollback", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestCalibreRunsHandler_InvalidRunID(t *testing.T) {
	t.Parallel()
	stub := &stubCalibreRuns{}
	srv := newCalibreRunsRouter(stub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/calibre/runs/abc/rollback/preview", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
