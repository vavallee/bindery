package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// deleteBookWithHistory gives alice's book a grab row, then has alice delete
// the book through the API, leaving the row with no book.
func deleteBookWithHistory(t *testing.T, f authzBookFixture, hist *db.HistoryRepo) *models.HistoryEvent {
	t.Helper()
	ctx := context.Background()
	ev := &models.HistoryEvent{BookID: &f.b1.ID, EventType: models.HistoryEventGrabbed, SourceTitle: "Alice.Secret.Release.EPUB", Data: `{"guid":"alice-guid-1","indexer":"AliceIndexer"}`}
	if err := hist.Create(ctx, ev); err != nil {
		t.Fatal(err)
	}
	bh := NewBookHandler(f.books, nil, hist, nil)
	rec := httptest.NewRecorder()
	bh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/book/x", f.b1.ID, withAuthCtx(ctx, f.u1, "user")))
	if rec.Code/100 != 2 {
		t.Fatalf("alice delete own book: %d %s", rec.Code, rec.Body.String())
	}
	return ev
}

func historyListBody(t *testing.T, hh *HistoryHandler, ctx context.Context) string {
	t.Helper()
	rec := httptest.NewRecorder()
	hh.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("history list: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// A deleted book's history stays with the book's owner under tenancy: another
// user can neither list it, delete it nor blocklist its release, and the owner
// still can.
func TestHistory_DeletedBookHistoryStaysWithOwner(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)
	ctx := context.Background()
	hist := db.NewHistoryRepo(f.database)
	bl := db.NewBlocklistRepo(f.database)
	hh := NewHistoryHandler(hist, bl, f.books)
	bob := withAuthCtx(ctx, f.u2, "user")
	alice := withAuthCtx(ctx, f.u1, "user")

	ev := deleteBookWithHistory(t, f, hist)

	if body := historyListBody(t, hh, bob); strings.Contains(body, "Alice.Secret.Release") {
		t.Errorf("bob's history shows alice's release after she deleted the book: %s", body)
	}
	if body := historyListBody(t, hh, alice); !strings.Contains(body, "Alice.Secret.Release") {
		t.Errorf("alice lost her own history when she deleted the book: %s", body)
	}

	rec := httptest.NewRecorder()
	hh.Blocklist(rec, newRequestForID(http.MethodPost, "/api/v1/history/x/blocklist", ev.ID, bob))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob blocklisting alice's event: %d %s, want 404", rec.Code, rec.Body.String())
	}
	if blocked, _ := bl.IsBlocked(ctx, "alice-guid-1"); blocked {
		t.Error("bob's blocklist of alice's event created an entry")
	}
	rec = httptest.NewRecorder()
	hh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/history/x", ev.ID, bob))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob deleting alice's event: %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	hh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/history/x", ev.ID, alice))
	if rec.Code != http.StatusNoContent {
		t.Errorf("alice deleting her own event: %d, want 204", rec.Code)
	}
}

// A history row with neither an owner nor a book (recorded before rows carried
// an owner) is admin only under tenancy: hidden from a user's list and not
// theirs to delete or blocklist. An admin still sees and manages it.
func TestHistory_UnownedBooklessRowIsAdminOnly(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)
	ctx := context.Background()
	hist := db.NewHistoryRepo(f.database)
	bl := db.NewBlocklistRepo(f.database)
	hh := NewHistoryHandler(hist, bl, f.books)
	bob := withAuthCtx(ctx, f.u2, "user")
	admin := withAuthCtx(ctx, 99, "admin")

	ev := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "Legacy.Bookless.Grab", Data: `{"guid":"legacy-guid"}`}
	if err := hist.Create(ctx, ev); err != nil {
		t.Fatal(err)
	}

	if body := historyListBody(t, hh, bob); strings.Contains(body, "Legacy.Bookless.Grab") {
		t.Errorf("bob sees an ownerless, bookless row: %s", body)
	}
	if body := historyListBody(t, hh, admin); !strings.Contains(body, "Legacy.Bookless.Grab") {
		t.Errorf("admin does not see the ownerless row: %s", body)
	}
	rec := httptest.NewRecorder()
	hh.Blocklist(rec, newRequestForID(http.MethodPost, "/api/v1/history/x/blocklist", ev.ID, bob))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob blocklisting an ownerless row: %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	hh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/history/x", ev.ID, bob))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob deleting an ownerless row: %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	hh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/history/x", ev.ID, admin))
	if rec.Code != http.StatusNoContent {
		t.Errorf("admin deleting an ownerless row: %d, want 204", rec.Code)
	}
}

// With tenancy off nothing is scoped: every user sees a deleted book's history
// and may delete it, as before.
func TestHistory_DeletedBookHistoryTenancyOffUnchanged(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserBooks(t)
	ctx := context.Background()
	hist := db.NewHistoryRepo(f.database)
	hh := NewHistoryHandler(hist, db.NewBlocklistRepo(f.database), f.books)
	bob := withAuthCtx(ctx, f.u2, "user")

	ev := deleteBookWithHistory(t, f, hist)
	if body := historyListBody(t, hh, bob); !strings.Contains(body, "Alice.Secret.Release") {
		t.Errorf("tenancy off: bob should see every row: %s", body)
	}
	rec := httptest.NewRecorder()
	hh.Delete(rec, newRequestForID(http.MethodDelete, "/api/v1/history/x", ev.ID, bob))
	if rec.Code != http.StatusNoContent {
		t.Errorf("tenancy off: bob delete: %d, want 204", rec.Code)
	}
}

// A grab with no book records the grabbing user as the row's owner, so their
// free text grabs stay in their own history and nobody else's.
func TestQueueGrabHistoryOwnedByGrabber(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)
	ctx := context.Background()
	hist := db.NewHistoryRepo(f.database)
	h := &QueueHandler{history: hist}
	h.recordHistory(ctx, models.HistoryEventGrabbed, "Bob.Free.Text", nil, f.u2, map[string]any{"guid": "bob-guid"})

	hh := NewHistoryHandler(hist, db.NewBlocklistRepo(f.database), f.books)
	if body := historyListBody(t, hh, withAuthCtx(ctx, f.u2, "user")); !strings.Contains(body, "Bob.Free.Text") {
		t.Errorf("bob does not see his own bookless grab: %s", body)
	}
	if body := historyListBody(t, hh, withAuthCtx(ctx, f.u1, "user")); strings.Contains(body, "Bob.Free.Text") {
		t.Errorf("alice sees bob's bookless grab: %s", body)
	}
}

// A file change on a shared (unowned) book records the account that made it as
// the history row's owner, so it shows in their history and not in everyone's.
// Under tenancy only an admin may change a shared book, so that is an admin.
func TestBookDeleteFile_SharedBookHistoryOwnedByCaller(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)
	ctx := context.Background()
	hist := db.NewHistoryRepo(f.database)
	shared := &models.Book{ForeignID: "B-SHARED", AuthorID: f.a2.ID, Title: "Shared Book", SortTitle: "shared book", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec("UPDATE books SET owner_user_id = NULL WHERE id = ?", shared.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Shared.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, shared.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	h := NewBookHandler(f.books, nil, hist, nil)
	deleteAs := func(userID int64, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/book/x/file?path="+url.QueryEscape(path), nil).
			WithContext(withAuthCtx(ctx, userID, role))
		rec := httptest.NewRecorder()
		h.DeleteFile(rec, withURLParam(req, "id", strconv.FormatInt(shared.ID, 10)))
		return rec
	}
	// A shared book is read only for a user who is not an admin, so bob's
	// attempt is refused and records nothing.
	if rec := deleteAs(f.u2, "user"); rec.Code != http.StatusNotFound {
		t.Fatalf("bob deregisters a file of the shared book: %d %s, want 404", rec.Code, rec.Body.String())
	}
	if evs, err := hist.ListByType(ctx, models.HistoryEventBookFileDeleted); err != nil || len(evs) != 0 {
		t.Fatalf("history after refused delete = %v, %v; want none", evs, err)
	}
	// An admin may change it, and the entry belongs to the admin who did.
	if rec := deleteAs(f.u1, "admin"); rec.Code != http.StatusOK {
		t.Fatalf("admin deregisters a file of the shared book: %d %s", rec.Code, rec.Body.String())
	}
	evs, err := hist.ListByType(ctx, models.HistoryEventBookFileDeleted)
	if err != nil || len(evs) != 1 {
		t.Fatalf("history = %v, %v; want one row", evs, err)
	}
	if evs[0].OwnerUserID != f.u1 {
		t.Errorf("history owner = %d, want the admin (%d)", evs[0].OwnerUserID, f.u1)
	}
}
