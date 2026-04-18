package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/vavallee/bindery/internal/calibre"
)

// CalibreSyncHandler exposes POST /calibre/sync (start a bulk push of
// every imported book into Calibre) and GET /calibre/sync/status (poll
// the job's progress). Routes sit behind the normal auth middleware.
type CalibreSyncHandler struct {
	syncer   syncerAPI
	loadCfg  func() calibre.Config
	loadMode func() calibre.Mode
}

// syncerAPI is the subset of *calibre.Syncer the API touches so tests can
// swap in a stub without wiring the full repo stack.
type syncerAPI interface {
	Start(ctx context.Context, cfg calibre.Config, mode calibre.Mode) error
	Progress() calibre.SyncProgress
}

func NewCalibreSyncHandler(s syncerAPI, loadCfg func() calibre.Config, loadMode func() calibre.Mode) *CalibreSyncHandler {
	return &CalibreSyncHandler{syncer: s, loadCfg: loadCfg, loadMode: loadMode}
}

// Start is POST /api/v1/calibre/sync. Validates that plugin mode is
// selected and the plugin URL is set, then kicks off the bulk-push
// goroutine. 202 Accepted with the initial progress snapshot so the UI
// can go straight into polling.
//
// context.WithoutCancel is critical: the syncer walks every imported
// book and issues one HTTP call per book — cancelling on response-send
// would routinely leave the job half-finished.
func (h *CalibreSyncHandler) Start(w http.ResponseWriter, r *http.Request) {
	mode := h.loadMode()
	if mode != calibre.ModePlugin {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "calibre mode is not 'plugin' — bulk sync only targets the Bindery Bridge plugin"})
		return
	}
	cfg := h.loadCfg()
	if cfg.PluginURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "calibre plugin_url is empty"})
		return
	}

	err := h.syncer.Start(context.WithoutCancel(r.Context()), cfg, mode)
	switch {
	case errors.Is(err, calibre.ErrSyncAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, calibre.ErrSyncModeNotPlugin):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, h.syncer.Progress())
}

// Status is GET /api/v1/calibre/sync/status. Cheap, stateless poll.
func (h *CalibreSyncHandler) Status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.syncer.Progress())
}
