package metadata

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// models.Book.LockedFields carries the contract these tests pin: every
// refresh, enrichment and merge path must ask before writing a lockable field
// (#1237, #1446, #2767). The enrichment paths asked nothing, so a hand edited
// description or genre list was replaced by whichever provider answered.
//
// Emptiness and ownership are separate questions throughout. EnrichAudiobook
// fills only when empty, enrichBook overwrites whenever the provider string is
// longer, and the genre branch replaces outright. A lock has to stop all three,
// which is why the guard is its own test rather than a tightening of the
// emptiness one (#2757).

// TestEnrichAudiobookHonoursDescriptionLock is the fill case: a description the
// user cleared by hand is locked and empty, and Audnex has a summary for the
// ASIN. Before the fix the summary was written over the clear.
func TestEnrichAudiobookHonoursDescriptionLock(t *testing.T) {
	tests := []struct {
		name        string
		description string
		locked      bool
		want        string
	}{
		{name: "locked empty survives", locked: true, want: ""},
		{name: "locked non-empty survives", description: "mine", locked: true, want: "mine"},
		{name: "unlocked empty is filled", want: "Audnex summary"},
		{name: "unlocked non-empty is kept", description: "theirs", want: "theirs"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubAudnexClient{books: map[string]*audnex.Book{
				"B0TESTLOCK": {ASIN: "B0TESTLOCK", Summary: "Audnex summary", Narrators: []audnex.Person{{Name: "Kate Reading"}}},
			}}
			agg := newTestAggregator(&mockProvider{name: "ol"}).WithAudnexClient(stub)

			book := &models.Book{
				Title:       "Locked Book",
				MediaType:   models.MediaTypeAudiobook,
				ASIN:        "B0TESTLOCK",
				Description: tc.description,
			}
			if tc.locked {
				book.LockField(models.BookFieldDescription)
			}
			if err := agg.EnrichAudiobook(context.Background(), book); err != nil {
				t.Fatalf("EnrichAudiobook: %v", err)
			}
			// Audnex really answered, so a pass cannot come from the lookup
			// being skipped.
			if stub.calls != 1 {
				t.Fatalf("audnex calls = %d, want 1", stub.calls)
			}
			if book.Narrator != "Kate Reading" {
				t.Fatalf("narrator = %q, want the enrichment to have run", book.Narrator)
			}
			if book.Description != tc.want {
				t.Errorf("description = %q, want %q", book.Description, tc.want)
			}
		})
	}
}

// TestEnrichBookHonoursDescriptionLock is the overwrite case: the provider
// description is longer, so the pre-fix rule replaced the user's shorter hand
// written one with no emptiness gate to hide behind.
func TestEnrichBookHonoursDescriptionLock(t *testing.T) {
	const providerDesc = "A considerably longer description supplied by the enricher."

	for _, locked := range []bool{true, false} {
		name := "unlocked is overwritten"
		want := providerDesc
		if locked {
			name = "locked survives"
			want = "mine"
		}
		t.Run(name, func(t *testing.T) {
			enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
				{Title: "Locked Book", Description: providerDesc},
			}}
			agg := newTestAggregator(&mockProvider{name: "ol"}, enricher)

			book := &models.Book{Title: "Locked Book", Description: "mine"}
			if locked {
				book.LockField(models.BookFieldDescription)
			}
			agg.enrichBook(context.Background(), book)
			if book.Description != want {
				t.Errorf("description = %q, want %q", book.Description, want)
			}
		})
	}
}

// TestEnrichBookHonoursGenreLock is the replace case. A locked genre list is
// usually one the user set in bulk through an author or series override
// (#1446); the Hardcover branch replaced it wholesale. The same lock was
// already honoured downstream in the author refresh merge, so the two disagreed.
func TestEnrichBookHonoursGenreLock(t *testing.T) {
	for _, locked := range []bool{true, false} {
		name := "unlocked is replaced"
		want := []string{"Fantasy"}
		if locked {
			name = "locked survives"
			want = []string{"Unsorted"}
		}
		t.Run(name, func(t *testing.T) {
			enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
				{Title: "Locked Book", Genres: []string{"Fantasy"}},
			}}
			agg := newTestAggregator(&mockProvider{name: "ol"}, enricher)

			book := &models.Book{Title: "Locked Book", Genres: []string{"Unsorted"}}
			if locked {
				book.LockField(models.BookFieldGenres)
			}
			agg.enrichBook(context.Background(), book)
			if !slices.Equal(book.Genres, want) {
				t.Errorf("genres = %v, want %v", book.Genres, want)
			}
		})
	}
}

// TestApplyEnrichmentSnapshotHonoursLocks covers the cache replay. The snapshot
// holds provider values, and applyEnrichmentSnapshot mirrors the live merge
// rules field for field, so a guard added only to the live path would leave the
// bug intact on every cache hit.
func TestApplyEnrichmentSnapshotHonoursLocks(t *testing.T) {
	snap := enrichmentSnapshot{
		description: "A considerably longer cached description.",
		genres:      []string{"Fantasy"},
	}

	t.Run("locked survives", func(t *testing.T) {
		book := &models.Book{Title: "Locked Book", Description: "mine", Genres: []string{"Unsorted"}}
		book.LockField(models.BookFieldDescription)
		book.LockField(models.BookFieldGenres)
		applyEnrichmentSnapshot(book, snap)
		if book.Description != "mine" {
			t.Errorf("description = %q, want %q", book.Description, "mine")
		}
		if !slices.Equal(book.Genres, []string{"Unsorted"}) {
			t.Errorf("genres = %v, want [Unsorted]", book.Genres)
		}
	})

	t.Run("unlocked is replaced", func(t *testing.T) {
		book := &models.Book{Title: "Locked Book", Description: "mine", Genres: []string{"Unsorted"}}
		applyEnrichmentSnapshot(book, snap)
		if book.Description != snap.description {
			t.Errorf("description = %q, want %q", book.Description, snap.description)
		}
		if !slices.Equal(book.Genres, snap.genres) {
			t.Errorf("genres = %v, want %v", book.Genres, snap.genres)
		}
	})
}

// The enrichment cache stores the post-merge book under a (provider,
// foreignID) key, which every book for that work shares, users included. Once
// enrichBook stops overwriting a locked field, the value left in the book is
// the user's own, and caching it would hand their hand written description to
// the next library that enriches the same work. A locked book must not seed
// the cache at all.
func TestEnrichBookDoesNotCacheFromALockedBook(t *testing.T) {
	newAgg := func() *Aggregator {
		enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
			{Title: "Locked Book", Description: "A considerably longer description supplied by the enricher.", Genres: []string{"Fantasy"}},
		}}
		return newTestAggregator(&mockProvider{name: "ol"}, enricher)
	}

	agg := newAgg()
	locked := &models.Book{Title: "Locked Book", MetadataProvider: "hardcover", ForeignID: "hc:1", Description: "mine"}
	locked.LockField(models.BookFieldDescription)
	agg.enrichBook(context.Background(), locked)

	// Same work, different library, nothing locked. It must get the provider's
	// description, not the first book's.
	other := &models.Book{Title: "Locked Book", MetadataProvider: "hardcover", ForeignID: "hc:1"}
	agg.enrichBook(context.Background(), other)
	if other.Description == "mine" {
		t.Fatal("a locked book's own description was served to another book from the enrichment cache")
	}
	if other.Description != "A considerably longer description supplied by the enricher." {
		t.Errorf("description = %q, want the enricher's", other.Description)
	}
}
