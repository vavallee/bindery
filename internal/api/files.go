package api

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
)

type FileHandler struct {
	books        *db.BookRepo
	allowedRoots []string
}

func NewFileHandler(books *db.BookRepo, allowedRoots ...string) *FileHandler {
	var roots []string
	for _, r := range allowedRoots {
		if r != "" {
			roots = append(roots, filepath.Clean(r))
		}
	}
	return &FileHandler{books: books, allowedRoots: roots}
}

// Download serves the book's content for browser download.
//   - Ebook (FilePath is a file): streams the file with its original name.
//   - Audiobook (FilePath is a directory): streams a zip of the folder so
//     multi-part m4b/mp3 + cover art come down as one bundle.
func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	// Prefer the legacy FilePath, fall back to per-format columns for books
	// created after the dual-format schema landed (migration 026+).
	filePath := book.FilePath
	if filePath == "" {
		filePath = book.EbookFilePath
	}
	if filePath == "" {
		filePath = book.AudiobookFilePath
	}
	if filePath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no file available for this book"})
		return
	}

	// Defence-in-depth: refuse to serve paths that aren't under a configured
	// library root, even if a tampered DB row or importer bug set a path
	// to something outside the library (e.g. /etc/passwd, /config/*).
	if !h.isAllowedPath(filePath) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found on disk"})
		return
	}

	if info.IsDir() {
		streamZip(w, filePath)
		return
	}

	filename := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeFile(w, r, filePath)
}

// isAllowedPath reports whether p falls under one of the configured library
// roots. Paths are compared after filepath.Clean so trailing slashes and
// `..` traversal don't bypass the check. If no roots are configured the
// handler falls back to allowing any path — this preserves behaviour for
// installs that haven't set BINDERY_LIBRARY_DIR and for tests that wire
// the handler without roots.
func (h *FileHandler) isAllowedPath(p string) bool {
	if len(h.allowedRoots) == 0 {
		return true
	}
	p = filepath.Clean(p)
	for _, root := range h.allowedRoots {
		if root == "" || root == "." {
			continue
		}
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// streamZip writes a zip archive of every regular file under srcDir to the
// ResponseWriter. Headers are set before the first byte is written.
// Content-Length is unknown (streamed), so we use chunked transfer.
func streamZip(w http.ResponseWriter, srcDir string) {
	zipName := filepath.Base(srcDir) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+zipName+"\"")

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return nil
		}
		// Use forward slashes in zip entries for cross-platform extraction.
		rel = strings.ReplaceAll(rel, string(filepath.Separator), "/")
		zf, zerr := zw.Create(rel)
		if zerr != nil {
			return zerr
		}
		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(zf, f)
		return err
	})
}
