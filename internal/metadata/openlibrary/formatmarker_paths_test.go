package openlibrary

import (
	"context"
	"testing"
)

// TestSubjectBooksAndISBNFallback_HTTP_StripFormatMarker covers the two
// remaining title entry points: genre popular recommendations and an ISBN
// whose edition record links no work.
func TestSubjectBooksAndISBNFallback_HTTP_StripFormatMarker(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/science_fiction.json": jsonStr(map[string]any{
			"name":  "science_fiction",
			"works": []map[string]any{{"key": "/works/OL1W", "title": "Acts of War [Unabridged Audiobook]"}},
		}),
		"/isbn/9780593197783.json": jsonStr(map[string]any{
			"key": "/books/OL2M", "title": "Iron Gold (Audio CD)",
		}),
	})
	cands, err := c.GetSubjectBooks(context.Background(), "science_fiction", 5)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if len(cands) != 1 || cands[0].Title != "Acts of War" {
		t.Errorf("GetSubjectBooks = %+v, want one %q", cands, "Acts of War")
	}
	book, err := c.GetBookByISBN(context.Background(), "9780593197783")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil || book.Title != "Iron Gold" || book.SortTitle != "Iron Gold" || book.MediaType != "" {
		t.Errorf("GetBookByISBN = %+v, want %q with no media type", book, "Iron Gold")
	}
}
