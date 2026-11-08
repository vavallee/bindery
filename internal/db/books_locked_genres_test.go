package db

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_ReadKeepsLockedAndHumanGenres guards the two cases the read
// time filter must not touch. A locked genre list is the user's, whatever it
// holds, and is never hidden (which the next save would turn into a delete).
// An unlocked list from an Audiobookshelf import, which does not lock genres,
// keeps values a person wrote even when they look tag like ("Time: Future",
// "E=mc2"), because only OpenLibrary's exact "key:value" shape is filtered.
func TestBookRepo_ReadKeepsLockedAndHumanGenres(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-TAG-L", Name: "Pierce Brown", SortName: "Brown, Pierce"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	create := func(foreignID, genres, locked string) int64 {
		t.Helper()
		b := &models.Book{ForeignID: foreignID, AuthorID: author.ID, Title: foreignID, Status: models.BookStatusWanted}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("UPDATE books SET genres = ?, locked_fields = ? WHERE id = ?", genres, locked, b.ID); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	lockedID := create("OL-TAG-L1", `["nyt:hardcover-fiction=2015-01-25","Time: Future","E=mc2"]`, `["genres"]`)
	absID := create("OL-TAG-L2", `["Time: Future","E=mc2","Series: Discworld","nyt:hardcover-fiction=2015-01-25"]`, `[]`)

	got, err := books.GetByID(ctx, lockedID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nyt:hardcover-fiction=2015-01-25", "Time: Future", "E=mc2"}; !slices.Equal(got.Genres, want) {
		t.Errorf("locked genres = %q, want %q untouched", got.Genres, want)
	}
	got, err = books.GetByID(ctx, absID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Time: Future", "E=mc2", "Series: Discworld"}; !slices.Equal(got.Genres, want) {
		t.Errorf("unlocked imported genres = %q, want %q", got.Genres, want)
	}
}
