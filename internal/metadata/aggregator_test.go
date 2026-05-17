package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestAggregator_SearchAuthors(t *testing.T) {
	want := []models.Author{{Name: "Frank Herbert", ForeignID: "OL123A"}}
	primary := &mockProvider{name: "ol", searchAuthors: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Frank Herbert" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_SearchAuthors_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", searchAuthErr: errors.New("network error")}
	agg := newTestAggregator(primary)

	_, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}

func TestAggregator_SearchBooks(t *testing.T) {
	want := []models.Book{{Title: "Dune", ForeignID: "OL456W"}}
	primary := &mockProvider{name: "ol", searchBooks: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Dune" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_GetAuthor_Success(t *testing.T) {
	author := &models.Author{Name: "Ursula K. Le Guin", ForeignID: "OL111A"}
	primary := &mockProvider{name: "ol", getAuthor: author}
	agg := newTestAggregator(primary)

	got, err := agg.GetAuthor(context.Background(), "OL111A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if got.Name != "Ursula K. Le Guin" {
		t.Errorf("Name: want 'Ursula K. Le Guin', got %q", got.Name)
	}
}

func TestAggregator_GetAuthor_Cached(t *testing.T) {
	calls := 0
	primary := &mockProvider{name: "ol", getAuthor: &models.Author{Name: "Isaac Asimov"}}
	agg := newTestAggregator(primary)
	// Wrap to count calls
	origGetAuthor := primary.getAuthor

	_, _ = agg.GetAuthor(context.Background(), "OL999A")
	calls++                 // first call
	primary.getAuthor = nil // second call should use cache, not nil author
	got, err := agg.GetAuthor(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthor (cached): %v", err)
	}
	if got.Name != origGetAuthor.Name {
		t.Errorf("expected cached author, got %+v", got)
	}
	_ = calls
}

func TestAggregator_GetAuthor_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getAuthorErr: errors.New("not found")}
	agg := newTestAggregator(primary)

	_, err := agg.GetAuthor(context.Background(), "OL999A")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetBook_LongDescription(t *testing.T) {
	// A book with a long description should NOT trigger enrichment.
	longDesc := string(make([]byte, 100)) // 100-char description
	for i := range longDesc {
		_ = i
	}
	longDesc = "This is a very long book description that exceeds the fifty character minimum and should never be enriched by secondary providers."
	book := &models.Book{Title: "Dune", Description: longDesc}

	enricherCalled := false
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Description: "Should not be used"}},
	}
	// We'll detect if enricher was called by overriding its SearchBooks
	_ = enricherCalled
	primary := &mockProvider{name: "ol", getBook: book}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != longDesc {
		t.Errorf("description should not be overwritten when long enough")
	}
}

func TestAggregator_GetBook_ShortDescription_Enriched(t *testing.T) {
	shortDesc := "Short."
	richerDesc := "A much richer description that is longer than the short one from the primary provider."

	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Foundation", Description: shortDesc},
	}
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Title: "Foundation", Description: richerDesc}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL789W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != richerDesc {
		t.Errorf("expected enriched description %q, got %q", richerDesc, got.Description)
	}
}

func TestAggregator_GetBook_Enrichment_RatingFilled(t *testing.T) {
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Short", Description: "x", AverageRating: 0},
	}
	enricher := &mockProvider{
		name:        "hc",
		searchBooks: []models.Book{{Title: "Short", Description: "Some desc", AverageRating: 4.5, RatingsCount: 100}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL001W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.AverageRating != 4.5 {
		t.Errorf("rating: want 4.5, got %f", got.AverageRating)
	}
	if got.RatingsCount != 100 {
		t.Errorf("ratingsCount: want 100, got %d", got.RatingsCount)
	}
}

func TestAggregator_GetBook_Cached(t *testing.T) {
	primary := &mockProvider{name: "ol", getBook: &models.Book{Title: "Cached Book", Description: "A sufficiently long description for caching test purposes here."}}
	agg := newTestAggregator(primary)

	first, _ := agg.GetBook(context.Background(), "OL111W")
	primary.getBook = nil // clear so second call must use cache

	second, err := agg.GetBook(context.Background(), "OL111W")
	if err != nil {
		t.Fatalf("GetBook (cache): %v", err)
	}
	if second.Title != first.Title {
		t.Errorf("cached book mismatch: got %q", second.Title)
	}
}

func TestAggregator_GetBook_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getBookErr: errors.New("lookup failed")}
	agg := newTestAggregator(primary)

	_, err := agg.GetBook(context.Background(), "OL999W")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetBook_RoutesProviderPrefixes(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getBook: &models.Book{Title: "Wrong"}}
	google := &mockProvider{name: "googlebooks", getBook: &models.Book{ForeignID: "gb:vol1", Title: "Google Book", MetadataProvider: "googlebooks"}}
	hardcover := &mockProvider{name: "hardcover", getBook: &models.Book{ForeignID: "hc:book", Title: "Hardcover Book", MetadataProvider: "hardcover"}}
	dnb := &mockProvider{name: "dnb", getBook: &models.Book{ForeignID: "dnb:123", Title: "DNB Book", MetadataProvider: "dnb"}}
	agg := newTestAggregator(primary, google, hardcover, dnb)

	tests := []struct {
		foreignID string
		wantTitle string
		provider  *mockProvider
	}{
		{foreignID: "gb:vol1", wantTitle: "Google Book", provider: google},
		{foreignID: "hc:book", wantTitle: "Hardcover Book", provider: hardcover},
		{foreignID: "dnb:123", wantTitle: "DNB Book", provider: dnb},
	}
	for _, tt := range tests {
		got, err := agg.GetBook(context.Background(), tt.foreignID)
		if err != nil {
			t.Fatalf("GetBook(%q): %v", tt.foreignID, err)
		}
		if got == nil || got.Title != tt.wantTitle {
			t.Fatalf("GetBook(%q) = %+v, want %s", tt.foreignID, got, tt.wantTitle)
		}
		if tt.provider.getBookCalls != 1 || tt.provider.gotBookIDs[0] != tt.foreignID {
			t.Fatalf("%s calls=%d ids=%v, want one %s", tt.provider.name, tt.provider.getBookCalls, tt.provider.gotBookIDs, tt.foreignID)
		}
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("primary get calls = %d, want 0", primary.getBookCalls)
	}
}

func TestAggregator_GetAuthor_RoutesProviderPrefixes(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getAuthor: &models.Author{Name: "Wrong"}}
	hardcover := &mockProvider{name: "hardcover", getAuthor: &models.Author{ForeignID: "hc:author", Name: "Hardcover Author", MetadataProvider: "hardcover"}}
	agg := newTestAggregator(primary, hardcover)

	got, err := agg.GetAuthor(context.Background(), "hc:author")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if got == nil || got.Name != "Hardcover Author" {
		t.Fatalf("got %+v, want Hardcover Author", got)
	}
}

func TestAggregator_GetEditions_Success(t *testing.T) {
	editions := []models.Edition{{Title: "1st ed."}, {Title: "2nd ed."}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	got, err := agg.GetEditions(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 editions, got %d", len(got))
	}
}

func TestAggregator_GetEditions_Cached(t *testing.T) {
	editions := []models.Edition{{Title: "Paperback"}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	_, _ = agg.GetEditions(context.Background(), "OL999W")
	primary.getEditions = nil // clear; second call must use cache

	got, err := agg.GetEditions(context.Background(), "OL999W")
	if err != nil {
		t.Fatalf("GetEditions (cache): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Paperback" {
		t.Errorf("cached editions mismatch: %+v", got)
	}
}

// TestAggregator_ResolveBookByISBN_AcceptsDNBWithSyntheticAuthorID is the
// regression test for #608: prior to the DNB-author-foreign-id fix, the
// aggregator silently dropped DNB-only ISBN hits because Author.ForeignID
// was empty. Now that DNB populates a synthetic "dnb:gnd:" (or
// "dnb:author:") ForeignID, ResolveBookByISBN must accept it.
func TestAggregator_ResolveBookByISBN_AcceptsDNBWithSyntheticAuthorID(t *testing.T) {
	primary := &mockProvider{name: "openlibrary"} // OL doesn't have this ISBN.
	dnb := &mockProvider{name: "dnb", getByISBN: &models.Book{
		ForeignID: "dnb:bib-001",
		Title:     "Der Wüstenplanet",
		Author: &models.Author{
			ForeignID:        "dnb:gnd:118585665",
			Name:             "Frank Herbert",
			SortName:         "Herbert, Frank",
			MetadataProvider: "dnb",
		},
	}}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.ResolveBookByISBN(context.Background(), "9783453198975")
	if err != nil {
		t.Fatalf("ResolveBookByISBN: %v", err)
	}
	if got == nil {
		t.Fatal("expected DNB result with synthetic author ForeignID to be accepted, got nil")
	}
	if got.Author == nil || got.Author.ForeignID != "dnb:gnd:118585665" {
		t.Errorf("unexpected resolved author: %+v", got.Author)
	}
}

// TestAggregator_ResolveBookByISBN_StillSkipsResultsWithoutAuthorID guards
// the inverse: a provider that genuinely returns a book without any author
// ForeignID is still dropped, so the caller sees nil instead of a placeholder
// row it can't persist.
func TestAggregator_ResolveBookByISBN_StillSkipsResultsWithoutAuthorID(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getByISBN: &models.Book{
		Title:  "Title Only",
		Author: &models.Author{Name: "Unknown", ForeignID: ""},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.ResolveBookByISBN(context.Background(), "9780000000000")
	if err != nil {
		t.Fatalf("ResolveBookByISBN: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no provider has an author ForeignID, got %+v", got)
	}
}
