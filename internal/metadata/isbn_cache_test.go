package metadata

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

const isbnCacheTestDescription = "A description long enough that the ISBN path skips enrichment entirely."

// Every caller of an ISBN lookup gets its own copy of the cached book: editing
// the first answer must not rewrite what the next caller is handed (#2869).
func TestGetBookByISBN_CacheHitIsACopy(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getByISBN: &models.Book{
		ForeignID:   "OL1W",
		Title:       "Dune",
		Description: isbnCacheTestDescription,
		Genres:      []string{"Science Fiction"},
		Author:      &models.Author{Name: "Frank Herbert"},
	}}
	a := newTestAggregator(primary)

	first, err := a.GetBookByISBN(context.Background(), "9780441172719")
	if err != nil || first == nil {
		t.Fatalf("first lookup: %+v %v", first, err)
	}
	first.Genres[0] = "mutated"
	first.OwnerUserID = 42
	first.Title = "mutated"
	first.Author.Name = "mutated"

	second, err := a.GetBookByISBN(context.Background(), "9780441172719")
	if err != nil || second == nil {
		t.Fatalf("second lookup: %+v %v", second, err)
	}
	if primary.getByISBNCalls != 1 {
		t.Fatalf("provider asked %d times, want 1 (second lookup should be a cache hit)", primary.getByISBNCalls)
	}
	if second.Genres[0] != "Science Fiction" || second.OwnerUserID != 0 || second.Title != "Dune" || second.Author.Name != "Frank Herbert" {
		t.Fatalf("cache entry rewritten through the first caller's book: %+v author=%+v", second, second.Author)
	}
	// And a hit is not shared between two hit callers either.
	second.Genres[0] = "mutated again"
	third, _ := a.GetBookByISBN(context.Background(), "9780441172719")
	if third.Genres[0] != "Science Fiction" {
		t.Fatalf("cache entry rewritten through a cache hit: %v", third.Genres)
	}
}

// isbnScopedProvider stands in for a provider whose configuration (a token,
// a primary switch) changes between two lookups.
type isbnScopedProvider struct {
	*mockProvider
	scope string
}

func (p *isbnScopedProvider) ResolveCacheProvider(context.Context) (Provider, string) {
	return &mockProvider{name: p.name, getByISBN: &models.Book{
		ForeignID: "OL1W", Title: "answer under " + p.scope, Description: isbnCacheTestDescription,
	}}, p.scope
}

// An ISBN answer built under one provider configuration is not served after
// the configuration changes (#2869).
func TestGetBookByISBN_KeyedByProviderScope(t *testing.T) {
	primary := &isbnScopedProvider{mockProvider: &mockProvider{name: "openlibrary"}, scope: "first"}
	a := newTestAggregator(primary)
	for _, scope := range []string{"first", "second", "first"} {
		primary.scope = scope
		book, err := a.GetBookByISBN(context.Background(), "9780441172719")
		if err != nil || book == nil || book.Title != "answer under "+scope {
			t.Fatalf("scope %q: got %+v %v", scope, book, err)
		}
	}
}

// An ISBN record built while an enricher was failing is incomplete, so it is
// kept for the five minute window rather than the 24 hour one (#2869).
func TestGetBookByISBN_IncompleteEnrichmentIsShortCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		primary := &mockProvider{name: "openlibrary", getByISBN: &models.Book{
			ForeignID: "OL1W", Title: "Dune", Author: &models.Author{Name: "Frank Herbert"},
		}}
		enricher := &mockProvider{name: "hardcover", searchBookErr: errors.New("rate limited")}
		a := newRequestTestAggregator(primary)
		a.enrichers = []Provider{enricher}

		book, err := a.GetBookByISBN(context.Background(), "9780441172719")
		if err != nil || book == nil || book.Description != "" {
			t.Fatalf("first lookup: %+v %v", book, err)
		}
		if _, err := a.GetBookByISBN(context.Background(), "9780441172719"); err != nil {
			t.Fatal(err)
		}
		if primary.getByISBNCalls != 1 {
			t.Fatalf("incomplete record not cached at all: %d provider calls", primary.getByISBNCalls)
		}

		enricher.searchBookErr = nil
		enricher.searchBooks = []models.Book{{Title: "Dune", Author: &models.Author{Name: "Frank Herbert"}, Description: isbnCacheTestDescription}}
		time.Sleep(5*time.Minute + time.Nanosecond)
		book, err = a.GetBookByISBN(context.Background(), "9780441172719")
		if err != nil || book == nil || book.Description != isbnCacheTestDescription {
			t.Fatalf("record built during an enricher failure was pinned past the short window: %+v %v", book, err)
		}

		// The complete record now holds for the long TTL.
		time.Sleep(5*time.Minute + time.Nanosecond)
		if _, err := a.GetBookByISBN(context.Background(), "9780441172719"); err != nil {
			t.Fatal(err)
		}
		if primary.getByISBNCalls != 2 {
			t.Fatalf("complete record not cached for the long TTL: %d provider calls", primary.getByISBNCalls)
		}
	})
}

func canonicalASINTestAggregator(primary Provider) (*Aggregator, *stubAudnexClient) {
	audnexClient := &stubAudnexClient{books: map[string]*audnex.Book{
		"B0D3R3MTLM": {
			ASIN:     "B0D3R3MTLM",
			Title:    "Iron Flame",
			Authors:  []audnex.Person{{Name: "Rebecca Yarros"}},
			Language: "English",
		},
	}}
	return newTestAggregator(primary).WithAudnexClient(audnexClient), audnexClient
}

func canonicalASINTestPrimary(name string) *mockProvider {
	return &mockProvider{
		name: name,
		searchBooksByQuery: map[string][]models.Book{
			"Iron Flame Rebecca Yarros": {{
				ForeignID: "OL-IRON", Title: "Iron Flame", EditionCount: 42,
				Author: &models.Author{Name: "Rebecca Yarros"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL-IRON": {
				ForeignID: "OL-IRON", Title: "Iron Flame", Description: strings.Repeat("Canonical. ", 10),
				ImageURL: "cover", Genres: []string{"Fantasy"}, MetadataProvider: "openlibrary",
				Author: &models.Author{Name: "Rebecca Yarros"},
			},
		},
	}
}

// The canonical ASIN lookup has the same shape and gets the same treatment:
// a caller editing its answer does not rewrite the cache (#2869).
func TestGetCanonicalBookByASIN_CacheHitIsACopy(t *testing.T) {
	a, _ := canonicalASINTestAggregator(canonicalASINTestPrimary("openlibrary"))
	first, err := a.GetCanonicalBookByASIN(context.Background(), "B0D3R3MTLM")
	if err != nil || first == nil {
		t.Fatalf("first lookup: %+v %v", first, err)
	}
	first.Genres[0] = "mutated"
	first.Title = "mutated"

	second, err := a.GetCanonicalBookByASIN(context.Background(), "B0D3R3MTLM")
	if err != nil || second == nil {
		t.Fatalf("second lookup: %+v %v", second, err)
	}
	if second.Title != "Iron Flame" || second.Genres[0] != "Fantasy" {
		t.Fatalf("cache entry rewritten through the first caller's book: %+v", second)
	}
}

type canonicalScopedProvider struct {
	*mockProvider
	scope   string
	byScope map[string]*mockProvider
}

func (p *canonicalScopedProvider) ResolveCacheProvider(context.Context) (Provider, string) {
	return p.byScope[p.scope], p.scope
}

// A canonical ASIN answer built under one provider configuration is not
// served after it changes (#2869).
func TestGetCanonicalBookByASIN_KeyedByProviderScope(t *testing.T) {
	firstPrimary := canonicalASINTestPrimary("openlibrary")
	secondPrimary := canonicalASINTestPrimary("openlibrary")
	secondPrimary.searchBooksByQuery["Iron Flame Rebecca Yarros"][0].ForeignID = "OL-IRON-2"
	primary := &canonicalScopedProvider{
		mockProvider: &mockProvider{name: "openlibrary"},
		scope:        "first",
		byScope:      map[string]*mockProvider{"first": firstPrimary, "second": secondPrimary},
	}
	a, _ := canonicalASINTestAggregator(primary)
	if book, err := a.GetCanonicalBookByASIN(context.Background(), "B0D3R3MTLM"); err != nil || book == nil || book.ForeignID != "OL-IRON" {
		t.Fatalf("first scope: %+v %v", book, err)
	}
	primary.scope = "second"
	book, err := a.GetCanonicalBookByASIN(context.Background(), "B0D3R3MTLM")
	if err != nil || book == nil || book.ForeignID != "OL-IRON-2" {
		t.Fatalf("second scope served the first configuration's answer: %+v %v", book, err)
	}
}
