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
	books *db.BookRepo
}

func NewFileHandler(books *db.BookRepo) *FileHandler {
	return &FileHandler{books: books}
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

	if book.FilePath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no file available for this book"})
		return
	}

	info, err := os.Stat(book.FilePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found on disk"})
		return
	}

	if info.IsDir() {
		streamZip(w, book.FilePath)
		return
	}

	filename := filepath.Base(book.FilePath)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeFile(w, r, book.FilePath)
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
