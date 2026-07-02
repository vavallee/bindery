package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vavallee/bindery/internal/grimmory"
)

// GrimmorySyncHandler exposes POST /grimmory/sync (start a bulk push of every
// imported ebook into Grimmory's BookDrop) and GET /grimmory/sync/status
// (poll the job's progress). Mirrors the Calibre sync surface. Routes sit
// behind RequireAdmin.
type GrimmorySyncHandler struct {
	syncer  grimmorySyncerAPI
	loadCfg func() grimmory.PushConfig
	lastFn  func(ctx context.Context) (time.Time, int, error)
}

// grimmorySyncerAPI is the subset of *grimmory.Syncer the API touches so
// tests can swap in a stub.
type grimmorySyncerAPI interface {
	Start(ctx context.Context, cfg grimmory.PushConfig) error
	Progress() grimmory.SyncProgress
}

func NewGrimmorySyncHandler(s grimmorySyncerAPI, loadCfg func() grimmory.PushConfig) *GrimmorySyncHandler {
	return &GrimmorySyncHandler{syncer: s, loadCfg: loadCfg}
}

// WithLastPush attaches the pushed-files aggregate used to enrich the status
// response with last-push info for the Settings tab.
func (h *GrimmorySyncHandler) WithLastPush(fn func(ctx context.Context) (time.Time, int, error)) *GrimmorySyncHandler {
	h.lastFn = fn
	return h
}

// grimmorySyncStatusResponse decorates the syncer progress with cumulative
// push state so the Settings tab can show "N files pushed, last at T" without
// a separate endpoint.
type grimmorySyncStatusResponse struct {
	grimmory.SyncProgress
	TotalPushedFiles int        `json:"totalPushedFiles"`
	LastPushedAt     *time.Time `json:"lastPushedAt,omitempty"`
}

// Start is POST /api/v1/grimmory/sync. Validates the integration is enabled
// and configured, then kicks off the bulk-push goroutine. 202 Accepted with
// the initial progress snapshot so the UI can go straight into polling.
//
// context.WithoutCancel is critical: the syncer walks every imported book and
// issues one upload per book — cancelling on response-send would routinely
// leave the job half-finished.
func (h *GrimmorySyncHandler) Start(w http.ResponseWriter, r *http.Request) {
	cfg := h.loadCfg()
	if ok, reason := cfg.Ready(); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": reason})
		return
	}

	err := h.syncer.Start(context.WithoutCancel(r.Context()), cfg)
	switch {
	case errors.Is(err, grimmory.ErrSyncAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusAccepted, h.status(r.Context()))
}

// Status is GET /api/v1/grimmory/sync/status. Cheap poll.
func (h *GrimmorySyncHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.status(r.Context()))
}

func (h *GrimmorySyncHandler) status(ctx context.Context) grimmorySyncStatusResponse {
	resp := grimmorySyncStatusResponse{SyncProgress: h.syncer.Progress()}
	if h.lastFn != nil {
		if last, count, err := h.lastFn(ctx); err == nil {
			resp.TotalPushedFiles = count
			if !last.IsZero() {
				resp.LastPushedAt = &last
			}
		}
	}
	return resp
}
