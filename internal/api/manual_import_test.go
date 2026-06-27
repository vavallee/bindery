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
// roots but no backing database — safe for tests that only reach roots.Contains.
func containmentHandler(roots ...string) *ManualImportHandler {
	return NewManualImportHandler(&stubManualImportScanner{}, nil, nil).
		WithRoots(NewLibraryRoots(nil, roots...))
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
	h := NewManualImportHandler(stub, nil, nil).WithRoots(NewLibraryRoots(nil, ebookRoot, audiobookRoot))

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

// TestManualImport_SymlinkEscapeRejected is the regression test for the
// arbitrary-file-read/move defect: a symlink that physically lives inside the
// allowed root but points at a file OUTSIDE it must be rejected by both
// endpoints, not silently followed.
func TestManualImport_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.epub")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.epub")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	h := containmentHandler(root)

	// Lookup must refuse to follow the escaping symlink.
	rec := httptest.NewRecorder()
	h.Lookup(rec, lookupRequest(link))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Lookup status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "outside the configured library roots") {
		t.Errorf("Lookup body = %q, want containment error", rec.Body.String())
	}

	// Import must refuse it too (403 fires before any repo call).
	body, _ := json.Marshal(map[string]any{"path": link, "bookId": 1})
	rec = httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Import status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "outside the configured library roots") {
		t.Errorf("Import body = %q, want containment error", rec.Body.String())
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

// ── Scan handler ────────────────────────────────────────────────────────────

func scanRequest(path string) *http.Request {
	u := "/api/v1/queue/manual-import/scan?" + url.Values{"path": {path}}.Encode()
	return httptest.NewRequest(http.MethodGet, u, nil)
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManualImportScan_EnumeratesBookUnits(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	stub.lookupResult = importer.LookupResult{Match: "none", ParsedTitle: "x"}

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Book One.epub"))            // book file -> unit
	writeTestFile(t, filepath.Join(root, "Book Two.mobi"))            // book file -> unit
	writeTestFile(t, filepath.Join(root, "cover.jpg"))                // non-book -> skipped
	writeTestFile(t, filepath.Join(root, "Author - Title", "ab.m4b")) // subdir w/ book -> unit
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err) // subdir w/o book -> skipped
	}

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp ScanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items = %d, want 3 (2 book files + 1 book subdir); got %+v", len(resp.Items), resp.Items)
	}
	if resp.Truncated {
		t.Error("unexpected truncation for a small folder")
	}
}

func TestManualImportScan_RejectsSingleFile(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)
	root := t.TempDir()
	f := filepath.Join(root, "book.epub")
	writeTestFile(t, f)
	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(f))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (scan needs a folder, not a file)", rec.Code)
	}
}

func TestManualImportScan_EmptyPath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)
	rec := httptest.NewRecorder()
	h.Scan(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/scan", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ── Batch import ────────────────────────────────────────────────────────────

func TestManualImportBatch_MixedValidity(t *testing.T) {
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

	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)

	root := t.TempDir()
	good := filepath.Join(root, "good.epub")
	writeTestFile(t, good)
	noBook := filepath.Join(root, "nobook.epub")
	writeTestFile(t, noBook)

	items := []map[string]any{
		{"path": good, "bookId": book.ID, "format": models.MediaTypeEbook}, // valid
		{"path": noBook, "bookId": 0},                                      // missing bookId
		{"path": filepath.Join(root, "missing.epub"), "bookId": book.ID},   // path not accessible
	}
	body, _ := json.Marshal(items)
	rec := httptest.NewRecorder()
	h.ImportBatch(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/batch", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp BatchImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Accepted != 1 || resp.Failed != 2 {
		t.Fatalf("accepted=%d failed=%d, want 1/2; results=%+v", resp.Accepted, resp.Failed, resp.Results)
	}
	if !resp.Results[0].Accepted || resp.Results[0].DownloadID == 0 {
		t.Errorf("first item should be accepted with a download id; got %+v", resp.Results[0])
	}
	if resp.Results[1].Accepted || resp.Results[2].Accepted {
		t.Errorf("invalid items should not be accepted; got %+v", resp.Results[1:])
	}
	all, err := downloads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("download rows = %d, want 1 (only the valid item created one)", len(all))
	}
}

func TestManualImportBatch_Empty(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)
	rec := httptest.NewRecorder()
	h.ImportBatch(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/batch", bytes.NewReader([]byte("[]"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty batch", rec.Code)
	}
}

func TestManualImportReassign_DetachesSourceAndImportsTarget(t *testing.T) {
	t.Parallel()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	bookFiles := db.NewBookFileRepo(database)
	ctx := context.Background()

	src := seedBook(t, authors, books, ctx) // file is wrongly attached here
	target := &models.Book{
		ForeignID: "mi-book-2", AuthorID: src.AuthorID,
		Title: "Correct Book", SortTitle: "correct book",
		Status: "wanted", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, target); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "mismatched.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, src.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatalf("attach file to source: %v", err)
	}

	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)
	body, _ := json.Marshal(map[string]any{"path": epub, "targetBookId": target.ID, "format": models.MediaTypeEbook})
	rec := httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	// The stale association is gone from the source book (the actual move to the
	// target is performed by the stubbed ImportFromPath, exercised live, not here).
	srcFiles, err := bookFiles.ListByBook(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcFiles) != 0 {
		t.Errorf("source book still has %d file(s); want 0 after reassign", len(srcFiles))
	}
}

func TestManualImportReassign_EmptyPath(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)
	body, _ := json.Marshal(map[string]any{"path": "  ", "targetBookId": 1})
	rec := httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestManualImportReassign_TargetNotFound(t *testing.T) {
	t.Parallel()
	h, _, _, _, _ := manualImportFixture(t)
	tmp := t.TempDir()
	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"path": epub, "targetBookId": 9999})
	rec := httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (book not found); body = %s", rec.Code, rec.Body.String())
	}
}
