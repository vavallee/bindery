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
	if !progress.Running {
		t.Errorf("expected progress.Running=true immediately after start, got %+v", progress)
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

	// Immediately send a second — should hit the already-running guard.
	second := sendRequest()
	if second.Code != http.StatusConflict {
		t.Errorf("second request: expected 409, got %d body=%s", second.Code, second.Body.String())
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
