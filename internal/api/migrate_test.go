package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/migrate"
	"github.com/vavallee/bindery/internal/models"
)

func migrateFixture(t *testing.T, primary metadata.Provider) *MigrateHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewMigrateHandler(
		db.NewAuthorRepo(database),
		db.NewIndexerRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBlocklistRepo(database),
		db.NewBookRepo(database),
		db.NewSettingsRepo(database),
		metadata.NewAggregator(primary),
		nil,
	)
}

// multipartBody builds a multipart/form-data body with a single "file" field.
func multipartBody(t *testing.T, field, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return body, w.FormDataContentType()
}

// multipartBodyWithFields builds a multipart body with a "file" field plus
// extra plain text form fields.
func multipartBodyWithFields(t *testing.T, filename, content string, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	return body, w.FormDataContentType()
}

func TestMigrate_ImportCSV_BadMultipart(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	// Not multipart at all
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", bytes.NewBufferString("junk"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ImportCSV(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-multipart: expected 400, got %d", rec.Code)
	}
}

func TestMigrate_ImportCSV_MissingFileField(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	// Multipart with wrong field name
	body, ct := multipartBody(t, "notfile", "x.csv", "Andy Weir\n")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportCSV(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no file field: expected 400, got %d", rec.Code)
	}
}

func TestMigrate_ImportCSV_Success(t *testing.T) {
	p := &stubProvider{
		authors: []models.Author{{Name: "Andy Weir", ForeignID: "OL1A"}},
	}
	h := migrateFixture(t, p)

	body, ct := multipartBody(t, "file", "authors.csv", "Andy Weir\n")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportCSV(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Payload should be JSON with "requested" field.
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["requested"]; !ok {
		t.Errorf("expected requested field in result, got %+v", got)
	}
}

func TestMigrate_ImportReadarr_BadMultipart(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/readarr", bytes.NewBufferString("junk"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ImportReadarr(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-multipart: expected 400, got %d", rec.Code)
	}
}

func TestUploadTempDir_NoEnv(t *testing.T) {
	t.Setenv("BINDERY_DB_PATH", "")
	if got := uploadTempDir(); got != "" {
		t.Errorf("no env: want empty, got %q", got)
	}
}

func TestUploadTempDir_CreatesDirNextToDB(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "bindery.db")
	t.Setenv("BINDERY_DB_PATH", dbPath)

	got := uploadTempDir()
	want := filepath.Join(root, "tmp")
	if got != want {
		t.Errorf("path: want %q, got %q", want, got)
	}
	// Must exist (MkdirAll).
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Errorf("expected dir at %q (err=%v)", got, err)
	}
}

// TestMigrate_ImportReadarr_InvalidDB verifies that uploading garbage bytes
// (not a valid SQLite file) returns 202 Accepted immediately (the import is
// async) and that polling the status endpoint eventually shows an error —
// rather than the handler silently dropping the connection and causing a
// "NetworkError when attempting to fetch resource" in the browser (issue #398).
func TestMigrate_ImportReadarr_InvalidDB(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	// Upload garbage bytes — the SQLite driver should reject them once the
	// goroutine opens the file, but the HTTP handler must return 202 first.
	body, ct := multipartBody(t, "file", "readarr.db", "not a real sqlite file")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/readarr", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportReadarr(rec, req)

	// The handler must respond 202 immediately — never a network-level close.
	if rec.Code != http.StatusAccepted {
		t.Errorf("invalid db: expected 202 Accepted (async), got %d body=%s",
			rec.Code, rec.Body.String())
	}

	// Body must decode as ReadarrProgress with Running=true.
	var progress migrate.ReadarrProgress
	if err := json.NewDecoder(rec.Body).Decode(&progress); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	// The import runs in a background goroutine. With an invalid SQLite file
	// the goroutine can fail and finish in microseconds — before the handler
	// serialises this response — so Running may already be false. The handler's
	// contract is "respond 202 immediately"; observing the async job as either
	// still-running or already-finished-with-an-error both satisfy it. Asserting
	// Running==true here was a scheduling race (#702). A non-running response
	// with no error recorded would be the genuine bug, so that still fails.
	if !progress.Running && progress.Error == "" {
		t.Errorf("expected the import to be running or finished with an error, got %+v", progress)
	}
	if progress.StartedAt.IsZero() {
		t.Errorf("expected StartedAt to be set, got zero")
	}

	// Poll status until the goroutine finishes (invalid SQLite → fast error).
	deadline := time.Now().Add(5 * time.Second)
	var finalProgress migrate.ReadarrProgress
	for time.Now().Before(deadline) {
		srec := httptest.NewRecorder()
		h.ImportReadarrStatus(srec, httptest.NewRequest(http.MethodGet, "/api/v1/migrate/readarr/status", nil))
		if srec.Code != http.StatusOK {
			t.Fatalf("status: expected 200, got %d", srec.Code)
		}
		var p migrate.ReadarrProgress
		if err := json.NewDecoder(srec.Body).Decode(&p); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if !p.Running {
			finalProgress = p
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The import must have finished with an error (bad SQLite).
	if finalProgress.Running {
		t.Fatal("import did not finish within 5 seconds")
	}
	if finalProgress.Error == "" {
		t.Errorf("expected an error for invalid SQLite, got empty error field: %+v", finalProgress)
	}
	if finalProgress.FinishedAt == nil {
		t.Error("expected FinishedAt to be set after completion")
	}
}

// TestMigrate_ImportReadarr_ConflictOnDoubleSubmit checks that a second
// concurrent upload is rejected with 409 Conflict when an import is already
// running, and that the temp file is cleaned up.
func TestMigrate_ImportReadarr_ConflictOnDoubleSubmit(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	// Block the goroutine at goroutine start until we've sent the second
	// request. Without this, the goroutine can fail on the bad SQLite payload
	// and clear running=false before the second HTTP call arrives (race).
	goroutineStarted := make(chan struct{})
	goroutineRelease := make(chan struct{})
	h.readarrImporter.SetTestStartHook(func() {
		close(goroutineStarted)
		<-goroutineRelease
	})

	sendRequest := func() *httptest.ResponseRecorder {
		body, ct := multipartBody(t, "file", "readarr.db", "not a real sqlite file")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/readarr", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h.ImportReadarr(rec, req)
		return rec
	}

	// First request must be accepted.
	first := sendRequest()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", first.Code)
	}

	// Wait until the goroutine has started (and is blocked at testHook) so
	// running=true is guaranteed to be visible to the second request.
	<-goroutineStarted

	// Second request must be rejected while the first is still in progress.
	second := sendRequest()
	if second.Code != http.StatusConflict {
		t.Errorf("second request: expected 409, got %d body=%s", second.Code, second.Body.String())
	}

	// Release the goroutine so it can finish and clean up.
	close(goroutineRelease)
}

// TestMigrate_ImportReadarrStatus_CatalogueFields pins the wire shape of the
// catalogue backfill counters. They are always present (zeroed before any
// import), so the UI can tell "none running" from an old server that omits
// them.
func TestMigrate_ImportReadarrStatus_CatalogueFields(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	rec := httptest.NewRecorder()
	h.ImportReadarrStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/migrate/readarr/status", nil))
	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, ok := raw["catalogueRunning"]; !ok || v != false {
		t.Errorf("catalogueRunning: want false, got %v (present=%v)", v, ok)
	}
	for _, k := range []string{"catalogueTotal", "catalogueDone"} {
		if v, ok := raw[k]; !ok || v != float64(0) {
			t.Errorf("%s: want 0, got %v (present=%v)", k, v, ok)
		}
	}
}

// TestMigrate_ImportReadarrStatus_BeforeFirstRun checks the status endpoint
// before any import has been kicked off returns 200 with Running=false.
func TestMigrate_ImportReadarrStatus_BeforeFirstRun(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	rec := httptest.NewRecorder()
	h.ImportReadarrStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/migrate/readarr/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var p migrate.ReadarrProgress
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Running {
		t.Errorf("expected Running=false before any import, got %+v", p)
	}
}

const goodreadsTestHeader = "Book Id,Title,Author,Author l-f,Additional Authors,ISBN,ISBN13,My Rating,Average Rating,Publisher,Binding,Number of Pages,Year Published,Original Publication Year,Date Read,Date Added,Bookshelves,Bookshelves with positions,Exclusive Shelf,My Review,Spoiler,Private Notes,Read Count,Owned Copies"

// TestMigrate_Goodreads_PreviewAndCommit drives the two-step flow end to end
// through the HTTP handlers with a stub metadata provider.
func TestMigrate_Goodreads_PreviewAndCommit(t *testing.T) {
	p := &stubProvider{
		byISBN: &models.Book{
			ForeignID: "OL-GR-W1",
			Title:     "Project Hail Mary",
			Author:    &models.Author{ForeignID: "OL-GR-A1", Name: "Andy Weir"},
		},
	}
	h := migrateFixture(t, p)
	h.goodreadsImporter.WithPacing(0) // no inter-lookup delay in tests

	csv := goodreadsTestHeader + "\n" +
		`1,Project Hail Mary,Andy Weir,"Weir, Andy",,="0593135202",="9780593135204",5,4.5,Pub,Hardcover,476,2021,2021,,2024/01/01,to-read,,to-read,,false,,0,0` + "\n" +
		// a read-shelf row: excluded by the default to-read-only filter
		`2,Some Read Book,Andy Weir,"Weir, Andy",,,,4,4.0,Pub,Paperback,300,2015,2015,,2023/01/01,read,,read,,false,,1,1` + "\n"

	body, ct := multipartBody(t, "file", "goodreads.csv", csv)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/preview", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportGoodreadsPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var preview migrate.GoodreadsPreview
	if err := json.NewDecoder(rec.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.TotalRows != 2 {
		t.Errorf("totalRows = %d, want 2", preview.TotalRows)
	}
	if preview.Resolved != 1 {
		t.Errorf("resolved = %d, want 1 (to-read only)", preview.Resolved)
	}
	if preview.SkippedShelf != 1 {
		t.Errorf("skippedShelf = %d, want 1", preview.SkippedShelf)
	}
	if preview.Token == "" {
		t.Fatal("preview must return a token")
	}

	// Commit the token.
	commitBody, _ := json.Marshal(map[string]string{"token": preview.Token})
	creq := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/commit", bytes.NewReader(commitBody))
	creq.Header.Set("Content-Type", "application/json")
	crec := httptest.NewRecorder()
	h.ImportGoodreadsCommit(crec, creq)
	if crec.Code != http.StatusOK {
		t.Fatalf("commit: expected 200, got %d: %s", crec.Code, crec.Body.String())
	}
	var result migrate.GoodreadsCommitResult
	if err := json.NewDecoder(crec.Body).Decode(&result); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if result.Added != 1 {
		t.Fatalf("commit added = %d, want 1; failures=%v", result.Added, result.Failures)
	}
}

// TestMigrate_Goodreads_ShelfFilter verifies the shelves form field widens the
// import beyond the to-read default.
func TestMigrate_Goodreads_ShelfFilter(t *testing.T) {
	p := &stubProvider{
		byISBN: &models.Book{
			ForeignID: "OL-GR-W2",
			Title:     "Read Book",
			Author:    &models.Author{ForeignID: "OL-GR-A2", Name: "Some Author"},
		},
	}
	h := migrateFixture(t, p)
	h.goodreadsImporter.WithPacing(0)

	csv := goodreadsTestHeader + "\n" +
		`1,Read Book,Some Author,"Author, Some",,="0000000000",="9780000000000",4,4.0,Pub,Paperback,300,2015,2015,,2023/01/01,read,,read,,false,,1,1` + "\n"

	body, ct := multipartBodyWithFields(t, "goodreads.csv", csv, map[string]string{"shelves": "to-read,read"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/preview", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportGoodreadsPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var preview migrate.GoodreadsPreview
	if err := json.NewDecoder(rec.Body).Decode(&preview); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if preview.Resolved != 1 {
		t.Errorf("resolved = %d, want 1 (read shelf explicitly included)", preview.Resolved)
	}
}

func TestMigrate_Goodreads_CommitUnknownToken(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})
	commitBody, _ := json.Marshal(map[string]string{"token": "does-not-exist"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/commit", bytes.NewReader(commitBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ImportGoodreadsCommit(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown token, got %d", rec.Code)
	}
}

func TestMigrate_Goodreads_PreviewBadCSV(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})
	// A CSV with no Title column is rejected.
	body, ct := multipartBody(t, "file", "bad.csv", "Author,ISBN\nSomeone,123\n")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/preview", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportGoodreadsPreview(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a CSV with no Title column, got %d", rec.Code)
	}
}
