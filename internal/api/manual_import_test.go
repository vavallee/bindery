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
	"sync"
	"testing"
	"time"

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
	lookupResult     importer.LookupResult
	lookupErr        error
	lookupBatchCalls int

	// importMu guards the fields below: ImportFromPath is invoked in a goroutine
	// by the handler, so tests read these under the lock after a short wait.
	importMu    sync.Mutex
	importCalls int
	lastPath    string
	lastBookID  int64
}

func (s *stubManualImportScanner) Lookup(_ context.Context, _ string) (importer.LookupResult, error) {
	return s.lookupResult, s.lookupErr
}

func (s *stubManualImportScanner) LookupBatch(_ context.Context, paths []string) ([]importer.LookupResult, error) {
	s.lookupBatchCalls++
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	out := make([]importer.LookupResult, len(paths))
	for i := range paths {
		out[i] = s.lookupResult
	}
	return out, nil
}

func (s *stubManualImportScanner) ImportFromPath(_ context.Context, dl *models.Download, path, _ string) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	s.importCalls++
	s.lastPath = path
	if dl.BookID != nil {
		s.lastBookID = *dl.BookID
	}
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
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("body = %q, want generic server error", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "catalogue unavailable") {
		t.Errorf("body = %q, must not leak the internal error", rec.Body.String())
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

// TestMatchDownload_ImportsRecordedPath is the #1589 core: an unmatched
// import-failed download that recorded where its files are is matched to a book
// and imported directly against it — book assigned, ImportFromPath invoked with
// the recorded path.
func TestMatchDownload_ImportsRecordedPath(t *testing.T) {
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
	h := NewManualImportHandler(stub, downloads, books) // nil roots ⇒ containment allows

	tmp := t.TempDir()
	epub := filepath.Join(tmp, "unmatched.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{
		GUID: "match-guid", Title: "Unmatched Release", NZBURL: "http://x/y.nzb",
		Status: models.StateImportFailed, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if err := downloads.SetImportPath(ctx, dl.ID, epub); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Imported bool `json:"imported"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Imported {
		t.Errorf("imported = false, want true (recorded path present)")
	}
	// Book assigned to the download.
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got.BookID == nil || *got.BookID != book.ID {
		t.Errorf("download book = %v, want %d", got.BookID, book.ID)
	}
	// ImportFromPath invoked (async) with the recorded path and the assigned book.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stub.importMu.Lock()
		calls, path, bid := stub.importCalls, stub.lastPath, stub.lastBookID
		stub.importMu.Unlock()
		if calls > 0 {
			if path != epub {
				t.Errorf("import path = %q, want %q", path, epub)
			}
			if bid != book.ID {
				t.Errorf("import bookID = %d, want %d", bid, book.ID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ImportFromPath was not called within 2s")
}

// TestMatchDownload_ClientFallbackResetsRetry: a download with no recorded path
// but an owning download client is assigned the book and its retry is reset so
// the next client poll re-derives the location and imports.
func TestMatchDownload_ClientFallbackResetsRetry(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)

	client := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	stub := &stubManualImportScanner{}
	h := NewManualImportHandler(stub, downloads, books)

	dl := &models.Download{
		GUID: "match-client", Title: "Client Release", NZBURL: "http://x/y.nzb",
		Status: models.StateImportFailed, Protocol: "usenet", DownloadClientID: &client.ID,
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET import_retry_count=3, download_client_id=? WHERE id=?", client.ID, dl.ID); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got.BookID == nil || *got.BookID != book.ID {
		t.Errorf("download book = %v, want %d", got.BookID, book.ID)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("retry count = %d, want 0 (reset for re-poll)", got.ImportRetryCount)
	}
}

// TestMatchDownload_NoPathNoClientReportsUnlocated: with neither a recorded path
// nor an owning client there are no files to import and nothing to re-poll — the
// book is still assigned, but the response reports located=false so the UI can
// tell the user honestly instead of promising a retry that never fires (#1589).
func TestMatchDownload_NoPathNoClientReportsUnlocated(t *testing.T) {
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
	dl := &models.Download{
		GUID: "match-orphan", Title: "No Files", NZBURL: "http://x/y.nzb",
		Status: models.StateImportFailed, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Imported    bool `json:"imported"`
		RetryQueued bool `json:"retryQueued"`
		Located     bool `json:"located"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Imported || resp.RetryQueued || resp.Located {
		t.Errorf("resp = %+v, want all false (nothing to import, nothing to re-poll)", resp)
	}
	// Book is still assigned so the queue shows it as matched.
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got.BookID == nil || *got.BookID != book.ID {
		t.Errorf("download book = %v, want %d", got.BookID, book.ID)
	}
}

// TestMatchDownload_RejectsNonImportFailed guards against matching a book onto a
// download that isn't in the importFailed state.
func TestMatchDownload_RejectsNonImportFailed(t *testing.T) {
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
	dl := &models.Download{
		GUID: "match-completed", Title: "Done", NZBURL: "http://x/y.nzb",
		Status: models.StateCompleted, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestMatchDownload_BadJSON(t *testing.T) {
	h, _, _, _, _ := manualImportFixture(t)
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMatchDownload_MissingIDs(t *testing.T) {
	h, _, _, _, _ := manualImportFixture(t)
	for _, body := range []string{`{}`, `{"downloadId":1}`, `{"bookId":1}`} {
		rec := httptest.NewRecorder()
		h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestMatchDownload_DownloadNotFound(t *testing.T) {
	h, _, _, _, _ := manualImportFixture(t)
	body, _ := json.Marshal(map[string]any{"downloadId": 999, "bookId": 1})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMatchDownload_BookNotFound(t *testing.T) {
	h, _, downloads, _, ctx := manualImportFixture(t)
	dl := &models.Download{GUID: "match-nobook", Title: "t", NZBURL: "x", Status: models.StateImportFailed, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": 9999})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (book not found)", rec.Code)
	}
}

// TestMatchDownload_RecordedPathMissingFallsBack: import_path is set but the
// file no longer exists on disk, so the direct-import branch is skipped and the
// handler falls through — here with no client, so it reports located=false.
func TestMatchDownload_RecordedPathMissingFallsBack(t *testing.T) {
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
	dl := &models.Download{GUID: "match-gonepath", Title: "t", NZBURL: "x", Status: models.StateImportFailed, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if err := downloads.SetImportPath(ctx, dl.ID, filepath.Join(t.TempDir(), "vanished.epub")); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Imported, Located bool }
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Imported || resp.Located {
		t.Errorf("resp = %+v, want imported=false located=false (recorded file gone, no client)", resp)
	}
}

// TestMatchDownload_LoadError forces the initial GetByID to fail by closing the
// database, exercising the writeServerError branch.
func TestMatchDownload_LoadError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)
	database.Close() // subsequent queries error

	body, _ := json.Marshal(map[string]any{"downloadId": 1, "bookId": 1})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on DB error", rec.Code)
	}
}

// TestMatchDownload_SetBookError forces the SetBookID write to fail (read-only
// DB) after the reads succeed, exercising that writeServerError branch.
func TestMatchDownload_SetBookError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1) // one conn so the query_only pragma sticks

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)
	dl := &models.Download{GUID: "match-roerr", Title: "t", NZBURL: "x", Status: models.StateImportFailed, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	// Reads still succeed; the next UPDATE (SetBookID) fails.
	if _, err := database.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}

	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)
	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when SetBookID fails; body = %s", rec.Code, rec.Body.String())
	}
}

// TestMatchDownload_RetryResetError reaches the client fallback (no recorded
// path, has a client) and forces ResetImportRetry to fail via a trigger that
// aborts only import_retry_count updates — so SetBookID still succeeds but the
// retry reset errors, exercising that writeServerError branch.
func TestMatchDownload_RetryResetError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)

	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	ctx := context.Background()
	book := seedBook(t, authors, books, ctx)
	client := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{GUID: "match-reseterr", Title: "t", NZBURL: "x", Status: models.StateImportFailed, Protocol: "usenet", DownloadClientID: &client.ID}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET download_client_id=? WHERE id=?", client.ID, dl.ID); err != nil {
		t.Fatal(err)
	}
	// Fires only when import_retry_count is updated: SetBookID (book_id) passes,
	// ResetImportRetry (import_retry_count) aborts.
	if _, err := database.ExecContext(ctx,
		"CREATE TRIGGER fail_retry_reset BEFORE UPDATE OF import_retry_count ON downloads BEGIN SELECT RAISE(ABORT, 'boom'); END;"); err != nil {
		t.Fatal(err)
	}

	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)
	body, _ := json.Marshal(map[string]any{"downloadId": dl.ID, "bookId": book.ID})
	rec := httptest.NewRecorder()
	h.MatchDownload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/match", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when ResetImportRetry fails; body = %s", rec.Code, rec.Body.String())
	}
	// SetBookID still applied before the failing reset.
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got.BookID == nil || *got.BookID != book.ID {
		t.Errorf("book = %v, want %d assigned before the reset error", got.BookID, book.ID)
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
	// Regression guard for #1473: however many units a scan enumerates, it must
	// load the catalogue exactly once (a single batch lookup) rather than once
	// per item, which is the N+1 that stalled large scans past WriteTimeout.
	if stub.lookupBatchCalls != 1 {
		t.Errorf("LookupBatch called %d times, want exactly 1 for the whole scan", stub.lookupBatchCalls)
	}
}

// TestManualImportScan_AllowsConfiguredRootItself reproduces #1373: pasting a
// configured root ("/books") or an allow-listed download dir ("/downloads")
// into bulk import must scan it, not 403. Before the fix, ResolveContained
// reused the delete path's strict containment (root != contained), so the two
// most obvious targets for the feature both failed with "path is outside the
// configured library roots".
func TestManualImportScan_AllowsConfiguredRootItself(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	stub.lookupResult = importer.LookupResult{Match: "none", ParsedTitle: "x"}

	libraryDir := t.TempDir()
	downloadDir := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(libraryDir, "Book One.epub"))
	writeTestFile(t, filepath.Join(downloadDir, "Backlog Book.epub"))
	writeTestFile(t, filepath.Join(outside, "secret.epub"))
	// Mirror the production wiring (#1373): the manual-import allow-list holds
	// the library root AND the download dir.
	h.WithRoots(NewLibraryRoots(nil, libraryDir, downloadDir))

	for _, dir := range []string{libraryDir, downloadDir} {
		rec := httptest.NewRecorder()
		h.Scan(rec, scanRequest(dir))
		if rec.Code != http.StatusOK {
			t.Errorf("Scan(%s) status = %d, want 200; body = %s", dir, rec.Code, rec.Body.String())
			continue
		}
		var resp ScanResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Items) != 1 {
			t.Errorf("Scan(%s) items = %d, want 1", dir, len(resp.Items))
		}
	}

	// The gate itself still stands: an unconfigured dir is rejected.
	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(outside))
	if rec.Code != http.StatusForbidden {
		t.Errorf("Scan(outside) status = %d, want 403", rec.Code)
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

// TestManualImportReassign_SymlinkedLibrary_DetachesSource reproduces #1368
// Bug A: when the file being reassigned is stored in book_files under an
// unresolved (symlinked) path, prepareImport resolves it via EvalSymlinks and
// the detach must STILL empty the source book. Before the fix the detach ran
// against the resolved path only, missed the exact-match book_files row, and
// left the source book holding a live reference — so a later delete removed the
// target's file.
func TestManualImportReassign_SymlinkedLibrary_DetachesSource(t *testing.T) {
	realDir := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "lib")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	// Physical file lives under the real directory.
	authorDir := filepath.Join(realDir, "Author")
	if err := os.MkdirAll(authorDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authorDir, "book.m4b"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The path AS STORED in book_files goes through the symlink.
	storedPath := filepath.Join(link, "Author", "book.m4b")

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	downloads := db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	src := seedBook(t, authors, books, ctx)
	if err := books.AddBookFile(ctx, src.ID, models.MediaTypeAudiobook, storedPath); err != nil {
		t.Fatalf("attach audiobook to source: %v", err)
	}
	target := &models.Book{
		ForeignID: "mi-target", AuthorID: src.AuthorID,
		Title: "Correct Book", SortTitle: "correct book",
		Status: "wanted", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, target); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books).
		WithRoots(NewLibraryRoots(staticRootLister{paths: []string{link}}))
	body, _ := json.Marshal(map[string]any{"path": storedPath, "targetBookId": target.ID, "format": models.MediaTypeAudiobook})
	rec := httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	// The source must be fully detached despite the symlink path mismatch.
	srcFiles, err := books.ListFiles(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcFiles) != 0 {
		t.Errorf("source still has %d book_files row(s) after reassign; want 0 (detach must match the symlinked stored path)", len(srcFiles))
	}
	after, err := books.GetByID(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AudiobookFilePath != "" || after.FilePath != "" {
		t.Errorf("source legacy paths not cleared: audiobook=%q file=%q", after.AudiobookFilePath, after.FilePath)
	}
	if after.Status != models.BookStatusWanted {
		t.Errorf("source status = %q, want wanted after losing its only file", after.Status)
	}
}

// TestRemoveStaleSource_KeepsSourceWhenImportPlacedNothing reproduces #1368
// Bug B: removeStaleSource must not delete the source file just because the
// target already owns an unrelated file (e.g. an ebook). Only a file absent
// from the pre-import snapshot proves this reassign actually moved something.
func TestRemoveStaleSource_KeepsSourceWhenImportPlacedNothing(t *testing.T) {
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
	h := NewManualImportHandler(&stubManualImportScanner{}, downloads, books)

	target := seedBook(t, authors, books, ctx)
	tmp := t.TempDir()

	// Target already owns an unrelated ebook that exists on disk.
	existing := filepath.Join(tmp, "existing.epub")
	if err := os.WriteFile(existing, []byte("e"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, target.ID, models.MediaTypeEbook, existing); err != nil {
		t.Fatal(err)
	}
	// Snapshot taken before the (failed / no-op) import: it already contains the
	// pre-existing ebook.
	preexisting := h.targetFilePaths(ctx, target.ID)

	src := filepath.Join(tmp, "source.m4b")
	if err := os.WriteFile(src, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The import placed nothing new for the target, so the source must survive.
	h.removeStaleSource(ctx, src, target.ID, preexisting)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source file deleted despite no new import (data loss): %v", err)
	}

	// Positive control: once the import DOES add a new file the source is cleaned.
	moved := filepath.Join(tmp, "moved.m4b")
	if err := os.WriteFile(moved, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, target.ID, models.MediaTypeAudiobook, moved); err != nil {
		t.Fatal(err)
	}
	h.removeStaleSource(ctx, src, target.ID, preexisting)
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should be removed once a new file is placed at the target; stat err=%v", err)
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
