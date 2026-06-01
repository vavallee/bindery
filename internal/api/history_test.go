package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
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

// --- D3 per-user scoping ---------------------------------------------------

// seedTwoUserHistory creates two users, an owned book each, and one history
// event per book. Returns the handler, user ids, and event ids.
func seedTwoUserHistory(t *testing.T) (*HistoryHandler, *sql.DB, int64, int64, int64, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	uA, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	uB, err := users.Create(ctx, "bob", "h")
	if err != nil {
		t.Fatal(err)
	}

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	aA := &models.Author{ForeignID: "a-alice", Name: "Aa", SortName: "Aa"}
	if err := authors.CreateForUser(ctx, aA, uA.ID); err != nil {
		t.Fatal(err)
	}
	aB := &models.Author{ForeignID: "a-bob", Name: "Ab", SortName: "Ab"}
	if err := authors.CreateForUser(ctx, aB, uB.ID); err != nil {
		t.Fatal(err)
	}
	bA := &models.Book{ForeignID: "fA", AuthorID: aA.ID, Title: "Alice Book", SortTitle: "Alice Book"}
	if err := books.Create(ctx, bA); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uA.ID, bA.ID); err != nil {
		t.Fatal(err)
	}
	bB := &models.Book{ForeignID: "fB", AuthorID: aB.ID, Title: "Bob Book", SortTitle: "Bob Book"}
	if err := books.Create(ctx, bB); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uB.ID, bB.ID); err != nil {
		t.Fatal(err)
	}

	hist := db.NewHistoryRepo(database)
	blk := db.NewBlocklistRepo(database)
	eA := &models.HistoryEvent{BookID: &bA.ID, EventType: models.HistoryEventGrabbed, SourceTitle: "alice grab"}
	if err := hist.Create(ctx, eA); err != nil {
		t.Fatal(err)
	}
	eB := &models.HistoryEvent{BookID: &bB.ID, EventType: models.HistoryEventGrabbed, SourceTitle: "bob grab"}
	if err := hist.Create(ctx, eB); err != nil {
		t.Fatal(err)
	}
	return NewHistoryHandler(hist, blk), database, uA.ID, uB.ID, eA.ID, eB.ID
}

func TestHistoryDelete_CrossUserBlockedWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, _, _, uBob, eA, _ := seedTwoUserHistory(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/history/"+strconv.FormatInt(eA, 10), nil), "id", strconv.FormatInt(eA, 10))
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.Delete(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob deleting alice's event, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHistoryBlocklist_CrossUserBlockedWhenGateOn — promoting another user's
// grab into your own blocklist would leak event existence + pollute their
// search results, so refuse it under the gate.
func TestHistoryBlocklist_CrossUserBlockedWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, _, _, uBob, eA, _ := seedTwoUserHistory(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+strconv.FormatInt(eA, 10)+"/blocklist", nil), "id", strconv.FormatInt(eA, 10))
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.Blocklist(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob blocklisting alice's event, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHistoryList_FiltersToCallerWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, _, _, uBob, _, eB := seedTwoUserHistory(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history", nil)
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.List(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var got []models.HistoryEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != eB {
		t.Errorf("bob should see only his event; got %+v", got)
	}
}

func TestHistoryDelete_GateOffPreservesLegacyBehavior(t *testing.T) {
	// Default: gate off.
	h, _, _, uBob, eA, _ := seedTwoUserHistory(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/history/"+strconv.FormatInt(eA, 10), nil), "id", strconv.FormatInt(eA, 10))
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.Delete(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNoContent {
		t.Errorf("legacy: bob should be able to delete alice's event (gate off); status=%d body=%s", rec.Code, rec.Body.String())
	}
}
