package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAggregator_GetBookByISBN_PrimaryFallbackWinsWhenEnricherDoesNotCanonicalize(t *testing.T) {
	book := &models.Book{Title: "The Left Hand of Darkness", Description: "A novel long enough description to pass the enrichment check easily."}
	primary := &mockProvider{name: "ol", getByISBN: book}
	enricher := &mockProvider{name: "hardcover", getByISBN: &models.Book{Title: "Wrong Book"}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.GetBookByISBN(context.Background(), "9780441478125")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got.Title != "The Left Hand of Darkness" {
		t.Errorf("Title: want 'The Left Hand of Darkness', got %q", got.Title)
	}
	if primary.getByISBNCalls != 1 {
		t.Errorf("primary calls = %d, want 1", primary.getByISBNCalls)
	}
	if enricher.getByISBNCalls != 1 {
		t.Errorf("enricher calls = %d, want 1 while checking for canonical fallback", enricher.getByISBNCalls)
	}
}

func TestAggregator_GetBookByISBN_SearchesRegisteredEnrichers(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
	}{
		{name: "google books", provider: "googlebooks"},
		{name: "hardcover", provider: "hardcover"},
		{name: "dnb", provider: "dnb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary := &mockProvider{name: "ol"}
			enricher := &mockProvider{
				name:      tt.provider,
				getByISBN: &models.Book{Title: tt.provider + " ISBN Book", MetadataProvider: tt.provider},
			}
			agg := newTestAggregator(primary, enricher)

			got, err := agg.GetBookByISBN(context.Background(), "9780000000002")
			if err != nil {
				t.Fatalf("GetBookByISBN: %v", err)
			}
			if got == nil {
				t.Fatal("expected secondary provider result")
			}
			if got.MetadataProvider != tt.provider {
				t.Fatalf("MetadataProvider = %q, want %q", got.MetadataProvider, tt.provider)
			}
			if primary.getByISBNCalls != 1 || enricher.getByISBNCalls != 1 {
				t.Fatalf("calls primary=%d enricher=%d, want 1/1", primary.getByISBNCalls, enricher.getByISBNCalls)
			}
		})
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesSecondaryHitToPrimary(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780593135204": nil,
			"Project Hail Mary Andy Weir": {{
				ForeignID: "OL-PHM",
				Title:     "Project Hail Mary",
				Author:    &models.Author{Name: "Andy Weir"},
			}},
		},
		getBook: &models.Book{
			ForeignID:        "OL-PHM",
			Title:            "Project Hail Mary",
			Description:      "OpenLibrary canonical description long enough to avoid extra enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Andy Weir", ForeignID: "OL-A"},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:vol-phm",
			Title:            "Project Hail Mary",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Andy Weir"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780593135204")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-PHM" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary book", got)
	}
	wantQueries := []string{"isbn:9780593135204", "Project Hail Mary Andy Weir"}
	if !equalStringSlices(primary.searchBookQueries, wantQueries) {
		t.Fatalf("search queries = %v, want %v", primary.searchBookQueries, wantQueries)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesSecondaryHitUsingISBNSearch(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780441172719": {{
				ForeignID: "OL-DUNE",
				Title:     "Dune",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"Dune Frank Herbert": {
				{ForeignID: "OL1W", Title: "Dune", Author: &models.Author{Name: "Frank Herbert"}},
				{ForeignID: "OL2W", Title: "Dune", Author: &models.Author{Name: "Frank Herbert"}},
			},
		},
		getBook: &models.Book{
			ForeignID:        "OL-DUNE",
			Title:            "Dune",
			Description:      "OpenLibrary canonical description long enough to avoid extra enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:dune",
			Title:            "Dune",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780441172719")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-DUNE" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary book from ISBN search", got)
	}
	wantQueries := []string{"isbn:9780441172719"}
	if !equalStringSlices(primary.searchBookQueries, wantQueries) {
		t.Fatalf("search queries = %v, want %v", primary.searchBookQueries, wantQueries)
	}
}

func TestAggregator_GetBookByISBN_NormalizesISBNBeforeProviderAndCanonicalSearch(t *testing.T) {
	inputs := []string{
		"978 0 307 47472 8",
		"978-0-307-47472-8",
		"978_0_307_47472_8",
		" 0307474720 ",
		"3-453-30523-x",
	}
	wantISBNs := []string{
		"9780307474728",
		"9780307474728",
		"9780307474728",
		"0307474720",
		"345330523X",
	}
	for i, input := range inputs {
		primary := &mockProvider{name: "openlibrary"}
		google := &mockProvider{
			name: "googlebooks",
			getByISBNByISBN: map[string]*models.Book{
				wantISBNs[i]: {
					ForeignID:        "gb:book",
					Title:            "Cien años de soledad",
					Description:      "Google Books description long enough to avoid enrichment.",
					MetadataProvider: "googlebooks",
					Author:           &models.Author{Name: "Gabriel García Márquez"},
				},
			},
		}
		agg := newTestAggregator(primary, google)

		if _, err := agg.GetBookByISBN(context.Background(), input); err != nil {
			t.Fatalf("GetBookByISBN(%q): %v", input, err)
		}
		if len(primary.gotISBNs) != 1 || primary.gotISBNs[0] != wantISBNs[i] {
			t.Fatalf("primary ISBNs for %q = %v, want %q", input, primary.gotISBNs, wantISBNs[i])
		}
		if len(google.gotISBNs) != 1 || google.gotISBNs[0] != wantISBNs[i] {
			t.Fatalf("google ISBNs for %q = %v, want %q", input, google.gotISBNs, wantISBNs[i])
		}
	}
}

func TestAggregator_GetBookByISBN_NormalizedISBNSearchBeatsDuplicateTitleFallback(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780307474728": {{
				ForeignID: "OL274505W",
				Title:     "Cien años de soledad",
				Author:    &models.Author{Name: "Gabriel García Márquez"},
			}},
			"Cien años de soledad Gabriel García Márquez": {{
				ForeignID: "OL40693424W",
				Title:     "Cien años de Soledad",
				Author:    &models.Author{Name: "Gabriel García Márquez"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL274505W": {
				ForeignID:        "OL274505W",
				Title:            "Cien años de soledad",
				Description:      "OpenLibrary canonical description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Gabriel García Márquez"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBNByISBN: map[string]*models.Book{
			"9780307474728": {
				ForeignID:        "gb:cien",
				Title:            "Cien años de soledad",
				Description:      "Google Books description long enough to avoid enrichment.",
				MetadataProvider: "googlebooks",
				Author:           &models.Author{Name: "Gabriel García Márquez"},
			},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "978 0 307 47472 8")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL274505W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want normalized ISBN canonical OpenLibrary work", got)
	}
	if len(primary.searchBookQueries) == 0 || primary.searchBookQueries[0] != "isbn:9780307474728" {
		t.Fatalf("search queries = %v, want normalized ISBN query first", primary.searchBookQueries)
	}
}

func TestAggregator_GetBookByISBN_PrimaryISBNSearchDoesNotFallThroughToWrongTitleSearch(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		getByISBN: &models.Book{
			ForeignID:        "OL-CORRECT",
			Title:            "Classic Title",
			Description:      "Primary OpenLibrary ISBN description long enough to avoid enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Jane Author"},
		},
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780000000008": {{
				ForeignID: "OL-CORRECT",
				Title:     "Classic Title",
				Author:    &models.Author{Name: "Jane Author"},
			}},
			"Classic Title Jane Author": {{
				ForeignID: "OL-WRONG",
				Title:     "Classic Title",
				Author:    &models.Author{Name: "Jane Author"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL-WRONG": {
				ForeignID:        "OL-WRONG",
				Title:            "Classic Title",
				Description:      "Wrong OpenLibrary title-search work description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
		},
	}
	agg := newTestAggregator(primary)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000008")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-CORRECT" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want exact OpenLibrary ISBN work", got)
	}
	wantQueries := []string{"isbn:9780000000008"}
	if !equalStringSlices(primary.searchBookQueries, wantQueries) {
		t.Fatalf("search queries = %v, want %v", primary.searchBookQueries, wantQueries)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no title-search canonical fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_ISBNSearchWinsOverPlausibleWrongTitleSearch(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780000000001": {{
				ForeignID: "OL-CORRECT",
				Title:     "Classic Title",
				Author:    &models.Author{Name: "Jane Author"},
			}},
			"Classic Title Jane Author": {{
				ForeignID: "OL-WRONG",
				Title:     "Classic Title",
				Author:    &models.Author{Name: "Jane Author"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL-CORRECT": {
				ForeignID:        "OL-CORRECT",
				Title:            "Classic Title",
				Description:      "Correct OpenLibrary ISBN work description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
			"OL-WRONG": {
				ForeignID:        "OL-WRONG",
				Title:            "Classic Title",
				Description:      "Wrong OpenLibrary title-search work description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:classic-title",
			Title:            "Classic Title",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000001")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-CORRECT" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want exact ISBN OpenLibrary work", got)
	}
	wantQueries := []string{"isbn:9780000000001"}
	if !equalStringSlices(primary.searchBookQueries, wantQueries) {
		t.Fatalf("search queries = %v, want %v", primary.searchBookQueries, wantQueries)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesNoiseWordTitleAfterISBNMiss(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780000000003": nil,
			"The Book Jane Author": {{
				ForeignID: "OL-THE-BOOK",
				Title:     "The Book",
				Author:    &models.Author{Name: "Jane Author"},
			}},
		},
		getBook: &models.Book{
			ForeignID:        "OL-THE-BOOK",
			Title:            "The Book",
			Description:      "OpenLibrary canonical description long enough to avoid extra enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:the-book",
			Title:            "The Book",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000003")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-THE-BOOK" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary book", got)
	}
	wantQueries := []string{"isbn:9780000000003", "The Book Jane Author"}
	if !equalStringSlices(primary.searchBookQueries, wantQueries) {
		t.Fatalf("search queries = %v, want %v", primary.searchBookQueries, wantQueries)
	}
}

func TestAggregator_GetBookByISBN_ContinuesAfterProviderError(t *testing.T) {
	primary := &mockProvider{name: "ol", getByISBNErr: errors.New("openlibrary down")}
	enricher := &mockProvider{name: "dnb", getByISBN: &models.Book{Title: "DNB ISBN Book", MetadataProvider: "dnb"}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.GetBookByISBN(context.Background(), "9783453198975")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.Title != "DNB ISBN Book" {
		t.Fatalf("got %+v, want DNB ISBN Book", got)
	}
}

func TestAggregator_GetBookByISBN_SkipsUnconfiguredProviders(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	unconfigured := &mockProvider{name: "hardcover", getByISBNErr: ErrProviderNotConfigured}
	dnb := &mockProvider{name: "dnb", getByISBN: &models.Book{Title: "DNB ISBN Book", MetadataProvider: "dnb"}}
	agg := newTestAggregator(primary, unconfigured, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453198975")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.MetadataProvider != "dnb" {
		t.Fatalf("got %+v, want dnb result", got)
	}
}

func TestAggregator_GetBookByISBN_AllProvidersMiss(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	enricher := &mockProvider{name: "dnb"}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.GetBookByISBN(context.Background(), "0000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN(nil): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing ISBN, got %+v", got)
	}
}

func TestAggregator_GetBookByISBN_AllConfiguredProvidersFail(t *testing.T) {
	primary := &mockProvider{name: "ol", getByISBNErr: errors.New("openlibrary down")}
	enricher := &mockProvider{name: "dnb", getByISBNErr: errors.New("dnb down")}
	agg := newTestAggregator(primary, enricher)

	_, err := agg.GetBookByISBN(context.Background(), "9780000000003")
	if err == nil {
		t.Fatal("expected error when all configured providers fail")
	}
}

func TestAggregator_GetBookByISBN_FirstSuccessfulProviderWins(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	first := &mockProvider{name: "googlebooks", getByISBN: &models.Book{Title: "First", MetadataProvider: "googlebooks"}}
	second := &mockProvider{name: "dnb", getByISBN: &models.Book{Title: "Second", MetadataProvider: "dnb"}}
	agg := newTestAggregator(primary, first, second)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000004")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.Title != "First" {
		t.Fatalf("got %+v, want first provider result", got)
	}
	if second.getByISBNCalls != 0 {
		t.Fatalf("second provider calls = %d, want 0", second.getByISBNCalls)
	}
}

func TestAggregator_GetBookByISBN_EnrichesShortDescription(t *testing.T) {
	primary := &mockProvider{name: "ol", getByISBN: &models.Book{Title: "Sparse ISBN", Description: "Short."}}
	enricher := &mockProvider{
		name: "googlebooks",
		searchBooks: []models.Book{{
			Title:         "Sparse ISBN",
			Description:   "A fuller description from a configured enricher that should replace the sparse ISBN result.",
			AverageRating: 4.2,
			RatingsCount:  12,
		}},
	}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000005")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil {
		t.Fatal("expected book")
	}
	if got.Description == "Short." {
		t.Fatalf("expected enriched description, got %q", got.Description)
	}
	if got.AverageRating != 4.2 || got.RatingsCount != 12 {
		t.Fatalf("rating/count = %f/%d, want 4.2/12", got.AverageRating, got.RatingsCount)
	}
}

func TestAggregator_GetBookByISBN_CachesSecondaryProviderHit(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	enricher := &mockProvider{name: "hardcover", getByISBN: &models.Book{Title: "Cached Secondary", MetadataProvider: "hardcover"}}
	agg := newTestAggregator(primary, enricher)

	_, err := agg.GetBookByISBN(context.Background(), "9780000000006")
	if err != nil {
		t.Fatalf("first GetBookByISBN: %v", err)
	}
	enricher.getByISBN = nil

	got, err := agg.GetBookByISBN(context.Background(), "9780000000006")
	if err != nil {
		t.Fatalf("cached GetBookByISBN: %v", err)
	}
	if got == nil || got.Title != "Cached Secondary" {
		t.Fatalf("cached book = %+v, want Cached Secondary", got)
	}
	if primary.getByISBNCalls != 1 || enricher.getByISBNCalls != 1 {
		t.Fatalf("calls primary=%d enricher=%d, want 1/1 after cache hit", primary.getByISBNCalls, enricher.getByISBNCalls)
	}
}

func TestAggregator_GetBookByISBN_CachesCleanMisses(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	enricher := &mockProvider{name: "dnb"}
	agg := newTestAggregator(primary, enricher)

	for i := 0; i < 2; i++ {
		got, err := agg.GetBookByISBN(context.Background(), "0000000000")
		if err != nil {
			t.Fatalf("GetBookByISBN #%d: %v", i+1, err)
		}
		if got != nil {
			t.Fatalf("GetBookByISBN #%d = %+v, want nil", i+1, got)
		}
	}
	if primary.getByISBNCalls != 1 || enricher.getByISBNCalls != 1 {
		t.Fatalf("calls primary=%d enricher=%d, want 1/1 after cached miss", primary.getByISBNCalls, enricher.getByISBNCalls)
	}
}
