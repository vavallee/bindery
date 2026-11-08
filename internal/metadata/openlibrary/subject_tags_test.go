package openlibrary

import (
	"context"
	"slices"
	"testing"
)

// junkSubjects is the shape seen on Red Rising's OpenLibrary record: real
// subjects mixed with machine tags that showed up as tag chips on the book
// page.
var junkSubjects = []string{
	"seriesRed Rising Trilogy",
	"Science fiction",
	"nyt:hardcover-fiction=2015-01-25",
	"place:Mars",
	"person:Darrow",
	"time:Future",
	"collectionID:scifi",
	"Dystopias",
}

var wantGenres = []string{"Science fiction", "Dystopias"}

func TestGetBook_HTTP_DropsMachineTagSubjects(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json": jsonStr(map[string]any{"key": "/works/OL1W", "title": "Red Rising", "subjects": junkSubjects}),
	})
	book, err := c.GetBook(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if !slices.Equal(book.Genres, wantGenres) {
		t.Errorf("genres = %q, want %q", book.Genres, wantGenres)
	}
}

// TestGetBook_HTTP_GenreCapCountsRealSubjects checks the tags are dropped
// before the ten genre cap, so a record that leads with machine tags still
// yields its real subjects.
func TestGetBook_HTTP_GenreCapCountsRealSubjects(t *testing.T) {
	subjects := []string{
		"nyt:a=1", "nyt:b=2", "nyt:c=3", "place:Mars", "person:Darrow", "time:Future",
		"G1", "G2", "G3", "G4", "G5", "G6", "G7", "G8", "G9", "G10", "G11",
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json": jsonStr(map[string]any{"key": "/works/OL1W", "title": "Red Rising", "subjects": subjects}),
	})
	book, err := c.GetBook(context.Background(), "OL1W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	want := []string{"G1", "G2", "G3", "G4", "G5", "G6", "G7", "G8", "G9", "G10"}
	if !slices.Equal(book.Genres, want) {
		t.Errorf("genres = %q, want %q", book.Genres, want)
	}
}

func TestGetAuthorWorks_HTTP_DropsMachineTagSubjects(t *testing.T) {
	worksResp := map[string]any{
		"size":    1,
		"entries": []map[string]any{{"key": "/works/OL1W", "title": "Red Rising", "subjects": junkSubjects}},
	}
	searchResp := map[string]any{"docs": []map[string]any{
		{"key": "/works/OL1W", "title": "Red Rising", "subject": junkSubjects},
		{"key": "/works/OL2W", "title": "Golden Son", "subject": junkSubjects},
	}}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL9A/works.json": jsonStr(worksResp),
		"/search.json":             jsonStr(searchResp),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL9A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("got %d books, want 2", len(books))
	}
	for _, b := range books {
		if !slices.Equal(b.Genres, wantGenres) {
			t.Errorf("%s genres = %q, want %q", b.ForeignID, b.Genres, wantGenres)
		}
	}
}

func TestSearchBooks_HTTP_DropsMachineTagSubjects(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json": jsonStr(map[string]any{"docs": []map[string]any{
			{"key": "/works/OL1W", "title": "Red Rising", "subject": junkSubjects},
		}}),
	})
	books, err := c.SearchBooks(context.Background(), "red rising")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 || !slices.Equal(books[0].Genres, wantGenres) {
		t.Fatalf("books = %+v, want one with genres %q", books, wantGenres)
	}
}
