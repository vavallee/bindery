package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/models"
)

type stubABSImporter struct {
	startErr error
	lastCfg  abs.ImportConfig
	progress abs.ImportProgress
	runs     []models.ABSImportRun
	// gotLimit and gotOffset record the paging the last RunsPage call asked for.
	gotLimit, gotOffset int
	rollback            *abs.RollbackResult
	// rollbackErr, when set, is returned by RollbackPreview and Rollback.
	rollbackErr error
}

func (s *stubABSImporter) Start(_ context.Context, cfg abs.ImportConfig) error {
	s.lastCfg = cfg
	return s.startErr
}

func (s *stubABSImporter) Progress() abs.ImportProgress { return s.progress }
func (s *stubABSImporter) RunsPage(_ context.Context, limit, offset int) ([]models.ABSImportRun, error) {
	s.gotLimit, s.gotOffset = limit, offset
	if offset >= len(s.runs) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.runs) {
		end = len(s.runs)
	}
	return s.runs[offset:end], nil
}
func (s *stubABSImporter) RollbackPreview(context.Context, int64) (*abs.RollbackResult, error) {
	if s.rollbackErr != nil {
		return nil, s.rollbackErr
	}
	if s.rollback == nil {
		return &abs.RollbackResult{}, nil
	}
	return s.rollback, nil
}
func (s *stubABSImporter) Rollback(context.Context, int64) (*abs.RollbackResult, error) {
	if s.rollbackErr != nil {
		return nil, s.rollbackErr
	}
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

func TestABSImportHandler_StartUnclassifiedImporterErrorIsGeneric500(t *testing.T) {
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
	// The handler validates the config itself before calling Start, and the
	// real importer's Start returns only its two sentinels, so an error it did
	// not classify is a fault underneath it, not a bad request.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestABSImportHandler_RunsHonoursPaging pins #2173: the runs list used to
// ignore limit and offset and always return the newest 10 rows, so a run over
// 26 libraries could not be seen past its last 10.
func TestABSImportHandler_RunsHonoursPaging(t *testing.T) {
	t.Parallel()

	runs := make([]models.ABSImportRun, 26)
	for i := range runs {
		runs[i] = models.ABSImportRun{ID: int64(100 - i), SourceID: "default", Status: "completed"}
	}
	stub := &stubABSImporter{runs: runs}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })

	cases := []struct {
		query       string
		wantLimit   int
		wantOffset  int
		wantLen     int
		wantFirstID int64
	}{
		{"", 10, 0, 10, 100},                     // defaults unchanged
		{"?limit=30", 30, 0, 26, 100},            // more than 10 now comes back
		{"?offset=10", 10, 10, 10, 90},           // offset moves the window
		{"?limit=5&offset=20", 5, 20, 5, 80},     // both together
		{"?limit=1000", 200, 0, 26, 100},         // clamped to the ceiling
		{"?limit=abc&offset=-4", 10, 0, 10, 100}, // bad values fall back like other lists
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/runs"+tc.query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d", tc.query, rec.Code)
		}
		var got []abs.PersistedImportRun
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%q: decode: %v", tc.query, err)
		}
		if stub.gotLimit != tc.wantLimit || stub.gotOffset != tc.wantOffset {
			t.Errorf("%q: importer asked for limit=%d offset=%d, want %d/%d", tc.query, stub.gotLimit, stub.gotOffset, tc.wantLimit, tc.wantOffset)
		}
		if len(got) != tc.wantLen || got[0].ID != tc.wantFirstID {
			t.Errorf("%q: got %d runs, want %d starting at id %d", tc.query, len(got), tc.wantLen, tc.wantFirstID)
		}
	}

	rec := httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/runs?offset=26", nil))
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != "[]" {
		t.Fatalf("offset past the end = %d %q, want 200 []", rec.Code, body)
	}
}
