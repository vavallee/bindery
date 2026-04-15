package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/calibre"
)

// stubImporter implements importerAPI so the handler's contract can be
// exercised without touching the real repo stack. Each test constructs
// one inline and asserts on call args + response codes.
type stubImporter struct {
	startErr  error
	progress  calibre.ImportProgress
	lastCalls int
	lastPath  string
}

func (s *stubImporter) Start(_ context.Context, libraryPath string) error {
	s.lastCalls++
	s.lastPath = libraryPath
	return s.startErr
}
func (s *stubImporter) Progress() calibre.ImportProgress { return s.progress }

func loader(cfg calibre.Config) func() calibre.Config { return func() calibre.Config { return cfg } }

func TestCalibreImport_Start_RejectsDisabled(t *testing.T) {
	s := &stubImporter{}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: false}))

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/import", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if s.lastCalls != 0 {
		t.Error("importer must not be invoked when disabled")
	}
}

func TestCalibreImport_Start_RejectsMissingLibraryPath(t *testing.T) {
	s := &stubImporter{}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: true, LibraryPath: ""}))

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/import", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestCalibreImport_Start_Accepted(t *testing.T) {
	s := &stubImporter{progress: calibre.ImportProgress{Running: true, Message: "kickoff"}}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: true, LibraryPath: "/lib"}))

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/import", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
	if s.lastCalls != 1 || s.lastPath != "/lib" {
		t.Errorf("Start not invoked correctly: calls=%d path=%q", s.lastCalls, s.lastPath)
	}
	// Response body must be the progress snapshot so the UI can start
	// polling immediately.
	var got calibre.ImportProgress
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !got.Running || got.Message != "kickoff" {
		t.Errorf("progress not echoed in body: %+v", got)
	}
}

func TestCalibreImport_Start_Conflict(t *testing.T) {
	s := &stubImporter{startErr: calibre.ErrAlreadyRunning}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: true, LibraryPath: "/lib"}))

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/import", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", rec.Code)
	}
}

func TestCalibreImport_Start_UnknownError(t *testing.T) {
	s := &stubImporter{startErr: errors.New("boom")}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: true, LibraryPath: "/lib"}))

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/import", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestCalibreImport_Status_ReturnsProgress(t *testing.T) {
	s := &stubImporter{progress: calibre.ImportProgress{Total: 5, Processed: 3}}
	h := NewCalibreImportHandler(s, loader(calibre.Config{Enabled: true, LibraryPath: "/lib"}))

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/v1/calibre/import/status", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	var got calibre.ImportProgress
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.Total != 5 || got.Processed != 3 {
		t.Errorf("progress = %+v", got)
	}
}
