package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestListByASINsVisibleTo pins the wishlist sync's batch lookup: keyed by
// the normalised ASIN, scoped like GetByForeignIDVisibleTo, and the oldest
// row winning when two visible books share an ASIN.
func TestListByASINsVisibleTo(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	alice := seedUser(t, ctx, database, "asin-alice")
	bob := seedUser(t, ctx, database, "asin-bob")
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-ASIN-A")

	mk := func(fid, asin string, owner int64) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: fid, AuthorID: author.ID, Title: fid, SortTitle: fid, Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner,
			ASIN: asin, MediaType: models.MediaTypeAudiobook,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := mk("OL-ASIN-1", "B000000001", alice)
	mk("OL-ASIN-1b", "B000000001", 0) // same ASIN, created later
	mk("OL-ASIN-2", "B000000002", bob)
	shared := mk("OL-ASIN-3", "B000000003", 0)

	got, err := books.ListByASINsVisibleTo(ctx, []string{"b000000001", "B000000002", "B000000003", "B000000009", ""}, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("alice sees %d ASINs, want 2: %v", len(got), got)
	}
	if got["B000000001"].ID != first.ID {
		t.Errorf("duplicate ASIN: got book %d, want the oldest %d", got["B000000001"].ID, first.ID)
	}
	if got["B000000003"].ID != shared.ID {
		t.Errorf("an unowned book must be visible to alice")
	}
	if _, ok := got["B000000002"]; ok {
		t.Errorf("bob's book leaked to alice")
	}

	all, err := books.ListByASINsVisibleTo(ctx, []string{"B000000001", "B000000002", "B000000003"}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("unscoped = %d, %v; want 3", len(all), err)
	}
	if none, err := books.ListByASINsVisibleTo(ctx, nil, 0); err != nil || len(none) != 0 {
		t.Fatalf("empty input = %v, %v", none, err)
	}
}
