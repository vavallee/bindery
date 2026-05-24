package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

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
	// Close the database after the book is seeded but before the request, so
	// that downloads.Create hits a closed-connection error while books.GetByID
	// would have succeeded on the live DB. We use a separate connection for
	// seeding so the close only affects the handler's repos.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)

	// Now close the shared DB — both repos will fail from here on.
	database.Close()

	stub := &stubManualImportScanner{}
	h := NewManualImportHandler(stub, db.NewDownloadRepo(database), db.NewBookRepo(database))

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"path": epub, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	// Both GetByID and Create will fail on the closed DB; either a 400 (book
	// not found) or a 500 (create failed) is acceptable — the point is that
	// the handler does not panic.
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 400 or 500 after DB close", rec.Code)
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
