package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func blocklistFixture(t *testing.T) (*BlocklistHandler, *db.BlocklistRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewBlocklistRepo(database)
	return NewBlocklistHandler(repo), repo, context.Background()
}

// TestBlocklistList_EmptyReturnsArray — same null-safety contract as the
// books list: never serialize null so the frontend can render unconditionally.
func TestBlocklistList_EmptyReturnsArray(t *testing.T) {
	h, _, _ := blocklistFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/blocklist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestBlocklistList_ReturnsEntries(t *testing.T) {
	h, repo, ctx := blocklistFixture(t)
	for _, e := range []*models.BlocklistEntry{
		{GUID: "g1", Title: "Bad 1", Reason: "nuked"},
		{GUID: "g2", Title: "Bad 2", Reason: "wrong edition"},
	} {
		if err := repo.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/blocklist", nil))
	var got []models.BlocklistEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d", len(got))
	}
}

func TestBlocklistDelete_BadID(t *testing.T) {
	h, _, _ := blocklistFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/blocklist/abc", nil), "id", "abc")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestBlocklistDelete_Success(t *testing.T) {
	h, repo, ctx := blocklistFixture(t)
	e := &models.BlocklistEntry{GUID: "g1", Title: "Bad", Reason: "nuked"}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/blocklist/"+strconv.FormatInt(e.ID, 10), nil), "id", strconv.FormatInt(e.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	remaining, _ := repo.List(ctx)
	if len(remaining) != 0 {
		t.Errorf("expected entry removed, %d remain", len(remaining))
	}
}

// TestBlocklistBulkDelete sweeps multiple ids — used by the UI's "remove
// selected" action.
func TestBlocklistBulkDelete(t *testing.T) {
	h, repo, ctx := blocklistFixture(t)
	ids := []int64{}
	for _, g := range []string{"g1", "g2", "g3"} {
		e := &models.BlocklistEntry{GUID: g, Title: g, Reason: "x"}
		if err := repo.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	// Delete first two.
	body, _ := json.Marshal(map[string][]int64{"ids": ids[:2]})
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/blocklist/bulk-delete", bytes.NewBuffer(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	remaining, _ := repo.List(ctx)
	if len(remaining) != 1 {
		t.Errorf("expected 1 entry to remain, got %d", len(remaining))
	}
}

func TestBlocklistBulkDelete_BadBody(t *testing.T) {
	h, _, _ := blocklistFixture(t)
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/blocklist/bulk-delete", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestBlocklistBulkDelete_EmptyIDs — zero-length ids is a 204 no-op, not
// an error. The UI calls this defensively even when the selection is empty.
func TestBlocklistBulkDelete_EmptyIDs(t *testing.T) {
	h, _, _ := blocklistFixture(t)
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/blocklist/bulk-delete", bytes.NewBufferString(`{"ids":[]}`)))
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}
