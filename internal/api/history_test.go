package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func historyFixture(t *testing.T) (*HistoryHandler, *db.HistoryRepo, *db.BlocklistRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	history := db.NewHistoryRepo(database)
	blocklist := db.NewBlocklistRepo(database)
	return NewHistoryHandler(history, blocklist), history, blocklist, context.Background()
}

func TestHistoryList_EmptyReturnsArray(t *testing.T) {
	h, _, _, _ := historyFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []models.HistoryEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected JSON array, got %s", rec.Body.String())
	}
}

// TestHistoryList_FiltersByEventType — the UI's event-type dropdown uses
// ?eventType=grabbed. Regression here would show all events regardless of
// the filter.
func TestHistoryList_FiltersByEventType(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	for _, e := range []*models.HistoryEvent{
		{EventType: models.HistoryEventGrabbed, SourceTitle: "A"},
		{EventType: models.HistoryEventGrabbed, SourceTitle: "B"},
		{EventType: models.HistoryEventDownloadFailed, SourceTitle: "C"},
	} {
		if err := history.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history?eventType="+models.HistoryEventGrabbed, nil))
	var got []models.HistoryEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 grabbed events, got %d", len(got))
	}
}

func TestHistoryDelete_Success(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	e := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "A"}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/history/"+strconv.FormatInt(e.ID, 10), nil), "id", strconv.FormatInt(e.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}

func TestHistoryDelete_BadID(t *testing.T) {
	h, _, _, _ := historyFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/history/abc", nil), "id", "abc")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHistoryBlocklist_PromotesGrab rolls the "mark as failed and block"
// action: reading the stored `guid` out of the event data, creating a
// blocklist row keyed on it. A bug here lets the same bad release be
// re-grabbed on the next RSS sync.
func TestHistoryBlocklist_PromotesGrab(t *testing.T) {
	h, history, blocklist, ctx := historyFixture(t)
	data, _ := json.Marshal(map[string]any{"guid": "nzb-guid-42"})
	e := &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Bad Release",
		Data: string(data),
	}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+strconv.FormatInt(e.ID, 10)+"/blocklist", nil), "id", strconv.FormatInt(e.ID, 10))
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	entries, _ := blocklist.List(ctx)
	if len(entries) != 1 || entries[0].GUID != "nzb-guid-42" {
		t.Errorf("expected blocklist entry keyed on guid, got %+v", entries)
	}
}

// TestHistoryBlocklist_FallsBackToTitle — some legacy events lack the guid
// field. Falling back to the sourceTitle keeps blocklist useful rather than
// failing silently.
func TestHistoryBlocklist_FallsBackToTitle(t *testing.T) {
	h, history, blocklist, ctx := historyFixture(t)
	e := &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Legacy Release",
		Data: "", // no guid
	}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+strconv.FormatInt(e.ID, 10)+"/blocklist", nil), "id", strconv.FormatInt(e.ID, 10))
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	entries, _ := blocklist.List(ctx)
	if len(entries) != 1 || entries[0].GUID != "Legacy Release" {
		t.Errorf("expected GUID fallback to title, got %+v", entries)
	}
}

func TestHistoryBlocklist_NotFound(t *testing.T) {
	h, _, _, _ := historyFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/999/blocklist", nil), "id", "999")
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
