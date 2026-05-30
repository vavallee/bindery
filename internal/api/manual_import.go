package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	scanner        manualImportScanner
	downloads      *db.DownloadRepo
	books          *db.BookRepo
	allowedRoots   []string
	rootFolderRepo *db.RootFolderRepo
}

func NewManualImportHandler(scanner manualImportScanner, downloads *db.DownloadRepo, books *db.BookRepo) *ManualImportHandler {
	return &ManualImportHandler{scanner: scanner, downloads: downloads, books: books}
}

// WithAllowedRoots registers static filesystem roots (e.g. cfg.LibraryDir, cfg.AudiobookDir)
// that paths submitted to Lookup and Import must fall under.
func (h *ManualImportHandler) WithAllowedRoots(roots ...string) *ManualImportHandler {
	for _, r := range roots {
		if r != "" {
			h.allowedRoots = append(h.allowedRoots, filepath.Clean(r))
		}
	}
	return h
}

// WithRootFolderRepo attaches the root-folder repo so dynamically configured
// root folders are also enforced by the path containment check.
func (h *ManualImportHandler) WithRootFolderRepo(rf *db.RootFolderRepo) *ManualImportHandler {
	h.rootFolderRepo = rf
	return h
}

// isAllowedPath reports whether p falls under a configured library root.
// It resolves symlinks via filepath.EvalSymlinks before comparing so that
// symlink-traversal escapes are caught. When no roots are configured, all
// paths are permitted — this preserves behaviour for installs without a
// library dir and for tests that don't wire roots.
func (h *ManualImportHandler) isAllowedPath(ctx context.Context, p string) bool {
	roots := make([]string, len(h.allowedRoots))
	copy(roots, h.allowedRoots)
	if h.rootFolderRepo != nil {
		if dynamic, err := h.rootFolderRepo.List(ctx); err == nil {
			for _, rf := range dynamic {
				if rf.Path != "" {
					roots = append(roots, filepath.Clean(rf.Path))
				}
			}
		}
	}
	if len(roots) == 0 {
		return true
	}
	resolved := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		resolved = r
	}
	for _, root := range roots {
		if root == "" || root == "." {
			continue
		}
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
	if !h.isAllowedPath(r.Context(), path) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path is outside the configured library roots"})
		return
	}
	if _, err := os.Stat(path); err != nil { //nolint:gosec // #nosec G304 -- path is cleaned and validated as absolute; RequireAdmin middleware enforced at route level
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
	if !h.isAllowedPath(r.Context(), req.Path) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path is outside the configured library roots"})
		return
	}
	if req.BookID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bookId is required"})
		return
	}
	if req.Format != "" && req.Format != models.MediaTypeEbook && req.Format != models.MediaTypeAudiobook {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be \"ebook\" or \"audiobook\""})
		return
	}

	info, err := os.Stat(req.Path) //nolint:gosec // #nosec G304 -- path is cleaned and validated as absolute; RequireAdmin middleware enforced at route level
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
