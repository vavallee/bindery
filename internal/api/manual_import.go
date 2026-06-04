package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

type manualImportScanner interface {
	Lookup(ctx context.Context, path string) (importer.LookupResult, error)
	ImportFromPath(ctx context.Context, dl *models.Download, path, formatHint string)
}

// ManualImportHandler serves the manual-import lookup and trigger endpoints.
type ManualImportHandler struct {
	scanner   manualImportScanner
	downloads *db.DownloadRepo
	books     *db.BookRepo
	roots     *LibraryRoots
}

func NewManualImportHandler(scanner manualImportScanner, downloads *db.DownloadRepo, books *db.BookRepo) *ManualImportHandler {
	return &ManualImportHandler{scanner: scanner, downloads: downloads, books: books}
}

// WithRoots attaches the shared library-root containment checker. Both Lookup
// and Import reject paths that don't resolve under a configured root.
func (h *ManualImportHandler) WithRoots(r *LibraryRoots) *ManualImportHandler {
	h.roots = r
	return h
}

// Lookup handles GET /api/v1/queue/manual-import/lookup?path=...
// It parses the filename, searches the local catalogue, and returns a match
// result along with the auto-detected format. No state is modified.
func (h *ManualImportHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if path == "" || path == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}
	if !filepath.IsAbs(path) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be absolute"})
		return
	}
	resolved, ok := h.roots.ResolveContained(r.Context(), path)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path is outside the configured library roots"})
		return
	}
	// Operate on the symlink-resolved path so the containment check and the
	// catalogue lookup act on the same bytes (a symlink inside a root that
	// points outside it is rejected by ResolveContained above).
	path = resolved
	if _, err := os.Stat(path); err != nil { //nolint:gosec // #nosec G304 -- path is symlink-resolved and confirmed inside a configured library root; RequireAdmin middleware enforced at route level
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("path not accessible: %v", err)})
		return
	}
	result, err := h.scanner.Lookup(r.Context(), path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type manualImportRequest struct {
	Path   string `json:"path"`
	BookID int64  `json:"bookId"`
	// Format is optional. When omitted, the format is auto-detected from file
	// extensions. Accepted values: "ebook", "audiobook".
	Format string `json:"format"`
}

// Import handles POST /api/v1/queue/manual-import
// It validates the path and book, creates a synthetic Download record, and
// kicks off tryImportInternal asynchronously. Returns 202 with the new record.
func (h *ManualImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	var req manualImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.Path = filepath.Clean(req.Path)
	if req.Path == "" || req.Path == "." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be absolute"})
		return
	}
	resolved, ok := h.roots.ResolveContained(r.Context(), req.Path)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path is outside the configured library roots"})
		return
	}
	// Use the symlink-resolved path for every subsequent operation (stat,
	// book-file detection, import) so a symlink inside a root that points
	// outside it can't redirect the read/move to an arbitrary file.
	req.Path = resolved
	if req.BookID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bookId is required"})
		return
	}
	if req.Format != "" && req.Format != models.MediaTypeEbook && req.Format != models.MediaTypeAudiobook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be \"ebook\" or \"audiobook\""})
		return
	}

	info, err := os.Stat(req.Path) //nolint:gosec // #nosec G304 -- path is symlink-resolved and confirmed inside a configured library root; RequireAdmin middleware enforced at route level
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("path not accessible: %v", err)})
		return
	}
	if !info.IsDir() && !importer.IsBookFile(req.Path) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is not a recognised book file"})
		return
	}

	book, err := h.books.GetByID(r.Context(), req.BookID)
	if err != nil || book == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "book not found"})
		return
	}

	now := time.Now().UTC()
	dl := &models.Download{
		GUID:   "manual-" + uuid.New().String(),
		BookID: &req.BookID,
		Title:  book.Title,
		Status: models.StateCompleted,
	}
	dl.CompletedAt = &now

	if err := h.downloads.Create(r.Context(), dl); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create import record"})
		return
	}

	go h.scanner.ImportFromPath(context.WithoutCancel(r.Context()), dl, req.Path, req.Format)

	writeJSON(w, http.StatusAccepted, dl)
}
