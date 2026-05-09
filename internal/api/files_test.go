package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func fileFixture(t *testing.T) (*FileHandler, *db.BookRepo, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return NewFileHandler(books), books, author, ctx
}

func TestFileDownload_BadID(t *testing.T) {
	h, _, _, _ := fileFixture(t)
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/abc/download", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}
}

func TestFileDownload_NotFound(t *testing.T) {
	h, _, _, _ := fileFixture(t)
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/999/download", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing book: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_NoFilePath(t *testing.T) {
	h, books, author, ctx := fileFixture(t)
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("no file path: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_FileMissingOnDisk(t *testing.T) {
	h, books, author, ctx := fileFixture(t)
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// FilePath set to a non-existent path.
	if err := books.SetFilePath(ctx, book.ID, "/nope/not-a-real-path.epub"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("file missing on disk: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_EbookFile(t *testing.T) {
	h, books, author, ctx := fileFixture(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(path, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte("epub bytes")) {
		t.Errorf("body mismatch: got %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Errorf("expected Content-Disposition header")
	}
}

func TestFileDownload_NonASCIIFilename(t *testing.T) {
	h, books, author, ctx := fileFixture(t)
	tmp := t.TempDir()
	name := "日本語.epub"
	path := filepath.Join(tmp, name)
	if err := os.WriteFile(path, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-JP", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	// RFC 5987 filename* parameter must carry the percent-encoded UTF-8 name.
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition missing filename* param: %q", cd)
	}
	encoded := url.PathEscape(name)
	if !strings.Contains(cd, encoded) {
		t.Errorf("Content-Disposition missing percent-encoded name %q: %q", encoded, cd)
	}
	// And the legacy filename= parameter must not contain raw non-ASCII bytes.
	for _, r := range cd {
		if r > 0x7e {
			t.Errorf("Content-Disposition contains non-ASCII byte %U: %q", r, cd)
			break
		}
	}
}

func TestFileDownload_AudiobookDirStreamsZip(t *testing.T) {
	h, books, author, ctx := fileFixture(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "Title (2020)")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "cover.jpg"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{ForeignID: "OL2B", AuthorID: author.ID, Title: "A", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, dir); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type: got %q, want application/zip", ct)
	}

	// Unzip and check entries are present with forward slashes.
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		io.Copy(io.Discard, rc)
		rc.Close()
	}
	if !names["part1.m4b"] || !names["nested/cover.jpg"] {
		t.Errorf("missing entries in zip: %v", names)
	}
}
