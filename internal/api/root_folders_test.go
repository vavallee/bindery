package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func rootFolderFixture(t *testing.T) *RootFolderHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewRootFolderHandler(db.NewRootFolderRepo(database))
}

func TestRootFolderList_Empty(t *testing.T) {
	h := rootFolderFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/rootfolder", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []models.RootFolder
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("expected empty list, got %d items", len(out))
	}
}

func TestRootFolderCreate_Valid(t *testing.T) {
	dir := t.TempDir()
	h := rootFolderFixture(t)

	body, _ := json.Marshal(map[string]string{"path": dir})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var out models.RootFolder
	json.NewDecoder(rec.Body).Decode(&out)
	if out.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if out.Path != dir {
		t.Errorf("path mismatch: want %q, got %q", dir, out.Path)
	}
}

func TestRootFolderCreate_NonexistentPath(t *testing.T) {
	h := rootFolderFixture(t)
	body, _ := json.Marshal(map[string]string{"path": "/no/such/path/xyz"})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRootFolderCreate_NotADirectory(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	h := rootFolderFixture(t)
	body, _ := json.Marshal(map[string]string{"path": f.Name()})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRootFolderCreate_EmptyPath(t *testing.T) {
	h := rootFolderFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewBufferString(`{"path":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRootFolderDelete(t *testing.T) {
	dir := t.TempDir()
	h := rootFolderFixture(t)

	// Create one
	body, _ := json.Marshal(map[string]string{"path": dir})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewReader(body)))
	var created models.RootFolder
	json.NewDecoder(rec.Body).Decode(&created)

	// Delete it
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/rootfolder/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", rec.Code)
	}

	// List should be empty
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/rootfolder", nil))
	var list []models.RootFolder
	json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("expected empty list after delete, got %d", len(list))
	}
}

// TestRootFolderDelete_NonNumericID pins that a malformed {id} answers 400 and
// not 404. Delete used to run `id, _ := strconv.ParseInt(...)`, which turns
// "abc" into id 0 and reports "root folder not found" for a request that never
// named a root folder at all (#2364).
func TestRootFolderDelete_NonNumericID(t *testing.T) {
	h := rootFolderFixture(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/rootfolder/abc", nil), "id", "abc")

	h.Delete(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-numeric id, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if out["error"] != "invalid id" {
		t.Errorf("error = %q, want %q", out["error"], "invalid id")
	}
}
