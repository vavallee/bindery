package db

import (
	"context"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_ListPageFiltered_InLibraryIncludesPartiallyOwned pins #3132: a
// dual format book with one tracked file is status=wanted (the other format is
// missing) but is still in the library, so the In Library filter must list it
// while the Wanted filter keeps listing it too.
func TestBookRepo_ListPageFiltered_InLibraryIncludesPartiallyOwned(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	author := &models.Author{ForeignID: "OL-IL", Name: "Jon Ingold", SortName: "Ingold, Jon", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	seed := func(title, status, mediaType string, monitored bool, files ...string) {
		t.Helper()
		b := &models.Book{ForeignID: "OL-IL-" + title, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: status, MediaType: mediaType, Monitored: monitored}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
		for i, format := range files {
			if _, err := database.ExecContext(ctx,
				`INSERT INTO book_files (book_id, format, path) VALUES (?, ?, ?)`,
				b.ID, format, "/lib/"+title+"/"+format+string(rune('a'+i))); err != nil {
				t.Fatalf("file %s: %v", title, err)
			}
		}
	}
	seed("PartialEbook", models.BookStatusWanted, models.MediaTypeBoth, true, "ebook")
	seed("PartialTwoEbooks", models.BookStatusWanted, models.MediaTypeBoth, true, "ebook", "ebook")
	seed("PartialAudio", models.BookStatusWanted, models.MediaTypeBoth, true, "audiobook")
	seed("PartialUnmonitored", models.BookStatusWanted, models.MediaTypeBoth, false, "ebook")
	seed("Complete", models.BookStatusImported, models.MediaTypeBoth, true, "ebook", "audiobook")
	seed("EbookOnly", models.BookStatusImported, models.MediaTypeEbook, true, "ebook")
	seed("NothingOwned", models.BookStatusWanted, models.MediaTypeBoth, true)
	seed("SkippedWithFile", models.BookStatusSkipped, models.MediaTypeBoth, true, "ebook")

	titles := func(f BookListFilter) []string {
		t.Helper()
		got, total, err := bookRepo.ListPageFiltered(ctx, f, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != len(got) {
			t.Errorf("total=%d but %d rows for %+v", total, len(got), f)
		}
		var out []string
		for _, b := range got {
			out = append(out, b.Title)
		}
		sort.Strings(out)
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
		}
	}

	eq("in library", titles(BookListFilter{Status: models.BookStatusImported}),
		[]string{"Complete", "EbookOnly", "PartialAudio", "PartialEbook", "PartialTwoEbooks", "PartialUnmonitored"})
	// Wanted is unchanged: monitored and status wanted, owned or not.
	eq("wanted", titles(BookListFilter{Status: models.BookStatusWanted}),
		[]string{"NothingOwned", "PartialAudio", "PartialEbook", "PartialTwoEbooks"})
	// The format chips compose with the In Library filter.
	eq("in library audiobook", titles(BookListFilter{Status: models.BookStatusImported, MediaType: "audiobook"}),
		[]string{"Complete", "PartialAudio", "PartialEbook", "PartialTwoEbooks", "PartialUnmonitored"})
	// All is untouched and has no duplicates.
	if got := titles(BookListFilter{}); len(got) != 8 {
		t.Errorf("all = %v, want 8 books", got)
	}
}
