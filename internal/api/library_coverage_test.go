package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// TestLibraryScanStatusCoverage: 404 until a scan result is stored (and when
// the handler has no settings at all), then the stored JSON verbatim.
func TestLibraryScanStatusCoverage(t *testing.T) {
	get := func(h *LibraryHandler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ScanStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/library/scan/status", nil))
		return rec
	}
	if rec := get(NewLibraryHandler(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("no settings: %d, want 404", rec.Code)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	h := NewLibraryHandler(nil).WithSettings(settings)

	if rec := get(h); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no scan result available") {
		t.Fatalf("before any scan: %d %s, want 404", rec.Code, rec.Body.String())
	}
	const stored = `{"filesFound":3,"matched":2}`
	if err := settings.Set(t.Context(), "library.lastScan", stored); err != nil {
		t.Fatal(err)
	}
	rec := get(h)
	// The stored result comes back with the live running and queued state
	// added (#3014).
	const want = `{"filesFound":3,"matched":2,"queued":false,"running":false}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("after scan: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}

	database.Close()
	if rec := get(h); rec.Code != http.StatusInternalServerError {
		t.Fatalf("settings error: %d, want 500", rec.Code)
	}
}
