package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/grimmory"
)

type stubGrimmorySyncer struct {
	startErr error
	started  int
	progress grimmory.SyncProgress
}

func (s *stubGrimmorySyncer) Start(context.Context, grimmory.PushConfig) error {
	if s.startErr == nil {
		s.started++
	}
	return s.startErr
}

func (s *stubGrimmorySyncer) Progress() grimmory.SyncProgress { return s.progress }

func readyGrimmoryCfg() grimmory.PushConfig {
	return grimmory.PushConfig{Enabled: true, BaseURL: "http://grimmory:6060", Username: "u", Password: "p"}
}

func TestGrimmorySyncStart_NotConfigured(t *testing.T) {
	h := NewGrimmorySyncHandler(&stubGrimmorySyncer{}, func() grimmory.PushConfig {
		return grimmory.PushConfig{} // disabled
	})
	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/grimmory/sync", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("body = %q, want a disabled reason", rec.Body.String())
	}
}

func TestGrimmorySyncStart_Accepted(t *testing.T) {
	stub := &stubGrimmorySyncer{}
	last := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	h := NewGrimmorySyncHandler(stub, readyGrimmoryCfg).
		WithLastPush(func(context.Context) (time.Time, int, error) { return last, 3, nil })

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/grimmory/sync", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if stub.started != 1 {
		t.Fatalf("syncer started %d times, want 1", stub.started)
	}
	var resp struct {
		TotalPushedFiles int        `json:"totalPushedFiles"`
		LastPushedAt     *time.Time `json:"lastPushedAt"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalPushedFiles != 3 || resp.LastPushedAt == nil || !resp.LastPushedAt.Equal(last) {
		t.Errorf("last-push decoration = %+v, want 3 files at %v", resp, last)
	}
}

func TestGrimmorySyncStart_AlreadyRunningConflicts(t *testing.T) {
	stub := &stubGrimmorySyncer{startErr: grimmory.ErrSyncAlreadyRunning}
	h := NewGrimmorySyncHandler(stub, readyGrimmoryCfg)

	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/grimmory/sync", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestGrimmorySyncStatus(t *testing.T) {
	stub := &stubGrimmorySyncer{progress: grimmory.SyncProgress{Running: true, Message: "pushing"}}
	h := NewGrimmorySyncHandler(stub, readyGrimmoryCfg)

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/v1/grimmory/sync/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"running":true`) {
		t.Errorf("body = %q, want running progress", rec.Body.String())
	}
}
