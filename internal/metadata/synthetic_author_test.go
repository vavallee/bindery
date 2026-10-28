package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSyntheticGoogleBooksAuthorID(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Owen Brackett", "gb:author:owen-brackett"},
		{"  owen   BRACKETT ", "gb:author:owen-brackett"},
		{"Owen Brackett.", "gb:author:owen-brackett"},
		{"José Saramago", "gb:author:jose-saramago"},
		{"", ""},
		{"   ", ""},
		{"...", ""},
		{"-- !! --", ""},
	}
	for _, tc := range cases {
		if got := SyntheticGoogleBooksAuthorID(tc.name); got != tc.want {
			t.Errorf("SyntheticGoogleBooksAuthorID(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	// A name in a script the ASCII reduction cannot hold still gets an ID of
	// its own, and two such names do not share one.
	a, b := SyntheticGoogleBooksAuthorID("村上春樹"), SyntheticGoogleBooksAuthorID("夏目漱石")
	if a == "" || b == "" || a == b {
		t.Errorf("non latin names must get distinct IDs, got %q and %q", a, b)
	}
}

func TestIsSyntheticAuthorID(t *testing.T) {
	for id, want := range map[string]bool{
		"gb:author:owen-brackett": true,
		" GB:author:x":            true,
		"gb:S2IcEgAAQBAJ":         false, // a volume id, not an author
		"dnb:author:x":            false,
		"OL1A":                    false,
		"":                        false,
	} {
		if got := IsSyntheticAuthorID(id); got != want {
			t.Errorf("IsSyntheticAuthorID(%q) = %v, want %v", id, got, want)
		}
	}
}

// A synthetic Google Books author has nothing upstream. Every aggregator
// entry an author sync, refresh or reconciliation uses must answer for it
// quietly and empty, never reach a provider, and never claim a complete
// (deletion safe) catalogue.
func TestAggregator_SyntheticGoogleBooksAuthorShortCircuits(t *testing.T) {
	const id = "gb:author:owen-brackett"
	ctx := context.Background()
	gb := &mockWorksProvider{mockProvider: mockProvider{
		name:         "googlebooks",
		getAuthor:    &models.Author{ForeignID: id, Name: "must not be returned"},
		getAuthorErr: errors.New("google books does not support author lookup by ID"),
	}}
	gb.authorWorks = []models.Book{{ForeignID: "gb:other", Title: "must not be returned"}}
	// Google Books is the primary here, the case where an unknown author ID
	// would otherwise fall back to searching the provider for the ID text.
	agg := NewAggregator(gb)

	author, err := agg.GetAuthor(ctx, id)
	if err != nil || author != nil {
		t.Errorf("GetAuthor = (%v, %v), want (nil, nil)", author, err)
	}

	books, err := agg.GetAuthorWorks(ctx, id)
	if err != nil || len(books) != 0 {
		t.Errorf("GetAuthorWorks = (%d books, %v), want none and no error", len(books), err)
	}
	books, err = agg.GetAuthorWorksUnenriched(ctx, id)
	if err != nil || len(books) != 0 {
		t.Errorf("GetAuthorWorksUnenriched = (%d books, %v), want none and no error", len(books), err)
	}
	books, err = agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), models.Author{ForeignID: id, Name: "Owen Brackett"})
	if err != nil || len(books) != 0 {
		t.Errorf("GetAuthorWorksForAuthor(refresh) = (%d books, %v), want none and no error", len(books), err)
	}

	snap, err := agg.GetAuthorWorksSnapshotForAuthor(ctx, models.Author{ForeignID: id, Name: "Owen Brackett"})
	if err != nil {
		t.Fatalf("snapshot errored loudly: %v", err)
	}
	if snap.Complete || len(snap.Books) != 0 {
		t.Errorf("snapshot = complete %v with %d books, want incomplete and empty, so reconciliation never reads it as removals", snap.Complete, len(snap.Books))
	}

	if gb.authorWorksCalls != 0 {
		t.Errorf("provider GetAuthorWorks was called %d times, want 0", gb.authorWorksCalls)
	}
	if len(gb.searchBookQueries) != 0 {
		t.Errorf("provider was searched for the synthetic id: %v", gb.searchBookQueries)
	}
}

// The ordinary Google Books ID keeps routing to its provider: only the
// synthetic author prefix is special.
func TestAggregator_GoogleBooksVolumeIDStillRoutes(t *testing.T) {
	gb := &mockWorksProvider{mockProvider: mockProvider{
		name:      "googlebooks",
		getAuthor: &models.Author{ForeignID: "gb:something", Name: "Real"},
	}}
	agg := NewAggregator(gb)
	got, err := agg.GetAuthor(context.Background(), "gb:something")
	if err != nil || got == nil || got.Name != "Real" {
		t.Errorf("GetAuthor(gb:something) = (%v, %v), want the provider's author", got, err)
	}
}
