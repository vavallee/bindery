package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
)

// libraryScanner is the subset of importer.Scanner used by LibraryHandler.
type libraryScanner interface {
	// StartScanTracked starts or queues a scan and names the scan_id its
	// result will carry (#3014).
	StartScanTracked(ctx context.Context) (string, error)
	// ScanState reports whether a scan is walking and whether another is
	// queued behind it.
	ScanState() (running, queued bool)
}

type LibraryHandler struct {
	scanner  libraryScanner
	settings *db.SettingsRepo
}

func NewLibraryHandler(scanner *importer.Scanner) *LibraryHandler {
	return &LibraryHandler{scanner: scanner}
}

// WithSettings attaches a SettingsRepo so the handler can serve scan status.
func (h *LibraryHandler) WithSettings(sr *db.SettingsRepo) *LibraryHandler {
	h.settings = sr
	return h
}

// Scan triggers an immediate library reconciliation in the background and
// returns 202 Accepted. The scan runs asynchronously; clients can monitor
// progress via the book list. If a scan is already in flight (manual or the
// scheduled 6-hourly job) it returns 409 Conflict instead of starting a
// second concurrent full walk (#1460). If the process is shutting down it
// returns 503 rather than 202 for a scan that will never run (#2372).
func (h *LibraryHandler) Scan(w http.ResponseWriter, r *http.Request) {
	// context.WithoutCancel so the scan goroutine isn't killed when the HTTP
	// response is sent and the request context is cancelled.
	scanID, err := h.scanner.StartScanTracked(context.WithoutCancel(r.Context()))
	if errors.Is(err, importer.ErrScanQueued) {
		// A scan is already walking. The request is not dropped: one more scan
		// runs as soon as it finishes, so a file placed behind the walk is
		// still picked up (#3014). 202, because the request will be honoured.
		// scanId is the id that scan's result will carry in scan/status.
		writeJSON(w, http.StatusAccepted, map[string]any{"message": "library scan queued", "queued": true, "scanId": scanID})
		return
	}
	if errors.Is(err, importer.ErrScanAlreadyRunning) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, importer.ErrShuttingDown) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message": "library scan started", "scanId": scanID})
}

// ScanStatus returns the result of the last library scan, stored as a JSON
// string in the settings table under "library.lastScan". Returns 404 if no
// scan has run yet.
func (h *LibraryHandler) ScanStatus(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no scan result available"})
		return
	}
	setting, err := h.settings.Get(r.Context(), "library.lastScan")
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if setting == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no scan result available"})
		return
	}
	// The stored value is the last finished scan. Add whether one is walking
	// now and whether another is queued, so a client waiting on its scan can
	// tell "still working" from "nothing happened" (#3014).
	var body map[string]any
	if err := json.Unmarshal([]byte(setting.Value), &body); err != nil || body == nil {
		// Not an object: hand it back untouched, as before.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(setting.Value)); err != nil {
			slog.Warn("failed to write library scan status", "error", err)
		}
		return
	}
	body["running"], body["queued"] = h.scanner.ScanState()
	writeJSON(w, http.StatusOK, body)
}
