package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/models"
)

type stubABSImporter struct {
	startErr error
	lastCfg  abs.ImportConfig
	progress abs.ImportProgress
	runs     []models.ABSImportRun
	rollback *abs.RollbackResult
}

func (s *stubABSImporter) Start(_ context.Context, cfg abs.ImportConfig) error {
	s.lastCfg = cfg
	return s.startErr
}

func (s *stubABSImporter) Progress() abs.ImportProgress { return s.progress }
func (s *stubABSImporter) RecentRuns(context.Context, int) ([]models.ABSImportRun, error) {
	return s.runs, nil
}
func (s *stubABSImporter) RollbackPreview(context.Context, int64) (*abs.RollbackResult, error) {
	if s.rollback == nil {
		return &abs.RollbackResult{}, nil
	}
	return s.rollback, nil
}
func (s *stubABSImporter) Rollback(context.Context, int64) (*abs.RollbackResult, error) {
	if s.rollback == nil {
		return &abs.RollbackResult{}, nil
	}
	return s.rollback, nil
}

func TestABSImportHandler_Start(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{progress: abs.ImportProgress{Running: true, Message: "kickoff", RunID: 7}}
	type ctxKey struct{}
	key := ctxKey{}
	h := NewABSImportHandler(stub, func(ctx context.Context) ABSStoredConfig {
		if ctx.Value(key) != "request" {
			t.Fatalf("load config context value = %v, want request", ctx.Value(key))
		}
		return ABSStoredConfig{
			BaseURL:    "https://abs.example.com",
			APIKey:     "secret",
			Label:      "Shelf",
			LibraryID:  "lib-books",
			LibraryIDs: []string{"lib-books", "lib-audio"},
			Enabled:    true,
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", nil)
	req = req.WithContext(context.WithValue(req.Context(), key, "request"))
	h.Start(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if stub.lastCfg.LibraryID != "lib-books" || stub.lastCfg.BaseURL != "https://abs.example.com" {
		t.Fatalf("cfg = %+v", stub.lastCfg)
	}
	if got := stub.lastCfg.LibraryIDs; len(got) != 2 || got[0] != "lib-books" || got[1] != "lib-audio" {
		t.Fatalf("libraryIDs = %v, want stored ordered libraries", got)
	}
}

func TestABSImportHandler_StartUsesStoredConfigAndIgnoresDraftOverrides(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{progress: abs.ImportProgress{Running: true}}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:   "https://stored.example.com",
			APIKey:    "stored-secret",
			Label:     "Stored Shelf",
			LibraryID: "lib-stored",
			PathRemap: "/abs:/books",
			Enabled:   true,
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", bytes.NewBufferString(`{"baseUrl":"https://draft.example.com","apiKey":"draft-secret","libraryId":"lib-draft","label":"Draft Shelf","pathRemap":"/draft:/books","enabled":false}`))
	h.Start(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if stub.lastCfg.BaseURL != "https://stored.example.com" {
		t.Fatalf("baseURL = %q, want stored config", stub.lastCfg.BaseURL)
	}
	if stub.lastCfg.LibraryID != "lib-stored" {
		t.Fatalf("libraryID = %q, want stored config", stub.lastCfg.LibraryID)
	}
	if stub.lastCfg.Label != "Stored Shelf" {
		t.Fatalf("label = %q, want stored config", stub.lastCfg.Label)
	}
	if stub.lastCfg.APIKey != "stored-secret" {
		t.Fatalf("apiKey = %q, want stored config", stub.lastCfg.APIKey)
	}
	if stub.lastCfg.PathRemap != "/abs:/books" {
		t.Fatalf("pathRemap = %q, want stored config", stub.lastCfg.PathRemap)
	}
	if !stub.lastCfg.Enabled {
		t.Fatal("enabled = false, want stored config")
	}
}

func TestABSImportHandler_StartAcceptsDryRun(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{progress: abs.ImportProgress{Running: true, DryRun: true}}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:   "https://stored.example.com",
			APIKey:    "stored-secret",
			Label:     "Stored Shelf",
			LibraryID: "lib-books",
			Enabled:   true,
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", bytes.NewBufferString(`{"dryRun":true}`))
	h.Start(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !stub.lastCfg.DryRun {
		t.Fatal("expected dryRun to be forwarded")
	}
}

func TestABSImportHandler_StartRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{Enabled: false}
	})

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestABSImportHandler_StartAlreadyRunning(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{startErr: abs.ErrAlreadyRunning}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:   "https://abs.example.com",
			APIKey:    "secret",
			Label:     "Shelf",
			LibraryID: "lib-books",
			Enabled:   true,
		}
	})

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

// TestABSImportHandler_StartShuttingDown pins the answer for an import the jobs
// group refused because the process is draining (#2372). 400 would blame the
// request; nothing about it is wrong, it just arrived too late.
func TestABSImportHandler_StartShuttingDown(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{startErr: abs.ErrShuttingDown}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:   "https://abs.example.com",
			APIKey:    "secret",
			Label:     "Shelf",
			LibraryID: "lib-books",
			Enabled:   true,
		}
	})

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestABSImportHandler_Status(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{progress: abs.ImportProgress{Running: false, Processed: 3}}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got abs.ImportProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Processed != 3 {
		t.Fatalf("processed = %d, want 3", got.Processed)
	}
}

func TestABSImportHandler_StartBubblesImporterValidation(t *testing.T) {
	t.Parallel()

	stub := &stubABSImporter{startErr: errors.New("boom")}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{
			BaseURL:   "https://abs.example.com",
			APIKey:    "secret",
			Label:     "Shelf",
			LibraryID: "lib-books",
			Enabled:   true,
		}
	})

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
