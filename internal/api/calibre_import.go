package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/vavallee/bindery/internal/calibre"
)

// CalibreImportHandler exposes POST /calibre/import (kick off a library
// import in the background) and GET /calibre/import/status (poll the
// importer's current progress). Both routes sit behind the normal auth
// middleware; the handler itself is thin — all orchestration lives in
// the calibre.Importer.
type CalibreImportHandler struct {
	importer importerAPI
	loadCfg  func() calibre.Config
}

// importerAPI is the subset of *calibre.Importer the API touches, so tests
// can swap in a stub without wiring the full repo stack.
type importerAPI interface {
	Start(ctx context.Context, libraryPath string) error
	Progress() calibre.ImportProgress
}

func NewCalibreImportHandler(imp importerAPI, loadCfg func() calibre.Config) *CalibreImportHandler {
	return &CalibreImportHandler{importer: imp, loadCfg: loadCfg}
}

// Start is POST /api/v1/calibre/import. Validates that the library_path
// setting resolves to a real Calibre library, then kicks off the import
// goroutine. Returns 202 Accepted with the initial progress snapshot so
// the UI can go straight into polling without an extra round-trip.
//
// context.WithoutCancel is critical: the importer walks thousands of
// books and stamps `calibre.last_import_at` at the end — cancelling it
// on response-send would routinely leave the settings row stale.
func (h *CalibreImportHandler) Start(w http.ResponseWriter, r *http.Request) {
	cfg := h.loadCfg()
	if !cfg.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "calibre integration is disabled"})
		return
	}
	if cfg.LibraryPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "calibre library_path is empty"})
		return
	}

	err := h.importer.Start(context.WithoutCancel(r.Context()), cfg.LibraryPath)
	switch {
	case errors.Is(err, calibre.ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, h.importer.Progress())
}

// Status is GET /api/v1/calibre/import/status. Cheap, stateless poll —
// the importer tracks progress internally and hands back a fresh
// snapshot every call.
func (h *CalibreImportHandler) Status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.importer.Progress())
}
