package openlibrary

import (
	"context"
	"testing"
)

func TestStripFormatMarker(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unabridged audiobook in brackets", "Acts of War [Unabridged Audiobook]", "Acts of War"},
		{"audiobook in brackets", "Acts of War [Audiobook]", "Acts of War"},
		{"audio cd in parens", "The Dutch House (Audio CD)", "The Dutch House"},
		{"unabridged audiobook in parens", "Acts of War (Unabridged Audiobook)", "Acts of War"},
		{"audiobook comma unabridged", "Acts of War [Audiobook, Unabridged]", "Acts of War"},
		{"lower case", "acts of war [unabridged audiobook]", "acts of war"},
		{"bare tokens after a colon", "Acts of War: Unabridged Audiobook", "Acts of War"},
		{"bare tokens after a hyphen", "Acts of War - Unabridged Audiobook", "Acts of War"},
		{"bare tokens after a comma", "Acts of War, Audiobook, Unabridged", "Acts of War"},
		{"bare tokens with no separator stay", "Acts of War Unabridged Audiobook", "Acts of War Unabridged Audiobook"},
		{"title ending in the words stays", "How to Record an Unabridged Audiobook", "How to Record an Unabridged Audiobook"},
		{"stacked unabridged and audiobook", "Acts of War [Unabridged] [Audiobook]", "Acts of War"},
		{"unabridged alone is not a format marker", "Acts of War (Unabridged)", "Acts of War (Unabridged)"},
		{"cd alone is not a format marker", "Learning Basics (CD)", "Learning Basics (CD)"},
		{"series parenthetical kept", "Red Rising (Red Rising Saga #1)", "Red Rising (Red Rising Saga #1)"},
		{"audiobook word inside the title", "The Audiobook Murders", "The Audiobook Murders"},
		{"bare audiobook alone kept", "My Life in Audiobook", "My Life in Audiobook"},
		{"marker only keeps the title", "[Audiobook]", "[Audiobook]"},
		{"plain", "Acts of War", "Acts of War"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripFormatMarker(tt.in); got != tt.want {
				t.Errorf("stripFormatMarker(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFormatMarkerLeavesMediaTypeAlone pins the scope of the fix. The marker
// comes off the display title, but OpenLibrary still leaves MediaType empty
// so the add flow applies the user's default format exactly as before: an
// OpenLibrary work title is not evidence enough to change what Bindery
// searches for, widen an existing ebook to both, or trip the strict media
// type filter.
func TestFormatMarkerLeavesMediaTypeAlone(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json": jsonStr(map[string]any{"key": "/works/OL1W", "title": "Acts of War [Unabridged Audiobook]"}),
		"/search.json": jsonStr(map[string]any{"docs": []map[string]any{
			{"key": "/works/OL1W", "title": "Acts of War [Unabridged Audiobook]"},
		}}),
	})
	book, err := c.GetBook(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Title != "Acts of War" || book.SortTitle != "Acts of War" {
		t.Errorf("GetBook title = %q, sort title = %q, want both %q", book.Title, book.SortTitle, "Acts of War")
	}
	if book.MediaType != "" {
		t.Errorf("GetBook media type = %q, want it left empty for the caller's default", book.MediaType)
	}

	books, err := c.SearchBooks(context.Background(), "acts of war")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Acts of War" || books[0].SortTitle != "Acts of War" || books[0].MediaType != "" {
		t.Errorf("SearchBooks = %+v, want one %q with no media type", books, "Acts of War")
	}
}

// TestGetAuthorWorks_HTTP_StripsFormatMarker covers both sources author sync
// merges. The noise filter still sees the raw title first, so a work titled
// "(Audio CD)" is dropped as before rather than kept under a cleaned name.
func TestGetAuthorWorks_HTTP_StripsFormatMarker(t *testing.T) {
	worksResp := authorWorksResponse{
		Size: 2,
		Entries: []authorWorkEntry{
			{Key: "/works/OL1W", Title: "Acts of War [Unabridged Audiobook]"},
			{Key: "/works/OL2W", Title: "Acts of War (Audio CD)"},
		},
	}
	searchResp := searchRespForAuthor{Docs: []searchDocForAuthor{
		{Key: "/works/OL1W", Title: "Acts of War [Unabridged Audiobook]"},
		{Key: "/works/OL3W", Title: "Iron Gold [Audiobook]"},
		{Key: "/works/OL4W", Title: "Iron Gold (Audio CD)"},
	}}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL9A/works.json": jsonStr(worksResp),
		"/search.json":             jsonStr(searchResp),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL9A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	want := map[string]string{"OL1W": "Acts of War", "OL3W": "Iron Gold"}
	if len(books) != len(want) {
		t.Fatalf("got %d books %+v, want %d (the Audio CD works dropped as noise)", len(books), books, len(want))
	}
	for _, b := range books {
		if b.Title != want[b.ForeignID] || b.SortTitle != want[b.ForeignID] {
			t.Errorf("%s: title = %q, sort title = %q, want %q", b.ForeignID, b.Title, b.SortTitle, want[b.ForeignID])
		}
		if b.MediaType != "" {
			t.Errorf("%s: media type = %q, want empty", b.ForeignID, b.MediaType)
		}
	}
}
