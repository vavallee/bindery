package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_GetByIDs verifies the batch loader returns each requested book
// keyed by id with its Author projection populated (the same LEFT JOIN GetByID
// uses), tolerates duplicate and missing ids, and short-circuits empty input.
func TestBookRepo_GetByIDs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	a := &models.Author{
		ForeignID: "OL-LEGUIN", Name: "Ursula K. Le Guin", SortName: "Le Guin, Ursula",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("create author: %v", err)
	}
	b1 := &models.Book{
		ForeignID: "OL-EARTHSEA", AuthorID: a.ID, Title: "A Wizard of Earthsea",
		SortTitle: "wizard of earthsea", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b1); err != nil {
		t.Fatalf("create b1: %v", err)
	}
	b2 := &models.Book{
		ForeignID: "OL-DISPOSSESSED", AuthorID: a.ID, Title: "The Dispossessed",
		SortTitle: "dispossessed", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b2); err != nil {
		t.Fatalf("create b2: %v", err)
	}

	// Duplicate id and a non-existent id are both tolerated: two real books
	// back, the missing id simply absent.
	got, err := books.GetByIDs(ctx, []int64{b1.ID, b2.ID, b1.ID, 999999})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 books, got %d", len(got))
	}
	if got[b1.ID] == nil || got[b1.ID].Title != "A Wizard of Earthsea" {
		t.Errorf("b1 missing or wrong: %+v", got[b1.ID])
	}
	if got[b2.ID] == nil || got[b2.ID].Title != "The Dispossessed" {
		t.Errorf("b2 missing or wrong: %+v", got[b2.ID])
	}
	// Author projection populated via the LEFT JOIN.
	if got[b1.ID].Author == nil || got[b1.ID].Author.ID != a.ID || got[b1.ID].Author.Name != "Ursula K. Le Guin" {
		t.Errorf("expected author populated on b1, got %+v", got[b1.ID].Author)
	}
	if _, ok := got[999999]; ok {
		t.Error("did not expect a row for the missing id")
	}

	// Empty input → empty map, no query, no error.
	empty, err := books.GetByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("GetByIDs(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected empty map for nil ids, got %d", len(empty))
	}
}
