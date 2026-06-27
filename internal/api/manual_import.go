package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// prepareImport validates a single (path, bookId, format) request, resolves the
// path inside a library root, confirms the book exists, and creates the
// synthetic Download record. On success it returns the record and the
// symlink-resolved path. On failure it returns an HTTP status and a message.
// It is the shared core of Import (one item) and ImportBatch (many).
func (h *ManualImportHandler) prepareImport(ctx context.Context, rawPath string, bookID int64, format string) (*models.Download, string, int, string) {
	path := filepath.Clean(rawPath)
	if path == "" || path == "." {
		return nil, "", http.StatusBadRequest, "path is required"
	}
	if !filepath.IsAbs(path) {
		return nil, "", http.StatusBadRequest, "path must be absolute"
	}
	// Resolve symlinks and confirm containment so a symlink inside a root that
	// points outside it can't redirect the read/move to an arbitrary file.
	resolved, ok := h.roots.ResolveContained(ctx, path)
	if !ok {
		return nil, "", http.StatusForbidden, "path is outside the configured library roots"
	}
	path = resolved
	if bookID <= 0 {
		return nil, "", http.StatusBadRequest, "bookId is required"
	}
	if format != "" && format != models.MediaTypeEbook && format != models.MediaTypeAudiobook {
		return nil, "", http.StatusBadRequest, "format must be \"ebook\" or \"audiobook\""
	}
	info, err := os.Stat(path) //nolint:gosec // #nosec G304 -- path is symlink-resolved and confirmed inside a configured library root; RequireAdmin middleware enforced at route level
	if err != nil {
		return nil, "", http.StatusBadRequest, fmt.Sprintf("path not accessible: %v", err)
	}
	if !info.IsDir() && !importer.IsBookFile(path) {
		return nil, "", http.StatusBadRequest, "path is not a recognised book file"
	}
	book, err := h.books.GetByID(ctx, bookID)
	if err != nil || book == nil {
		return nil, "", http.StatusBadRequest, "book not found"
	}
	now := time.Now().UTC()
	dl := &models.Download{
		GUID:   "manual-" + uuid.New().String(),
		BookID: &bookID,
		Title:  book.Title,
		Status: models.StateCompleted,
	}
	dl.CompletedAt = &now
	if err := h.downloads.Create(ctx, dl); err != nil {
		return nil, "", http.StatusInternalServerError, "failed to create import record"
	}
	return dl, path, 0, ""
}

// Import handles POST /api/v1/queue/manual-import
// It validates the path and book, creates a synthetic Download record, and
// kicks off the import asynchronously. Returns 202 with the new record.
func (h *ManualImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	var req manualImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	dl, path, status, msg := h.prepareImport(r.Context(), req.Path, req.BookID, req.Format)
	if dl == nil {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	go h.scanner.ImportFromPath(context.WithoutCancel(r.Context()), dl, path, req.Format)
	writeJSON(w, http.StatusAccepted, dl)
}

// reassignRequest moves a single already-imported file to a different book
// ("Fix Match", #1238) when the importer attached it to the wrong one.
type reassignRequest struct {
	// Path is the current on-disk path of the mis-matched file (book_files.path).
	Path string `json:"path"`
	// TargetBookID is the book the file should belong to.
	TargetBookID int64 `json:"targetBookId"`
	// Format is optional; auto-detected when empty. "ebook" or "audiobook".
	Format string `json:"format"`
}

// Reassign handles POST /api/v1/queue/manual-import/reassign.
//
// It detaches the file from whatever book currently owns it and re-imports it
// against TargetBookID, reusing the manual-import move/attach path so the file
// is renamed into the correct book's library folder rather than just re-pointed
// in the database. Returns 202 with the synthetic Download record that drives
// the async import.
func (h *ManualImportHandler) Reassign(w http.ResponseWriter, r *http.Request) {
	var req reassignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	// Validate path containment, confirm the target book exists, and create the
	// synthetic Download record. Nothing is mutated on failure.
	dl, path, status, msg := h.prepareImport(r.Context(), req.Path, req.TargetBookID, req.Format)
	if dl == nil {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	// Detach the stale association from the book that currently owns this path.
	// RemoveBookFile is status-aware (flips a now-fileless source book back to
	// "wanted") and is a no-op when the path isn't tracked. Best-effort: even if
	// it fails, the re-import below still re-points the file to the target.
	if _, err := h.books.RemoveBookFile(r.Context(), path); err != nil {
		slog.Warn("reassign: detach source book file", "path", path, "error", err)
	}
	ctx := context.WithoutCancel(r.Context())
	targetID := req.TargetBookID
	go func() {
		h.scanner.ImportFromPath(ctx, dl, path, req.Format)
		h.removeStaleSource(ctx, path, targetID)
	}()
	writeJSON(w, http.StatusAccepted, dl)
}

// removeStaleSource deletes the original mis-filed copy after a successful
// reassign. The import places the file under the target book (by hardlink or
// copy), leaving the original where it was — which a later library scan would
// re-attach to the wrong book. We remove it, but only once we can confirm the
// import actually put the file somewhere else: the target must now have a
// tracked file at a different path that exists on disk. If the import failed
// (no new file) or resolved to the same path, the source is left untouched so a
// file is never lost.
func (h *ManualImportHandler) removeStaleSource(ctx context.Context, src string, targetID int64) {
	files, err := h.books.ListBookFiles(ctx, targetID)
	if err != nil {
		slog.Warn("reassign cleanup: list target files", "error", err)
		return
	}
	moved := false
	for _, f := range files {
		if f.Path == src {
			return // target ended up at the same path; nothing to clean up
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			moved = true
		}
	}
	if !moved {
		return // import did not place a new file; leave the source in place
	}
	if err := os.Remove(src); err != nil {
		slog.Warn("reassign cleanup: remove stale source file", "path", src, "error", err)
		return
	}
	_ = os.Remove(filepath.Dir(src)) // best-effort: prune the now-empty folder
}

// ScanItem is one candidate book unit discovered under a folder during Scan.
type ScanItem struct {
	Path           string        `json:"path"`
	Name           string        `json:"name"`
	Match          string        `json:"match"` // "confident" | "ambiguous" | "none"
	ParsedTitle    string        `json:"parsedTitle"`
	ParsedAuthor   string        `json:"parsedAuthor"`
	DetectedFormat string        `json:"detectedFormat"`
	Book           *models.Book  `json:"book,omitempty"`
	Candidates     []models.Book `json:"candidates,omitempty"`
}

// ScanResponse is the result of scanning a folder for importable book units.
type ScanResponse struct {
	Items     []ScanItem `json:"items"`
	Truncated bool       `json:"truncated"`
}

// maxScanEntries caps how many child units a single Scan returns so pointing at
// an enormous tree can't produce an unbounded response or stall the request.
const maxScanEntries = 1000

// Scan handles GET /api/v1/queue/manual-import/scan?path=...
// It enumerates the immediate children of a folder (each book file, and each
// subdirectory that contains at least one book file) and runs the same
// catalogue Lookup on each, returning a per-unit match list to review and
// bulk-import. No state is modified.
func (h *ManualImportHandler) Scan(w http.ResponseWriter, r *http.Request) {
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
	path = resolved
	info, err := os.Stat(path) //nolint:gosec // #nosec G304 -- symlink-resolved and confirmed inside a configured library root; RequireAdmin enforced at route level
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("path not accessible: %v", err)})
		return
	}
	if !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be a folder; use lookup for a single book"})
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("read folder: %v", err)})
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	resp := ScanResponse{Items: []ScanItem{}}
	for _, e := range entries {
		if len(resp.Items) >= maxScanEntries {
			resp.Truncated = true
			break
		}
		child := filepath.Join(path, e.Name())
		isDir := e.IsDir()
		if !isDir && !importer.IsBookFile(child) {
			continue
		}
		if isDir && !dirHasBookFile(child) {
			continue
		}
		res, err := h.scanner.Lookup(r.Context(), child)
		if err != nil {
			continue
		}
		resp.Items = append(resp.Items, ScanItem{
			Path:           child,
			Name:           e.Name(),
			Match:          res.Match,
			ParsedTitle:    res.ParsedTitle,
			ParsedAuthor:   res.ParsedAuthor,
			DetectedFormat: res.DetectedFormat,
			Book:           res.Book,
			Candidates:     res.Candidates,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// dirHasBookFile reports whether dir contains at least one recognized book
// file, walking up to a bounded number of entries so a deep or huge tree can't
// stall the scan.
func dirHasBookFile(dir string) bool {
	const limit = 5000
	count := 0
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abort the walk
		}
		count++
		if count > limit {
			return fs.SkipAll
		}
		if !d.IsDir() && importer.IsBookFile(p) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// BatchImportItem is one (path, bookId, format) pair in a batch import.
type BatchImportItem struct {
	Path   string `json:"path"`
	BookID int64  `json:"bookId"`
	Format string `json:"format"`
}

// BatchImportResult reports the per-item outcome of validation. The actual
// import runs asynchronously after a successful validation (Accepted=true).
type BatchImportResult struct {
	Path       string `json:"path"`
	Accepted   bool   `json:"accepted"`
	Error      string `json:"error,omitempty"`
	DownloadID int64  `json:"downloadId,omitempty"`
}

// BatchImportResponse summarizes a batch import submission.
type BatchImportResponse struct {
	Results  []BatchImportResult `json:"results"`
	Accepted int                 `json:"accepted"`
	Failed   int                 `json:"failed"`
}

const (
	// maxBatchItems caps a single batch so one request can't enqueue an
	// unbounded number of imports.
	maxBatchItems = 1000
	// batchImportConcurrency bounds how many imports run at once in the
	// background, so a 500-item batch doesn't fan out 500 concurrent file moves.
	batchImportConcurrency = 4
)

// ImportBatch handles POST /api/v1/queue/manual-import/batch
// Body: a JSON array of {path, bookId, format}. Each item is validated and gets
// a Download record synchronously; the file imports then run in the background
// with bounded concurrency. Returns 202 with the per-item validation results.
func (h *ManualImportHandler) ImportBatch(w http.ResponseWriter, r *http.Request) {
	var items []BatchImportItem
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no items to import"})
		return
	}
	if len(items) > maxBatchItems {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many items (max %d)", maxBatchItems)})
		return
	}

	type job struct {
		dl     *models.Download
		path   string
		format string
	}
	var jobs []job
	resp := BatchImportResponse{Results: make([]BatchImportResult, 0, len(items))}
	for _, it := range items {
		dl, path, _, msg := h.prepareImport(r.Context(), it.Path, it.BookID, it.Format)
		if dl == nil {
			resp.Results = append(resp.Results, BatchImportResult{Path: it.Path, Accepted: false, Error: msg})
			resp.Failed++
			continue
		}
		jobs = append(jobs, job{dl: dl, path: path, format: it.Format})
		resp.Results = append(resp.Results, BatchImportResult{Path: it.Path, Accepted: true, DownloadID: dl.ID})
		resp.Accepted++
	}

	if len(jobs) > 0 {
		ctx := context.WithoutCancel(r.Context())
		go func() {
			sem := make(chan struct{}, batchImportConcurrency)
			var wg sync.WaitGroup
			for _, j := range jobs {
				sem <- struct{}{}
				wg.Add(1)
				go func(j job) {
					defer wg.Done()
					defer func() { <-sem }()
					h.scanner.ImportFromPath(ctx, j.dl, j.path, j.format)
				}(j)
			}
			wg.Wait()
		}()
	}

	writeJSON(w, http.StatusAccepted, resp)
}
