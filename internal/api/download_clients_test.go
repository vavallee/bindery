package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func downloadClientFixture(t *testing.T) (*DownloadClientHandler, *db.DownloadClientRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	clients := db.NewDownloadClientRepo(database)
	return NewDownloadClientHandler(clients), clients
}

func TestDownloadClientList_Empty(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/downloadclient", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("expected empty list, got %d items", len(out))
	}
}

func TestDownloadClientCRUD(t *testing.T) {
	h, clients := downloadClientFixture(t)
	ctx := context.Background()

	// Create — valid. Use RFC1918 IP literal so the SSRF validator's LAN
	// policy accepts it without needing DNS in the test environment.
	body := `{"name":"My SAB","host":"10.10.10.10","port":8080,"type":"sabnzbd","apiKey":"key1","enabled":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	// List — should have one entry
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/downloadclient", nil))
	var list []models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("expected 1 client, got %d", len(list))
	}

	// Get by ID
	idStr := "1"
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/downloadclient/1", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — not found
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/downloadclient/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	update := `{"name":"Updated SAB","host":"10.10.10.11","port":8080,"type":"sabnzbd","apiKey":"key2","enabled":false}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/downloadclient/1", bytes.NewBufferString(update)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := clients.GetByID(ctx, created.ID)
	if got == nil {
		t.Fatal("expected client still exists after update")
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/downloadclient/1", nil), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}
}

func TestDownloadClientCreate_Validation(t *testing.T) {
	h, _ := downloadClientFixture(t)
	for _, body := range []string{
		`{}`,
		`{"name":"x"}`,         // missing host
		`{"host":"localhost"}`, // missing name
		`not-json`,
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestDownloadClientCreate_Defaults(t *testing.T) {
	h, clients := downloadClientFixture(t)
	ctx := context.Background()
	body := `{"name":"SAB","host":"10.10.10.10"}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	var created models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&created)
	got, _ := clients.GetByID(ctx, created.ID)
	if got.Type != "sabnzbd" {
		t.Errorf("default type: want sabnzbd, got %q", got.Type)
	}
	if got.Port != 8080 {
		t.Errorf("default port: want 8080, got %d", got.Port)
	}
	if got.Category != "books" {
		t.Errorf("default category: want books, got %q", got.Category)
	}
}

func TestDownloadClientTest_NotFound(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/999/test", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing client, got %d", rec.Code)
	}
}
