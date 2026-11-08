package db

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_ReadDropsMachineTagGenres covers rows stored before ingest
// dropped OpenLibrary's machine tag subjects ("seriesRed Rising Trilogy",
// "nyt:hardcover-fiction=2015-01-25"). They are filtered as rows are read,
// so the book page stops showing them without a rewrite of every row, and the
// next save of the book stores the clean list.
func TestBookRepo_ReadDropsMachineTagGenres(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-TAG-A", Name: "Pierce Brown", SortName: "Brown, Pierce"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-TAG-B", AuthorID: author.ID, Title: "Red Rising", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stored := `["seriesRed Rising Trilogy","Science fiction","nyt:hardcover-fiction=2015-01-25","place:Mars","Dystopias"]`
	if _, err := database.Exec("UPDATE books SET genres = ? WHERE id = ?", stored, book.ID); err != nil {
		t.Fatal(err)
	}

	want := []string{"Science fiction", "Dystopias"}
	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Genres, want) {
		t.Errorf("GetByID genres = %q, want %q", got.Genres, want)
	}
	list, err := books.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !slices.Equal(list[0].Genres, want) {
		t.Errorf("List genres = %+v, want %q", list, want)
	}
}
