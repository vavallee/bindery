package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMigrateGoodreadsCoverageRefusals covers the upload and body refusals on
// the Goodreads preview and commit endpoints.
func TestMigrateGoodreadsCoverageRefusals(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})

	preview := func(body *bytes.Buffer, ct string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/preview", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h.ImportGoodreadsPreview(rec, req)
		return rec
	}

	// Not multipart at all.
	if rec := preview(bytes.NewBufferString(`{"x":1}`), "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-multipart: %d, want 400", rec.Code)
	}
	// Multipart without the "file" field.
	body, ct := multipartBody(t, "upload", "export.csv", "Title,Author\nDune,Frank Herbert\n")
	if rec := preview(body, ct); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing file field: %d, want 400", rec.Code)
	}
	// A valid header with no data rows.
	body, ct = multipartBody(t, "file", "export.csv", "Title,Author\n")
	rec := preview(body, ct)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no usable rows") {
		t.Fatalf("header only: %d %s, want 400 no usable rows", rec.Code, rec.Body.String())
	}

	commit := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/goodreads/commit", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ImportGoodreadsCommit(rec, req)
		return rec
	}
	if rec := commit(`{"token":`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Fatalf("bad commit body: %d %s", rec.Code, rec.Body.String())
	}
	if rec := commit(`{"token":"   "}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "token is required") {
		t.Fatalf("blank token: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMigrateReadarrCoverageMissingFile: an upload without the "file" field
// is refused before anything is spooled or started.
func TestMigrateReadarrCoverageMissingFile(t *testing.T) {
	h := migrateFixture(t, &stubProvider{})
	body, ct := multipartBody(t, "notfile", "readarr.db", "x")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/readarr", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ImportReadarr(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing file: %d %s, want 400", rec.Code, rec.Body.String())
	}
	status := httptest.NewRecorder()
	h.ImportReadarrStatus(status, httptest.NewRequest(http.MethodGet, "/api/v1/migrate/readarr/status", nil))
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `"running":true`) {
		t.Fatalf("status after refused upload: %d %s", status.Code, status.Body.String())
	}
}
