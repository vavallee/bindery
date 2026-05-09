package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func importListFixture(t *testing.T) *ImportListHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewImportListHandler(db.NewImportListRepo(database), nil)
}

func TestImportListList_Empty(t *testing.T) {
	h := importListFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/import-list", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Expect [] not null so the UI can render.
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestImportListCRUD(t *testing.T) {
	h := importListFixture(t)

	// Create (name-only; Type should default to "csv")
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list",
		bytes.NewBufferString(`{"name":"My CSV","url":""}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 || created.Type != "csv" {
		t.Fatalf("unexpected created list: %+v", created)
	}

	// Get
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — bad id
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("get bad id: expected 400, got %d", rec.Code)
	}

	// Get — missing
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1",
			bytes.NewBufferString(`{"name":"Renamed","type":"csv"}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d", rec.Code)
	}

	// Update — bad id
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/abc", bytes.NewBufferString(`{}`)),
		"id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad id: expected 400, got %d", rec.Code)
	}

	// Update — missing
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/999", bytes.NewBufferString(`{"name":"x"}`)),
		"id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("update missing: expected 404, got %d", rec.Code)
	}

	// Update — invalid body on existing row
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`not-json`)),
		"id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad body: expected 400, got %d", rec.Code)
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}

	// Delete — bad id
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete bad id: expected 400, got %d", rec.Code)
	}
}

func TestImportListCreate_Validation(t *testing.T) {
	h := importListFixture(t)
	for _, tc := range []struct {
		body string
		desc string
	}{
		{`not-json`, "invalid json"},
		{`{}`, "missing name"},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list", bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tc.desc, rec.Code)
		}
	}
}

func TestImportListExclusions(t *testing.T) {
	h := importListFixture(t)

	// List empty
	rec := httptest.NewRecorder()
	h.ListExclusions(rec, httptest.NewRequest(http.MethodGet, "/api/v1/import-list/exclusion", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}

	// Create — validation
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid json: expected 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing foreignId: expected 400, got %d", rec.Code)
	}

	// Create — success
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`{"foreignId":"OL1A","title":"T","authorName":"A"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete
	rec = httptest.NewRecorder()
	h.DeleteExclusion(rec, withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/exclusion/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}

	// Delete — bad id
	rec = httptest.NewRecorder()
	h.DeleteExclusion(rec, withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/exclusion/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}
}
