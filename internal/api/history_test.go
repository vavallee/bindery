package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// TestHistoryList_EmptyReturnsArray verifies a fresh history table returns
// the paginated envelope with items=[] (not null) so the frontend can render
// without a null-check. The shape was a bare array pre-Wave-2.
func TestHistoryList_EmptyReturnsArray(t *testing.T) {
	h, _, _, _ := historyFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	if got.Items == nil {
		t.Errorf("expected items=[] (not null), got %s", rec.Body.String())
	}
	if got.Total != 0 || got.Limit != historyListDefaultLimit {
		t.Errorf("expected default-limit zero-total envelope, got %+v", got)
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
	var got historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Errorf("expected 2 grabbed events, got total=%d items=%d", got.Total, len(got.Items))
	}
}

// seedHistoryEvents seeds n grabbed events. The sequential Create calls
// give them strictly increasing IDs; the ORDER BY created_at DESC, id DESC
// in HistoryRepo.ListPage yields the reverse-insertion order on read.
func seedHistoryEvents(t *testing.T, history *db.HistoryRepo, ctx context.Context, n int) []string {
	t.Helper()
	titles := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		title := fmt.Sprintf("Event %03d", i)
		if err := history.Create(ctx, &models.HistoryEvent{
			EventType: models.HistoryEventGrabbed, SourceTitle: title,
		}); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	return titles
}

// TestHistoryList_Paginates seeds 10 events and pages through them.
func TestHistoryList_Paginates(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	titles := seedHistoryEvents(t, history, ctx, 10)
	// titles is insertion order (oldest first). The list endpoint returns
	// newest first, so the expected page 0 is titles[9..7].
	wantFirst := []string{titles[9], titles[8], titles[7]}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history?limit=3&offset=0", nil))
	var first historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.Total != 10 || first.Limit != 3 || first.Offset != 0 || len(first.Items) != 3 {
		t.Errorf("first page = %+v, want total=10 limit=3 offset=0 len=3", first)
	}
	for i, e := range first.Items {
		if e.SourceTitle != wantFirst[i] {
			t.Errorf("first page item %d = %q, want %q", i, e.SourceTitle, wantFirst[i])
		}
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history?limit=3&offset=9", nil))
	var tail historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tail); err != nil {
		t.Fatalf("decode tail: %v", err)
	}
	if tail.Total != 10 || len(tail.Items) != 1 || tail.Items[0].SourceTitle != titles[0] {
		t.Errorf("tail page = %+v, want one item %q (oldest)", tail, titles[0])
	}
}

// TestHistoryList_DefaultsAndCaps confirms default + max limit behaviour.
func TestHistoryList_DefaultsAndCaps(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	seedHistoryEvents(t, history, ctx, 3)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	var defaults historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &defaults); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if defaults.Limit != historyListDefaultLimit {
		t.Errorf("expected default limit %d, got %d", historyListDefaultLimit, defaults.Limit)
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history?limit=10000", nil))
	var clamped historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &clamped); err != nil {
		t.Fatalf("decode clamped: %v", err)
	}
	if clamped.Limit != historyListMaxLimit {
		t.Errorf("expected clamped limit %d, got %d", historyListMaxLimit, clamped.Limit)
	}
}

// TestHistoryList_OrderStable — repeated identical requests must return the
// same rows in the same order. Backed by the (created_at DESC, id DESC) tie
// breaker in HistoryRepo.ListPage so rows sharing a timestamp don't shuffle.
func TestHistoryList_OrderStable(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	seedHistoryEvents(t, history, ctx, 5)
	collect := func() []string {
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history?limit=5", nil))
		var page historyListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		titles := make([]string, len(page.Items))
		for i, e := range page.Items {
			titles[i] = e.SourceTitle
		}
		return titles
	}
	first := collect()
	second := collect()
	if len(first) != 5 || len(second) != 5 {
		t.Fatalf("expected 5+5 items, got %d/%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("order changed at %d: %q vs %q", i, first[i], second[i])
		}
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

// TestBlocklistHandler_HistoryBlocklistRecordsCaller covers the D4b audit
// contract end-to-end: when an authenticated user promotes a history event
// to the blocklist, the persisted row carries their uid in
// created_by_user_id. The handler decides this from the request context, so
// the test drives it with an auth-tagged ctx and asserts the row in the
// repo.
func TestBlocklistHandler_HistoryBlocklistRecordsCaller(t *testing.T) {
	// Use OpenMemory directly so we can seed a real user before the
	// handler writes the audit row. The blocklist.created_by_user_id FK
	// to users(id) means a synthetic uid would trip the constraint.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	u, err := users.Create(ctx, "audit-alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	history := db.NewHistoryRepo(database)
	blocklist := db.NewBlocklistRepo(database)
	h := NewHistoryHandler(history, blocklist)

	data, _ := json.Marshal(map[string]any{"guid": "nzb-guid-d4b"})
	e := &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Tagged Release",
		Data: string(data),
	}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+strconv.FormatInt(e.ID, 10)+"/blocklist", nil), "id", strconv.FormatInt(e.ID, 10))
	// Tag the request ctx with a user id so the handler routes through
	// CreateByUser. Role is irrelevant here; the audit write happens
	// regardless of role.
	reqCtx := auth.WithUserID(req.Context(), u.ID)
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req.WithContext(reqCtx))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	entries, err := blocklist.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 blocklist row, got %d", len(entries))
	}
	if entries[0].CreatedByUserID == nil || *entries[0].CreatedByUserID != u.ID {
		t.Errorf("expected CreatedByUserID=%d, got %v", u.ID, entries[0].CreatedByUserID)
	}
}

// TestBlocklistHandler_HistoryBlocklistAnonymousFallsBackToSystem asserts
// the handler does not invent a fake uid when the request context has no
// authenticated user (UserIDFromContext returns 0). The row is still
// created (preserving the legacy unauth-friendly behavior), but the audit
// column stays NULL like a system-write.
func TestBlocklistHandler_HistoryBlocklistAnonymousFallsBackToSystem(t *testing.T) {
	h, history, blocklist, ctx := historyFixture(t)
	data, _ := json.Marshal(map[string]any{"guid": "nzb-guid-anon"})
	e := &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Anon Release",
		Data: string(data),
	}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+strconv.FormatInt(e.ID, 10)+"/blocklist", nil), "id", strconv.FormatInt(e.ID, 10))
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req) // no ctx auth tagging
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	entries, _ := blocklist.List(ctx)
	if len(entries) != 1 {
		t.Fatalf("expected 1 blocklist row, got %d", len(entries))
	}
	if entries[0].CreatedByUserID != nil {
		t.Errorf("expected CreatedByUserID=nil for anonymous caller, got %v", *entries[0].CreatedByUserID)
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
	// Wave 2 / E introduced a Page envelope { items, total, limit, offset };
	// unwrap items here.
	var resp historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != eB {
		t.Errorf("bob should see only his event; got %+v", resp.Items)
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
