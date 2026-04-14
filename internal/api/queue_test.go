package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func queueFixture(t *testing.T) (*QueueHandler, *sql.DB, *db.DownloadRepo, *db.DownloadClientRepo, *db.BookRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	books := db.NewBookRepo(database)
	history := db.NewHistoryRepo(database)
	return NewQueueHandler(downloads, clients, books, history), database, downloads, clients, books, context.Background()
}

// TestQueueGrab_RequiresGUIDAndURL — input validation is the first gate;
// without it, we'd create an orphaned download row and then 502 on SAB.
func TestQueueGrab_RequiresGUIDAndURL(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	for _, body := range []string{
		`{}`,
		`{"guid":"abc"}`,                    // missing nzbUrl
		`{"nzbUrl":"http://example/x.nzb"}`, // missing guid
	} {
		rec := httptest.NewRecorder()
		h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

// TestQueueGrab_RejectsBadJSON keeps the handler from panicking on a
// malformed client payload.
func TestQueueGrab_RejectsBadJSON(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestQueueGrab_NoDownloadClient is the 400 path when the operator hasn't
// enabled a download client yet. Without this guard the handler would NPE
// on the nil client pointer.
func TestQueueGrab_NoDownloadClient(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	body := bytes.NewBufferString(`{"guid":"abc","nzbUrl":"http://example/x.nzb","title":"t"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no client configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestQueueGrab_DuplicateGUID — the second Grab with the same guid must 409
// so a double-click doesn't double-spend indexer hit counts.
func TestQueueGrab_DuplicateGUID(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	// Pre-seed a download to simulate the prior grab.
	if err := downloads.Create(ctx, &models.Download{
		GUID: "dup-guid", Title: "T", Status: models.DownloadStatusDownloading, Protocol: "usenet",
	}); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"guid":"dup-guid","nzbUrl":"http://example/x.nzb","title":"T"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
}

// TestQueueDelete_NotFound — chi URL param resolution is handled by the
// router; here we verify missing id → 404 rather than a silent 204.
func TestQueueDelete_NotFound(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/queue/42", nil), "id", "42")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestQueueDelete_FlipsBookToWanted is the regression guard: deleting a
// queued download that owns a book must reset the book to `wanted` so the
// Wanted page re-surfaces it. Without this the book stays stuck in
// `downloading` forever.
func TestQueueDelete_FlipsBookToWanted(t *testing.T) {
	h, database, downloads, _, books, ctx := queueFixture(t)
	// Seed an author + book so the FK is satisfied.
	a := &models.Author{ForeignID: "OL1", Name: "X", SortName: "X", MetadataProvider: "openlibrary", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "B1", AuthorID: a.ID, Title: "T", SortTitle: "t",
		Status: models.BookStatusDownloading, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	d := &models.Download{
		GUID: "g", BookID: &b.ID, Title: "T",
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, d); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/queue/"+strconv.FormatInt(d.ID, 10), nil), "id", strconv.FormatInt(d.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, b.ID)
	if got.Status != models.BookStatusWanted {
		t.Errorf("book status should flip to wanted, got %q", got.Status)
	}
}
