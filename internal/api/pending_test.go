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

// seedTwoUserPending creates two users, an owned book each, and one pending
// release per book. Returns the handler, user ids, and pending ids.
func seedTwoUserPending(t *testing.T) (*PendingHandler, *sql.DB, int64, int64, int64, int64) {
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

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
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

	pending := db.NewPendingReleaseRepo(database)
	prA := &models.PendingRelease{BookID: bA.ID, MediaType: models.MediaTypeEbook, Title: "alice rel", GUID: "guid-a", Protocol: "usenet", Reason: "delay"}
	if err := pending.Upsert(ctx, prA); err != nil {
		t.Fatal(err)
	}
	prB := &models.PendingRelease{BookID: bB.ID, MediaType: models.MediaTypeEbook, Title: "bob rel", GUID: "guid-b", Protocol: "usenet", Reason: "delay"}
	if err := pending.Upsert(ctx, prB); err != nil {
		t.Fatal(err)
	}
	// Re-read to discover the auto-assigned IDs.
	all, err := pending.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var idA, idB int64
	for _, p := range all {
		switch p.GUID {
		case "guid-a":
			idA = p.ID
		case "guid-b":
			idB = p.ID
		}
	}
	if idA == 0 || idB == 0 {
		t.Fatalf("expected both pending releases inserted; got idA=%d idB=%d", idA, idB)
	}

	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	history := db.NewHistoryRepo(database)
	queue := NewQueueHandler(downloads, clients, books, history)
	h := NewPendingHandler(pending, queue, downloads, books)
	return h, database, uA.ID, uB.ID, idA, idB
}

// TestPendingDelete_CrossUserBlockedWhenGateOn — user B cannot dismiss user
// A's pending release.
func TestPendingDelete_CrossUserBlockedWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, _, _, uBob, idA, _ := seedTwoUserPending(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/pending/"+strconv.FormatInt(idA, 10), nil), "id", strconv.FormatInt(idA, 10))
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.Delete(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob deleting alice's pending, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPendingList_FiltersToCallerWhenGateOn(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, _, _, uBob, _, idB := seedTwoUserPending(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pending", nil)
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.List(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var got []models.PendingRelease
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != idB {
		t.Errorf("bob should see only his pending release; got %+v", got)
	}
}

func TestPendingDelete_GateOffPreservesLegacyBehavior(t *testing.T) {
	h, _, _, uBob, idA, _ := seedTwoUserPending(t)

	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/pending/"+strconv.FormatInt(idA, 10), nil), "id", strconv.FormatInt(idA, 10))
	ctx := auth.WithUserID(req.Context(), uBob)
	ctx = auth.WithUserRole(ctx, "user")
	h.Delete(rec, req.WithContext(ctx))
	if rec.Code != http.StatusNoContent {
		t.Errorf("legacy: bob should be able to delete alice's pending (gate off); status=%d body=%s", rec.Code, rec.Body.String())
	}
}
