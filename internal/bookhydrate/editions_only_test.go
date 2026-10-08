package bookhydrate

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestEditionsOnlyStoresEditionsAndLeavesTheBook is the hydrator the Calibre
// handoff uses for a list synced book with no editions (#1853). It must store
// the fetched editions, and must not promote an ASIN onto the book or write
// the book row, which HydrateHardcoverEditions does for a newly added book.
func TestEditionsOnlyStoresEditionsAndLeavesTheBook(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:list-synced", "hardcover", models.MediaTypeBoth)
	isbn := "9780441172719"
	audioASIN := "B0AUDIO000"
	hydrate := EditionsOnly(editions, func(_ context.Context, foreignID string) ([]models.Edition, error) {
		if foreignID != "hc:list-synced" {
			t.Errorf("fetched %q, want the book's own Hardcover id", foreignID)
		}
		return []models.Edition{
			{ForeignID: "hc:ebook", Title: "Ebook", ISBN13: &isbn, Publisher: "Ace", Format: "EPUB", IsEbook: true},
			{ForeignID: "hc:audio", Title: "Audio", ASIN: &audioASIN, Format: "Audiobook"},
		}, nil
	})

	if err := hydrate(ctx, book); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	list, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected the two fetched editions stored, got %d", len(list))
	}
	if book.ASIN != "" {
		t.Errorf("the caller's book must not change, ASIN became %q", book.ASIN)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ASIN != "" {
		t.Errorf("the books row must not change, ASIN became %q", stored.ASIN)
	}
}

// TestEditionsOnlySkipsNonHardcoverBooks: a book with no Hardcover identity
// is not fetched at all.
func TestEditionsOnlySkipsNonHardcoverBooks(t *testing.T) {
	_, editions, book, ctx := newHydrateBook(t, "OL123W", "openlibrary", models.MediaTypeEbook)
	called := false
	hydrate := EditionsOnly(editions, func(context.Context, string) ([]models.Edition, error) {
		called = true
		return nil, nil
	})
	if err := hydrate(ctx, book); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("a book with no Hardcover identity must not be fetched")
	}
}
