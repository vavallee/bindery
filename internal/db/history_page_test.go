package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestHistoryListPage_BookIDAndEventTypeAreAnded guards the fix for ListPage
// treating BookID and EventType as mutually exclusive: a request that sets both
// must match both. Before the fix, supplying a book_id silently dropped the
// event_type filter.
func TestHistoryListPage_BookIDAndEventTypeAreAnded(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	hist := NewHistoryRepo(database)

	a := &models.Author{ForeignID: "ol:A", Name: "A", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	b1 := &models.Book{ForeignID: "ol:B1", Title: "B1", AuthorID: a.ID, MetadataProvider: "openlibrary"}
	b2 := &models.Book{ForeignID: "ol:B2", Title: "B2", AuthorID: a.ID, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, b1); err != nil {
		t.Fatalf("seed book1: %v", err)
	}
	if err := books.Create(ctx, b2); err != nil {
		t.Fatalf("seed book2: %v", err)
	}

	mk := func(bookID int64, et string) {
		id := bookID
		if err := hist.Create(ctx, &models.HistoryEvent{BookID: &id, EventType: et, SourceTitle: et}); err != nil {
			t.Fatalf("create history (%d,%s): %v", bookID, et, err)
		}
	}
	mk(b1.ID, models.HistoryEventGrabbed)
	mk(b1.ID, models.HistoryEventImportFailed)
	mk(b2.ID, models.HistoryEventGrabbed)

	// Both filters: only book1's "grabbed" row qualifies.
	rows, total, err := hist.ListPage(ctx, HistoryListOpts{BookID: b1.ID, EventType: models.HistoryEventGrabbed})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected exactly 1 row for (book1, grabbed), got total=%d len=%d", total, len(rows))
	}
	if rows[0].EventType != models.HistoryEventGrabbed || rows[0].BookID == nil || *rows[0].BookID != b1.ID {
		t.Fatalf("unexpected row: %+v", rows[0])
	}

	// Sanity: BookID alone still returns both of book1's events.
	if _, totalBook, err := hist.ListPage(ctx, HistoryListOpts{BookID: b1.ID}); err != nil || totalBook != 2 {
		t.Fatalf("book-only filter: expected 2 rows, got total=%d err=%v", totalBook, err)
	}
}
