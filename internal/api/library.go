package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
)

// libraryScanner is the subset of importer.Scanner used by LibraryHandler.
type libraryScanner interface {
	ScanLibrary(ctx context.Context)
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
// progress via the book list.
func (h *LibraryHandler) Scan(w http.ResponseWriter, r *http.Request) {
	// context.WithoutCancel so the goroutine isn't killed when the HTTP
	// response is sent and the request context is cancelled.
	go h.scanner.ScanLibrary(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "library scan started"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if setting == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no scan result available"})
		return
	}
	// The value is already a JSON string — write it directly.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(setting.Value)); err != nil {
		slog.Warn("failed to write library scan status", "error", err)
	}
}
