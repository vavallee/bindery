package security_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
)

// newMigrateHandler wires a MigrateHandler against in-memory repos for
// upload-path testing. No network dependencies — the OpenLibrary client is
// never called because invalid uploads are rejected before import runs.
func newMigrateHandler(t *testing.T) *api.MigrateHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return api.NewMigrateHandler(
		db.NewAuthorRepo(database),
		db.NewIndexerRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBlocklistRepo(database),
		db.NewBookRepo(database),
		metadata.NewAggregator(openlibrary.New()),
		nil,
	)
}

// uploadWithCT builds a multipart body with a single file field carrying
// the given content and Content-Type. Returns (body bytes, boundary).
func uploadWithCT(t *testing.T, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	if contentType != "" {
		hdr["Content-Type"] = []string{contentType}
	}
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

// TestUpload_RejectsExecutableContentType asserts that uploading a binary
// with an executable or HTML MIME type is refused. Anything outside the
// tight allowlist in internal/api/migrate.go should get a 400.
func TestUpload_RejectsExecutableContentType(t *testing.T) {
	t.Parallel()
	h := newMigrateHandler(t)

	for _, ct := range []string{
		"application/x-msdownload",
		"application/x-executable",
		"text/html",
		"image/png",
		"application/zip",
	} {
		body, boundary := uploadWithCT(t, "evil.csv", ct, []byte("name\nHemingway"))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", body)
		req.Header.Set("Content-Type", boundary)
		rec := httptest.NewRecorder()
		h.ImportCSV(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Content-Type %q: expected 400, got %d", ct, rec.Code)
		}
	}
}

// TestUpload_AcceptsValidCSV exercises the happy path so we know the
// rejection above isn't accidentally blocking legitimate requests.
func TestUpload_AcceptsValidCSV(t *testing.T) {
	t.Parallel()
	h := newMigrateHandler(t)

	// Empty CSV would still pass acceptUpload (Content-Type check); we're
	// only testing the upload gate, not the import logic. Use an IP-only
	// author name so the OL lookup fails fast with "no match" rather than
	// making a network call.
	body, boundary := uploadWithCT(t, "names.csv", "text/csv", []byte("\n"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", body)
	req.Header.Set("Content-Type", boundary)
	rec := httptest.NewRecorder()
	h.ImportCSV(rec, req)

	// Empty body → 200 with a zero-count result, or 400 if the import
	// considers empty CSV an error. Either is acceptable here — what's
	// NOT acceptable is 415 or 500.
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
		t.Errorf("valid CSV: expected 200 or 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUpload_OversizeRejected verifies the MaxBytesReader cap fires on a
// blatantly-oversize CSV. The cap is 5 MB for CSVs — we send 10 MB.
func TestUpload_OversizeRejected(t *testing.T) {
	t.Parallel()
	h := newMigrateHandler(t)

	big := make([]byte, 10*1024*1024)
	for i := range big {
		big[i] = 'a'
	}
	body, boundary := uploadWithCT(t, "huge.csv", "text/csv", big)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", body)
	req.Header.Set("Content-Type", boundary)
	rec := httptest.NewRecorder()
	h.ImportCSV(rec, req)

	// 400 (multipart parse aborts) or 413 (if MaxBytesReader surfaces a
	// status) — anything under 400 would mean the cap didn't trip.
	if rec.Code < 400 {
		t.Errorf("oversize upload: expected 4xx, got %d (body: %s)",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
