package openlibrary

import (
	"context"
	"testing"
)

func TestTrimUnmatchedQuote(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"stray leading quote", `"Darrow would have lived in peace, but his enemies brought him war.`, "Darrow would have lived in peace, but his enemies brought him war."},
		{"stray trailing quote", `Ash and blood and bone."`, "Ash and blood and bone."},
		{"leading quote with other quotes inside is a guess", `"He is called "the Reaper" by friends and foes.`, `"He is called "the Reaper" by friends and foes.`},
		{"dialogue with an open last quote stays", `"Hello," she said. Then "bye`, `"Hello," she said. Then "bye`},
		{"multi paragraph curly openers stay", "“First paragraph.\n\n“Second paragraph.”", "“First paragraph.\n\n“Second paragraph.”"},
		{"leading curly opener with a later paragraph opener stays", "“First paragraph.\n\n“Second paragraph.", "“First paragraph.\n\n“Second paragraph."},
		{"straight stray quote beside balanced curly quotes", "\"They call him “the Reaper”.", "They call him “the Reaper”."},
		{"only a curly quote", "“", "“"},
		{"stray leading curly quote", "“Break the chains.", "Break the chains."},
		{"stray trailing curly quote", "Break the chains.”", "Break the chains."},
		{"whole description quoted stays", `"Break the chains." A rallying cry.`, `"Break the chains." A rallying cry.`},
		{"fully wrapped stays", `"Break the chains."`, `"Break the chains."`},
		{"quote inside stays", `They call him "the Reaper".`, `They call him "the Reaper".`},
		{"balanced curly stays", "“Break the chains.” A rallying cry.", "“Break the chains.” A rallying cry."},
		{"odd count with quotes at both ends is ambiguous", `"One" two "three"`, `"One" two "three"`},
		{"no quotes", "Red Rising is a dystopian novel.", "Red Rising is a dystopian novel."},
		{"only a quote", `"`, `"`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimUnmatchedQuote(tt.in); got != tt.want {
				t.Errorf("trimUnmatchedQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestGetBook_HTTP_CleansDuplicateMarkerAndStrayQuote covers the two pieces of
// OpenLibrary record junk seen on bindery-dev: a title carrying the merge
// queue's "(Duplicate of OL...)" note, and a description opening with a quote
// that never closes.
func TestGetBook_HTTP_CleansDuplicateMarkerAndStrayQuote(t *testing.T) {
	workResp := map[string]any{
		"key":         "/works/OL31211W",
		"title":       "Light Bringer (Duplicate of OL31211999W)",
		"description": map[string]any{"type": "/type/text", "value": `"Golden Son continues the saga.`},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL31211W.json": jsonStr(workResp),
	})
	book, err := c.GetBook(context.Background(), "OL31211W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Title != "Light Bringer" || book.SortTitle != "Light Bringer" {
		t.Errorf("title = %q, sort title = %q, want both %q", book.Title, book.SortTitle, "Light Bringer")
	}
	if book.Description != "Golden Son continues the saga." {
		t.Errorf("description = %q, want the stray leading quote gone", book.Description)
	}
}

// TestGetAuthorWorks_HTTP_CleansDuplicateMarker checks the author sync path,
// which is where most library rows come from, for both the works endpoint and
// the search index entries it merges in.
func TestGetAuthorWorks_HTTP_CleansDuplicateMarker(t *testing.T) {
	worksResp := authorWorksResponse{
		Size: 1,
		Entries: []authorWorkEntry{{
			Key:         "/works/OL1W",
			Title:       "Light Bringer (Duplicate of OL31211W)",
			Description: `"Darrow returns.`,
		}},
	}
	searchResp := searchRespForAuthor{Docs: []searchDocForAuthor{
		{Key: "/works/OL1W", Title: "Light Bringer (Duplicate of OL31211W)"},
		{Key: "/works/OL2W", Title: "Golden Son (Duplicate of OL26431102W)"},
	}}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL9A/works.json": jsonStr(worksResp),
		"/search.json":             jsonStr(searchResp),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL9A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	want := map[string]string{"OL1W": "Light Bringer", "OL2W": "Golden Son"}
	if len(books) != len(want) {
		t.Fatalf("got %d books, want %d", len(books), len(want))
	}
	for _, b := range books {
		if b.Title != want[b.ForeignID] || b.SortTitle != want[b.ForeignID] {
			t.Errorf("%s: title = %q, sort title = %q, want %q", b.ForeignID, b.Title, b.SortTitle, want[b.ForeignID])
		}
		if b.ForeignID == "OL1W" && b.Description != "Darrow returns." {
			t.Errorf("OL1W description = %q, want the stray leading quote gone", b.Description)
		}
	}
}

// TestSearchBooksAndISBNFallback_HTTP_CleanDuplicateMarker covers the two other
// title entry points: the add dialog's search and an ISBN whose edition record
// links no work.
func TestSearchBooksAndISBNFallback_HTTP_CleanDuplicateMarker(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json": jsonStr(map[string]any{"docs": []map[string]any{
			{"key": "/works/OL1W", "title": "Light Bringer (Duplicate of OL31211W)"},
		}}),
		"/isbn/9780425285961.json": jsonStr(map[string]any{
			"key": "/books/OL2M", "title": "Golden Son (Duplicate of OL26431102M)",
		}),
	})
	books, err := c.SearchBooks(context.Background(), "light bringer")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Light Bringer" || books[0].SortTitle != "Light Bringer" {
		t.Errorf("SearchBooks = %+v, want one %q", books, "Light Bringer")
	}
	book, err := c.GetBookByISBN(context.Background(), "9780425285961")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil || book.Title != "Golden Son" || book.SortTitle != "Golden Son" {
		t.Errorf("GetBookByISBN = %+v, want %q", book, "Golden Son")
	}
}
