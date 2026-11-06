package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestDropPlacements(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	author := &models.Author{ForeignID: "OLA-DROP", Name: "Jane Doe", SortName: "Doe, Jane", MetadataProvider: "openlibrary"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	first := secondBook(t, database, author.ID, "First")
	second := secondBook(t, database, author.ID, "Second")
	books := NewBookRepo(database)
	const path = "/drop/Shared Title - Jane Doe.epub"

	assertHolder := func(want int64) {
		t.Helper()
		got, err := books.DropPlacementBook(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("holder = %d, want %d", got, want)
		}
	}

	assertHolder(0)
	if err := books.RecordDropPlacement(ctx, path, first.ID); err != nil {
		t.Fatal(err)
	}
	assertHolder(first.ID)

	// The latest placement wins: the file there now is that book's.
	if err := books.RecordDropPlacement(ctx, path, second.ID); err != nil {
		t.Fatal(err)
	}
	assertHolder(second.ID)

	// A deleted book holds nothing, so its file is not replaced as its own.
	if err := books.Delete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	assertHolder(0)
}
