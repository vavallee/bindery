package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/migrate"
	"github.com/vavallee/bindery/internal/models"
)

// allowedUploadCT are the Content-Type values the migrate endpoints accept.
// Browsers commonly send application/octet-stream for .db files; curl defaults
// to application/x-sqlite3 or application/vnd.sqlite3 for readarr.db.
var allowedUploadCT = map[string]bool{
	"text/csv":                 true,
	"application/csv":          true,
	"application/octet-stream": true,
	"application/x-sqlite3":    true,
	"application/vnd.sqlite3":  true,
	"application/x-sqlite":     true,
	"":                         true, // some multipart clients omit Content-Type per-part
}

// uploadTempDir returns a directory under the data root for short-lived upload
// spools, falling back to os.TempDir() only when no data dir is configured.
// The sticky /tmp default is OK on Linux, but putting spools next to the DB
// keeps them on the same volume (avoids cross-FS rename) and inherits whatever
// ownership/perms the operator set on the data dir.
func uploadTempDir() string {
	p := strings.TrimSpace(os.Getenv("BINDERY_DB_PATH"))
	if p == "" {
		return ""
	}
	// BINDERY_DB_PATH is operator-controlled via env/secret, not request
	// input — gosec taint analysis can't distinguish the two.
	dir := filepath.Clean(filepath.Join(filepath.Dir(p), "tmp"))
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec
		return ""
	}
	return dir
}

// MigrateHandler exposes bulk-import endpoints under /api/v1/migrate.
type MigrateHandler struct {
	authors   *db.AuthorRepo
	indexers  *db.IndexerRepo
	clients   *db.DownloadClientRepo
	blocklist *db.BlocklistRepo
	books     *db.BookRepo
	meta      *metadata.Aggregator

	// onNewAuthor fires in a goroutine for each newly-imported author so
	// the book-fetch-on-add behaviour from the AddAuthor flow is preserved.
	onNewAuthor func(author *models.Author)

	// readarrImporter manages async Readarr DB imports so the HTTP handler
	// can return 202 immediately instead of blocking for minutes while
	// OpenLibrary metadata is resolved for each author.
	readarrImporter *migrate.ReadarrImporter

	// goodreadsImporter coordinates the two-step Goodreads CSV import:
	// a dry-run preview followed by a commit of the resolved books.
	goodreadsImporter *migrate.GoodreadsImporter
}

func NewMigrateHandler(
	authors *db.AuthorRepo,
	indexers *db.IndexerRepo,
	clients *db.DownloadClientRepo,
	blocklist *db.BlocklistRepo,
	books *db.BookRepo,
	meta *metadata.Aggregator,
	onNewAuthor func(author *models.Author),
) *MigrateHandler {
	return &MigrateHandler{
		authors: authors, indexers: indexers, clients: clients,
		blocklist: blocklist, books: books, meta: meta,
		onNewAuthor: onNewAuthor,
		readarrImporter: migrate.NewReadarrImporter(
			authors, indexers, clients, blocklist, meta, onNewAuthor,
		),
		goodreadsImporter: migrate.NewGoodreadsImporter(authors, books, meta),
	}
}

// ImportCSV accepts a multipart form with a "file" field containing either
// a newline-separated list of author names or a CSV (name[,monitored]). Top
// OpenLibrary match is chosen for each name. A third column, if present, is
// ignored for backward compatibility (#966).
func (h *MigrateHandler) ImportCSV(w http.ResponseWriter, r *http.Request) {
	file, err := acceptUpload(w, r, 5<<20) // 5 MB cap — CSV of names is tiny
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer file.Close()

	result, err := migrate.ImportCSVAuthors(r.Context(), file, h.authors, h.meta, h.onNewAuthor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ImportReadarr accepts a multipart form with a "file" field containing
// readarr.db (SQLite). The file is spooled to a temp path and the import
// is kicked off asynchronously so the HTTP connection is not held open for
// the duration — large libraries with many authors require one or more
// OpenLibrary round-trips per author and can easily exceed server write
// timeouts, producing a silent "NetworkError" in the browser.
//
// Returns 202 Accepted with an initial progress snapshot. The UI must poll
// GET /api/v1/migrate/readarr/status to track completion.
func (h *MigrateHandler) ImportReadarr(w http.ResponseWriter, r *http.Request) {
	file, err := acceptUpload(w, r, 1<<30) // 1 GB cap — readarr.db is usually < 100 MB
	if err != nil {
		slog.Error("readarr import: upload rejected", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp(uploadTempDir(), "readarr-*.db")
	if err != nil {
		slog.Error("readarr import: create temp file failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create temp: " + err.Error()})
		return
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		slog.Error("readarr import: spool upload failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write temp: " + err.Error()})
		return
	}
	if err := tmp.Close(); err != nil {
		slog.Error("readarr import: close temp file failed", "path", tmpPath, "error", err)
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "close temp: " + err.Error()})
		return
	}

	// context.WithoutCancel ensures the import goroutine is not cancelled
	// when the HTTP response is sent — same pattern as calibre/import.
	err = h.readarrImporter.Start(context.WithoutCancel(r.Context()), tmpPath)
	switch {
	case errors.Is(err, migrate.ErrAlreadyRunning):
		// Clean up the spool file we just wrote since the import won't run.
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		_ = os.Remove(tmpPath)
		slog.Error("readarr import: failed to start", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, h.readarrImporter.Progress())
}

// ImportReadarrStatus returns the current progress of an in-flight or
// recently completed Readarr import. Cheap stateless poll — callers may
// hit this as frequently as they like.
func (h *MigrateHandler) ImportReadarrStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.readarrImporter.Progress())
}

// acceptUpload reads a multipart "file" field with a size cap, returning
// the file reader. Caller must Close. Passing w to MaxBytesReader makes the
// server respond 413 Request Entity Too Large automatically when the body
// exceeds maxBytes, instead of the handler seeing a generic parse error.
func acceptUpload(w http.ResponseWriter, r *http.Request, maxBytes int64) (io.ReadCloser, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return nil, err
	}
	ct := ""
	if hdr != nil {
		ct = strings.ToLower(strings.TrimSpace(hdr.Header.Get("Content-Type")))
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
	}
	if !allowedUploadCT[ct] {
		_ = f.Close()
		return nil, fmt.Errorf("unsupported content-type %q; expected text/csv or sqlite binary", ct)
	}
	return f, nil
}

// ImportGoodreadsPreview accepts a multipart form with a "file" field holding
// a Goodreads library CSV export and an optional "shelves" field (a
// comma-separated subset of to-read,read,currently-reading; defaults to
// to-read). It parses the CSV and resolves every in-scope row against the
// metadata providers WITHOUT writing anything, returning a dry-run preview
// with a token. The UI shows the counts, then POSTs the token to
// /migrate/goodreads/commit to actually add the books.
//
// Resolution can take a while for a large export — every row makes at least
// one OpenLibrary call and the importer paces itself. The work runs on a
// context detached from the request so a client timeout does not abort it.
func (h *MigrateHandler) ImportGoodreadsPreview(w http.ResponseWriter, r *http.Request) {
	file, err := acceptUpload(w, r, 20<<20) // 20 MB cap — a big Goodreads export
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer file.Close()

	rows, err := migrate.ParseGoodreadsCSV(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no usable rows found in the Goodreads CSV"})
		return
	}

	// acceptUpload already wrapped the body in MaxBytesReader and parsed the
	// multipart form, so read from the parsed form directly rather than via
	// FormValue (which would re-trigger the body-reading parse path).
	opts := migrate.GoodreadsImportOptions{Shelves: parseShelfFilter(r.Form.Get("shelves"))}

	// Detach from the request context: a slow browser must not abort a
	// resolution pass that may run for minutes on a large library.
	preview, err := h.goodreadsImporter.Preview(context.WithoutCancel(r.Context()), rows, opts)
	if err != nil {
		if errors.Is(err, migrate.ErrGoodreadsRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		slog.Error("goodreads import: preview failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// ImportGoodreadsCommit persists the resolved books from a preview token. The
// JSON body is {"token": "..."}. The preview is consumed on commit so a
// token cannot double-add.
func (h *MigrateHandler) ImportGoodreadsCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}

	result, err := h.goodreadsImporter.Commit(context.WithoutCancel(r.Context()), body.Token)
	if err != nil {
		if errors.Is(err, migrate.ErrGoodreadsPreviewNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		slog.Error("goodreads import: commit failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// parseShelfFilter splits a comma-separated "shelves" form value into a slice
// of Goodreads Exclusive Shelf names. Unknown values are dropped; an empty or
// all-invalid input yields nil, which the importer treats as "to-read only".
func parseShelfFilter(raw string) []string {
	valid := map[string]bool{
		migrate.GoodreadsShelfToRead:           true,
		migrate.GoodreadsShelfRead:             true,
		migrate.GoodreadsShelfCurrentlyReading: true,
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if valid[part] {
			out = append(out, part)
		}
	}
	return out
}
