package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// lookupRequest builds a GET request for the manual-import lookup endpoint,
// properly percent-encoding the path so directory names with spaces don't
// produce a malformed URL.
func lookupRequest(path string) *http.Request {
	u := "/api/v1/queue/manual-import/lookup?" + url.Values{"path": {path}}.Encode()
	return httptest.NewRequest(http.MethodGet, u, nil)
}

// stubManualImportScanner satisfies manualImportScanner for testing.
// ImportFromPath is a no-op because the handler calls it in a goroutine and
// the tests only verify the synchronous HTTP response.
type stubManualImportScanner struct {
	lookupResult importer.LookupResult
	lookupErr    error
}

func (s *stubManualImportScanner) Lookup(_ context.Context, _ string) (importer.LookupResult, error) {
	return s.lookupResult, s.lookupErr
}

func (s *stubManualImportScanner) ImportFromPath(_ context.Context, _ *models.Download, _, _ string) {
}

// manualImportFixture spins up an in-memory DB and wires a ManualImportHandler.
func manualImportFixture(t *testing.T) (*ManualImportHandler, *stubManualImportScanner, *db.DownloadRepo, *db.BookRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	stub := &stubManualImportScanner{}
	return NewManualImportHandler(stub, downloads, books), stub, downloads, books, context.Background()
}

// seedBook inserts a minimal author + book and returns the book.
func seedBook(t *testing.T, authors *db.AuthorRepo, books *db.BookRepo, ctx context.Context) *models.Book {
	t.Helper()
	a := &models.Author{
		ForeignID: "mi-author-1", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	b := &models.Book{
		ForeignID: "mi-book-1", AuthorID: a.ID,
		Title: "Test Book", SortTitle: "test book",
		Status: "wanted", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	return b
}

// ── Lookup handler ──────────────────────────────────────────────────────────

func TestManualImportLookup_EmptyPath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/lookup", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path parameter required") {
		t.Errorf("body = %q, want 'path parameter required'", rec.Body.String())
	}
}

func TestManualImportLookup_RelativePath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/lookup?path=relative/path/book.epub", nil)
	h.Lookup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path must be absolute") {
		t.Errorf("body = %q, want 'path must be absolute'", rec.Body.String())
	}
}

func TestManualImportLookup_PathNotAccessible(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/lookup?path=/does/not/exist/book.epub", nil)
	h.Lookup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path not accessible") {
		t.Errorf("body = %q, want 'path not accessible'", rec.Body.String())
	}
}

func TestManualImportLookup_ScannerError(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	stub.lookupErr = errors.New("catalogue unavailable")

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/lookup?path="+epub, nil)
	h.Lookup(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "catalogue unavailable") {
		t.Errorf("body = %q, want scanner error message", rec.Body.String())
	}
}

func TestManualImportLookup_Success(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	stub.lookupResult = importer.LookupResult{
		Match:          "confident",
		DetectedFormat: models.MediaTypeEbook,
		ParsedTitle:    "Test Book",
		ParsedAuthor:   "Test Author",
	}

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "Test.Book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/lookup?path="+epub, nil)
	h.Lookup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var result importer.LookupResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Match != "confident" {
		t.Errorf("match = %q, want confident", result.Match)
	}
	if result.DetectedFormat != models.MediaTypeEbook {
		t.Errorf("detectedFormat = %q, want ebook", result.DetectedFormat)
	}
}

// ── Import handler ───────────────────────────────────────────────────────────

func TestManualImportImport_BadJSON(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Errorf("body = %q, want 'invalid request body'", rec.Body.String())
	}
}

func TestManualImportImport_EmptyPath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	body := bytes.NewBufferString(`{"path":"","bookId":1}`)
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path is required") {
		t.Errorf("body = %q, want 'path is required'", rec.Body.String())
	}
}

func TestManualImportImport_RelativePath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	body := bytes.NewBufferString(`{"path":"relative/book.epub","bookId":1}`)
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path must be absolute") {
		t.Errorf("body = %q, want 'path must be absolute'", rec.Body.String())
	}
}

func TestManualImportImport_NoBookID(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	body := bytes.NewBufferString(`{"path":"/some/book.epub","bookId":0}`)
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bookId is required") {
		t.Errorf("body = %q, want 'bookId is required'", rec.Body.String())
	}
}

func TestManualImportImport_InvalidFormat(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	body := bytes.NewBufferString(`{"path":"/some/book.epub","bookId":1,"format":"pdf"}`)
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "format must be") {
		t.Errorf("body = %q, want format error", rec.Body.String())
	}
}

func TestManualImportImport_PathNotAccessible(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	body := bytes.NewBufferString(`{"path":"/does/not/exist/book.epub","bookId":1}`)
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path not accessible") {
		t.Errorf("body = %q, want 'path not accessible'", rec.Body.String())
	}
}

func TestManualImportImport_NotABookFile(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	tmp := t.TempDir()
	// .zip is not in bookExtensions; it must not be mistaken for a book file.
	zip := filepath.Join(tmp, "archive.zip")
	if err := os.WriteFile(zip, []byte("not a book"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"path": zip, "bookId": 1})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a recognised book file") {
		t.Errorf("body = %q, want 'not a recognised book file'", rec.Body.String())
	}
}

func TestManualImportImport_BookNotFound(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"path": epub, "bookId": 9999})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "book not found") {
		t.Errorf("body = %q, want 'book not found'", rec.Body.String())
	}
}

func TestManualImportImport_DownloadCreateFails(t *testing.T) {
	// Use two separate in-memory DBs so books.GetByID can succeed (bookDB stays
	// open) while downloads.Create fails (downloadsDB is closed before the
	// request). This isolates exactly the "failed to create import record" 500
	// path without inadvertently causing GetByID to fail first.
	bookDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bookDB.Close() })

	downloadsDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	// downloadsDB is closed manually below to make downloads.Create fail.

	authors := db.NewAuthorRepo(bookDB)
	books := db.NewBookRepo(bookDB)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)

	downloadsDB.Close()

	stub := &stubManualImportScanner{}
	h := NewManualImportHandler(stub, db.NewDownloadRepo(downloadsDB), books)

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"path": epub, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when downloads.Create fails; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to create import record") {
		t.Errorf("body = %q, want 'failed to create import record'", rec.Body.String())
	}
}

func TestManualImportImport_Success(t *testing.T) {
	t.Parallel()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)

	stub := &stubManualImportScanner{}
	h := NewManualImportHandler(stub, downloads, books)

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "test.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"path":   epub,
		"bookId": book.ID,
		"format": models.MediaTypeEbook,
	})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	var dl models.Download
	if err := json.NewDecoder(rec.Body).Decode(&dl); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dl.ID == 0 {
		t.Error("expected response to contain a non-zero download ID")
	}
	if !strings.HasPrefix(dl.GUID, "manual-") {
		t.Errorf("GUID = %q, want 'manual-...' prefix", dl.GUID)
	}
	if dl.Title != book.Title {
		t.Errorf("Title = %q, want %q", dl.Title, book.Title)
	}
	if dl.Status != models.StateCompleted {
		t.Errorf("Status = %q, want %q", dl.Status, models.StateCompleted)
	}
	if dl.BookID == nil || *dl.BookID != book.ID {
		t.Errorf("BookID = %v, want %d", dl.BookID, book.ID)
	}
}

// TestManualImportImport_DirectoryPath verifies that a directory (audiobook
// folder) is accepted without triggering the "not a recognised book file" guard.
func TestManualImportImport_DirectoryPath(t *testing.T) {
	t.Parallel()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)

	stub := &stubManualImportScanner{}
	h := NewManualImportHandler(stub, downloads, books)

	dir := t.TempDir() // use a real directory as the "audiobook folder"

	body, _ := json.Marshal(map[string]any{
		"path":   dir,
		"bookId": book.ID,
		"format": models.MediaTypeAudiobook,
	})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
}

// ── Path containment checks ─────────────────────────────────────────────────
//
// These tests exercise isAllowedPath only. The containment check fires before
// any repo call, so no database is needed — handlers are constructed with nil
// repos to avoid the migration overhead under -race.

// makeBookPath creates a path that looks like a supported ebook or audiobook.
// For audiobooks it creates a real directory (the import handler accepts dirs
// for multi-part audiobooks).
func makeBookPath(t *testing.T, dir, name string, isDir bool) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if isDir {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// containmentHandler returns a ManualImportHandler with the given allowed
// roots but no backing database — safe for tests that only reach isAllowedPath.
func containmentHandler(roots ...string) *ManualImportHandler {
	return NewManualImportHandler(&stubManualImportScanner{}, nil, nil).WithAllowedRoots(roots...)
}

// TestManualImportLookup_PathOutsideAllowedRoots verifies that Lookup returns
// 403 for any path (ebook file, audiobook file, audiobook directory) that falls
// outside the configured allowed roots.
func TestManualImportLookup_PathOutsideAllowedRoots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, file string
		isDir      bool
	}{
		{"ebook file", "book.epub", false},
		{"audiobook file", "narration.m4b", false},
		{"audiobook directory", "Audiobook Title", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := containmentHandler(t.TempDir()) // allowed root is a different dir

			outside := t.TempDir()
			p := makeBookPath(t, outside, tc.file, tc.isDir)

			rec := httptest.NewRecorder()
			h.Lookup(rec, lookupRequest(p))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "outside the configured library roots") {
				t.Errorf("body = %q, want containment error", rec.Body.String())
			}
		})
	}
}

// TestManualImportLookup_PathInsideAllowedRoots verifies that Lookup accepts
// paths under the ebook root (cfg.LibraryDir) and the audiobook root
// (cfg.AudiobookDir) — two separate roots, mirroring production wiring.
func TestManualImportLookup_PathInsideAllowedRoots(t *testing.T) {
	t.Parallel()

	ebookRoot := t.TempDir()
	audiobookRoot := t.TempDir()
	stub := &stubManualImportScanner{lookupResult: importer.LookupResult{Match: "none"}}
	h := NewManualImportHandler(stub, nil, nil).WithAllowedRoots(ebookRoot, audiobookRoot)

	cases := []struct {
		name, file string
		root       string
		isDir      bool
	}{
		{"ebook file in ebook root", "book.epub", ebookRoot, false},
		{"audiobook file in audiobook root", "narration.m4b", audiobookRoot, false},
		{"audiobook directory in audiobook root", "Audiobook Title", audiobookRoot, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := makeBookPath(t, tc.root, tc.file, tc.isDir)

			rec := httptest.NewRecorder()
			h.Lookup(rec, lookupRequest(p))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestManualImportImport_PathOutsideAllowedRoots verifies that Import returns
// 403 for ebook and audiobook paths that fall outside the allowed roots.
// The 403 fires before any repo call so no database is needed.
func TestManualImportImport_PathOutsideAllowedRoots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, file string
		isDir      bool
	}{
		{"ebook file", "secret.epub", false},
		{"audiobook file", "secret.m4b", false},
		{"audiobook directory", "Secret Audiobook", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := containmentHandler(t.TempDir())

			outside := t.TempDir()
			p := makeBookPath(t, outside, tc.file, tc.isDir)

			body, _ := json.Marshal(map[string]any{"path": p, "bookId": 1})
			rec := httptest.NewRecorder()
			h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "outside the configured library roots") {
				t.Errorf("body = %q, want containment error", rec.Body.String())
			}
		})
	}
}

// TestManualImportLookup_SymlinkEscape verifies that a symlink inside the
// allowed root that points to a directory outside the root is rejected after
// filepath.EvalSymlinks resolves the real target.
func TestManualImportLookup_SymlinkEscape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, file string
		isDir      bool
	}{
		{"ebook file via symlink", "secret.epub", false},
		{"audiobook file via symlink", "secret.m4b", false},
		{"audiobook directory via symlink", "Secret Audiobook Dir", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			allowed := filepath.Join(tmp, "safe")
			outside := filepath.Join(tmp, "secret")
			if err := os.MkdirAll(allowed, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			makeBookPath(t, outside, tc.file, tc.isDir)

			// symlink inside "safe" pointing at the directory outside
			link := filepath.Join(allowed, "escape")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}

			h := containmentHandler(allowed)
			escapedPath := filepath.Join(link, tc.file)
			rec := httptest.NewRecorder()
			h.Lookup(rec, lookupRequest(escapedPath))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 for symlink escape; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
