package metadata

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The enrichment cache is keyed on (provider, foreignID), which every copy of
// a work shares, so a slice stored there is reachable from every book that
// later reads it. enrichmentSnapshot copies its scalar fields out of the book
// by value, but genres is a slice: storing it without a copy aliases the
// book's backing array into the cache, and handing it back without a copy
// aliases the cache's array into the next book. Either direction lets an
// ordinary in-place edit (book.Genres[0] = ...) rewrite the cached entry that
// every other book of that work reads (#2783).
//
// No production caller mutates a returned Genres slice in place today, which is
// why the contract is pinned here rather than fixed at a call site.
func newGenreAliasingAggregator() *Aggregator {
	enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
		{Title: "Aliased Book", Genres: []string{"Fantasy", "Adventure"}},
	}}
	return newTestAggregator(&mockProvider{name: "ol"}, enricher)
}

// TestEnrichBookSnapshotDoesNotAliasTheStoredGenres is the direction the issue
// describes: the book that seeded the cache is mutated after enrichBook
// returns, and the next book for the same key must not see the edit.
func TestEnrichBookSnapshotDoesNotAliasTheStoredGenres(t *testing.T) {
	agg := newGenreAliasingAggregator()

	first := &models.Book{Title: "Aliased Book", MetadataProvider: "hardcover", ForeignID: "hc:1"}
	agg.enrichBook(context.Background(), first)
	if !slices.Equal(first.Genres, []string{"Fantasy", "Adventure"}) {
		t.Fatalf("genres before mutation = %v, want [Fantasy Adventure]", first.Genres)
	}

	// The caller owns the slice it was handed; editing it must not reach the
	// cache entry.
	first.Genres[0] = "Poisoned"

	second := &models.Book{Title: "Aliased Book", MetadataProvider: "hardcover", ForeignID: "hc:1"}
	agg.enrichBook(context.Background(), second)
	if !slices.Equal(second.Genres, []string{"Fantasy", "Adventure"}) {
		t.Errorf("second book genres = %v, want the cached [Fantasy Adventure]", second.Genres)
	}
}

// TestEnrichBookCacheHitDoesNotAliasTheSnapshot is the other direction: a cache
// hit assigns the snapshot's slice to the book, so the same in-place edit from
// that book must not rewrite what the next hit reads. One copy on store alone
// would still fail this.
func TestEnrichBookCacheHitDoesNotAliasTheSnapshot(t *testing.T) {
	agg := newGenreAliasingAggregator()

	// Seed the cache.
	agg.enrichBook(context.Background(), &models.Book{Title: "Aliased Book", MetadataProvider: "hardcover", ForeignID: "hc:1"})

	hit := &models.Book{Title: "Aliased Book", MetadataProvider: "hardcover", ForeignID: "hc:1"}
	agg.enrichBook(context.Background(), hit)
	if !slices.Equal(hit.Genres, []string{"Fantasy", "Adventure"}) {
		t.Fatalf("cache-hit genres = %v, want [Fantasy Adventure]", hit.Genres)
	}
	hit.Genres[1] = "Poisoned"

	next := &models.Book{Title: "Aliased Book", MetadataProvider: "hardcover", ForeignID: "hc:1"}
	agg.enrichBook(context.Background(), next)
	if !slices.Equal(next.Genres, []string{"Fantasy", "Adventure"}) {
		t.Errorf("genres after a mutated cache hit = %v, want the cached [Fantasy Adventure]", next.Genres)
	}
}
