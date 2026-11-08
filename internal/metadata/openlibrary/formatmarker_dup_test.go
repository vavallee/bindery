package openlibrary

import (
	"context"
	"testing"
)

// TestOLDisplayTitle_StripsDuplicateNoteBeforeFormatMarker covers a title that
// carries both notes, the duplicate note outermost.
func TestOLDisplayTitle_StripsDuplicateNoteBeforeFormatMarker(t *testing.T) {
	for in, want := range map[string]string{
		"X [Audiobook] (Duplicate of OL1W)":                      "X",
		"Acts of War [Unabridged Audiobook] (Duplicate of OL9W)": "Acts of War",
		"X (Duplicate of OL1W)":                                  "X",
		"X [Audiobook]":                                          "X",
	} {
		if got := olDisplayTitle(in); got != want {
			t.Errorf("olDisplayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetBookAndSearch_HTTP_StripBothNotes(t *testing.T) {
	const raw = "Acts of War [Unabridged Audiobook] (Duplicate of OL9W)"
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json": jsonStr(map[string]any{"key": "/works/OL1W", "title": raw}),
		"/search.json":     jsonStr(map[string]any{"docs": []map[string]any{{"key": "/works/OL1W", "title": raw}}}),
	})
	book, err := c.GetBook(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Title != "Acts of War" || book.SortTitle != "Acts of War" {
		t.Errorf("GetBook title = %q, sort title = %q, want %q", book.Title, book.SortTitle, "Acts of War")
	}
	books, err := c.SearchBooks(context.Background(), "acts of war")
	if err != nil || len(books) != 1 || books[0].Title != "Acts of War" {
		t.Errorf("SearchBooks = %+v, %v, want one %q", books, err, "Acts of War")
	}
}
