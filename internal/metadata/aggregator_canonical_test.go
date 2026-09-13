package metadata

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestPrimaryBookCanonicalQueries_OrdersFullTitlesBeforeDerivedSegments(t *testing.T) {
	tests := []struct {
		title string
		want  []primaryBookCanonicalQuery
	}{
		{
			title: "Dune – Der Wüstenplanet",
			want: []primaryBookCanonicalQuery{
				{query: "Dune – Der Wüstenplanet Frank Herbert", matchTitle: "Dune – Der Wüstenplanet", allowEditionTitleMatch: true, hasAuthor: true},
				{query: "title:Dune – Der Wüstenplanet author:Frank Herbert", matchTitle: "Dune – Der Wüstenplanet", allowEditionTitleMatch: true, hasAuthor: true},
				{query: "Dune Frank Herbert", matchTitle: "Dune", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantRightSegment},
				{query: "title:Dune author:Frank Herbert", matchTitle: "Dune", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantRightSegment},
			},
		},
		{
			title: "Dune – Der Wüstenplanet: Roman",
			want: []primaryBookCanonicalQuery{
				{query: "Dune – Der Wüstenplanet: Roman Frank Herbert", matchTitle: "Dune – Der Wüstenplanet: Roman", allowEditionTitleMatch: true, hasAuthor: true},
				{query: "title:Dune – Der Wüstenplanet: Roman author:Frank Herbert", matchTitle: "Dune – Der Wüstenplanet: Roman", allowEditionTitleMatch: true, hasAuthor: true},
				{query: "Dune – Der Wüstenplanet Frank Herbert", matchTitle: "Dune – Der Wüstenplanet", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantDescriptor},
				{query: "title:Dune – Der Wüstenplanet author:Frank Herbert", matchTitle: "Dune – Der Wüstenplanet", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantDescriptor},
				{query: "Dune Frank Herbert", matchTitle: "Dune", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantRightSegment},
				{query: "title:Dune author:Frank Herbert", matchTitle: "Dune", exactTitleOnly: true, allowRankTieBreak: true, allowEditionTitleMatch: true, hasAuthor: true, variantKind: canonicalTitleVariantRightSegment},
			},
		},
	}

	for _, tc := range tests {
		got := primaryBookCanonicalQueries("", tc.title, "Frank Herbert", "ger")
		if len(got) < len(tc.want) {
			t.Fatalf("queries for %q = %+v, want at least %d queries", tc.title, got, len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("queries for %q [%d] = %+v, want %+v (all queries=%+v)", tc.title, i, got[i], tc.want[i], got)
			}
		}
	}
}

func TestPrimaryBookCanonicalQueries_KeepsOriginalNoiseWordTitle(t *testing.T) {
	got := primaryBookCanonicalQueries("", "The Book", "Jane Author", "eng")
	want := []primaryBookCanonicalQuery{
		{query: "The Book Jane Author", matchTitle: "The Book", allowEditionTitleMatch: true, hasAuthor: true},
		{query: "title:The Book author:Jane Author", matchTitle: "The Book", allowEditionTitleMatch: true, hasAuthor: true},
	}
	if !equalCanonicalQueries(got, want) {
		t.Fatalf("queries = %+v, want %+v", got, want)
	}
}

func TestPrimaryBookCanonicalQueries_DoesNotStripRealTrailingDescriptorWords(t *testing.T) {
	got := primaryBookCanonicalQueries("", "Data Science", "Jane Author", "eng")
	for _, query := range got {
		if query.matchTitle == "Data" || query.query == "Data Jane Author" || query.query == "title:Data author:Jane Author" {
			t.Fatalf("generated unsafe derived title query %+v from Data Science (all queries=%+v)", query, got)
		}
	}
}

func TestPrimaryBookCanonicalQueries_GatesGermanNoPunctuationFallbackByLanguageAndDescriptor(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		author   string
		language string
		query    string
		want     bool
	}{
		{
			name:     "german three letter code",
			title:    "Dune der Wüstenplanet Roman",
			author:   "Frank Herbert",
			language: "ger",
			query:    "Dune Frank Herbert",
			want:     true,
		},
		{
			name:     "german two letter code",
			title:    "Dune der Wüstenplanet Roman",
			author:   "Frank Herbert",
			language: "de",
			query:    "Dune Frank Herbert",
			want:     true,
		},
		{
			name:     "german title without descriptor",
			title:    "Dune der Wüstenplanet",
			author:   "Frank Herbert",
			language: "ger",
			query:    "Dune Frank Herbert",
			want:     false,
		},
		{
			name:     "ordinary german genitive title",
			title:    "Haus der Sonne",
			author:   "Jane Author",
			language: "ger",
			query:    "Haus Jane Author",
			want:     false,
		},
		{
			name:     "unknown language",
			title:    "Dune der Wüstenplanet Roman",
			author:   "Frank Herbert",
			language: "",
			query:    "Dune Frank Herbert",
			want:     false,
		},
		{
			name:     "english die verb",
			title:    "Never Die Alone",
			author:   "Donald Goines",
			language: "eng",
			query:    "Never Donald Goines",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := primaryBookCanonicalQueries("", tc.title, tc.author, tc.language)
			found := false
			for _, query := range got {
				if query.query == tc.query {
					found = true
					break
				}
			}
			if found != tc.want {
				t.Fatalf("query %q present=%v, want %v (all queries=%+v)", tc.query, found, tc.want, got)
			}
		})
	}
}

func TestAggregator_GetBookByISBN_DoesNotApplyGermanFallbackToEnglishDieTitle(t *testing.T) {
	tests := []struct {
		name     string
		language string
	}{
		{name: "english language", language: "eng"},
		{name: "unknown language", language: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			primary := &mockProvider{
				name: "openlibrary",
				searchBooksByQuery: map[string][]models.Book{
					"Never Donald Goines": {{
						ForeignID: "OL-NEVER",
						Title:     "Never",
						Author:    &models.Author{Name: "Donald Goines"},
					}},
				},
				getBook: &models.Book{
					ForeignID:        "OL-NEVER",
					Title:            "Never",
					Description:      "Wrong OpenLibrary description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Donald Goines"},
				},
			}
			google := &mockProvider{
				name: "googlebooks",
				getByISBN: &models.Book{
					ForeignID:        "gb:never-die-alone",
					Title:            "Never Die Alone",
					Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
					Language:         tc.language,
					MetadataProvider: "googlebooks",
					Author:           &models.Author{Name: "Donald Goines"},
				},
			}
			agg := newTestAggregator(primary, google)

			got, err := agg.GetBookByISBN(context.Background(), "9780000000002")
			if err != nil {
				t.Fatalf("GetBookByISBN: %v", err)
			}
			if got == nil || got.ForeignID != "gb:never-die-alone" || got.MetadataProvider != "googlebooks" {
				t.Fatalf("got %+v, want original Google Books result", got)
			}
			if primary.getBookCalls != 0 {
				t.Fatalf("GetBook calls=%d ids=%v, want no German fallback canonical fetch", primary.getBookCalls, primary.gotBookIDs)
			}
			for _, query := range primary.searchBookQueries {
				if query == "Never Donald Goines" {
					t.Fatalf("generated German fallback query for English title: %v", primary.searchBookQueries)
				}
			}
		})
	}
}

func TestAggregator_GetBookByISBN_DoesNotApplyGermanFirstWordFallbackWithoutDescriptor(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Haus Jane Author": {{
				ForeignID: "OL-HAUS",
				Title:     "Haus",
				Author:    &models.Author{Name: "Jane Author"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL-HAUS": {
				ForeignID:        "OL-HAUS",
				Title:            "Haus",
				Description:      "Wrong OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:haus-der-sonne",
			Title:            "Haus der Sonne",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			Language:         "ger",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000010")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "gb:haus-der-sonne" || got.MetadataProvider != "googlebooks" {
		t.Fatalf("got %+v, want original Google Books result", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no German first-word canonical fetch", primary.getBookCalls, primary.gotBookIDs)
	}
	for _, query := range primary.searchBookQueries {
		if query == "Haus Jane Author" || query == "title:Haus author:Jane Author" {
			t.Fatalf("generated unsafe German first-word query: %v", primary.searchBookQueries)
		}
	}
}

func TestAggregator_GetBookByISBN_DoesNotCanonicalizeRealTrailingDescriptorWord(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9780000000009": nil,
			"Data Jane Author": {{
				ForeignID: "OL-DATA",
				Title:     "Data",
				Author:    &models.Author{Name: "Jane Author"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL-DATA": {
				ForeignID:        "OL-DATA",
				Title:            "Data",
				Description:      "Wrong OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:data-science",
			Title:            "Data Science",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000009")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "gb:data-science" || got.MetadataProvider != "googlebooks" {
		t.Fatalf("got %+v, want original Google Books result", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no unsafe trailing-word canonical fetch", primary.getBookCalls, primary.gotBookIDs)
	}
	for _, query := range primary.searchBookQueries {
		if query == "Data Jane Author" || query == "title:Data author:Jane Author" {
			t.Fatalf("generated unsafe trailing-word query: %v", primary.searchBookQueries)
		}
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesPrimaryOpenLibraryDuplicateWork(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{
			name: "openlibrary",
			getByISBN: &models.Book{
				ForeignID:        "OL26431102W",
				Title:            "Dune – Der Wüstenplanet",
				Description:      "Duplicate OpenLibrary work description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert", ForeignID: "OL79034A", MetadataProvider: "openlibrary"},
			},
			searchBooksByQuery: map[string][]models.Book{
				"isbn:9783453321229": {{
					ForeignID: "OL26431102W",
					Title:     "Dune – Der Wüstenplanet",
					Author:    &models.Author{Name: "Frank Herbert"},
				}},
				"Dune – Der Wüstenplanet Frank Herbert": {{
					ForeignID: "OL26431102W",
					Title:     "Dune – Der Wüstenplanet",
					Author:    &models.Author{Name: "Frank Herbert"},
				}},
			},
			getBook: &models.Book{
				ForeignID:        "OL893415W",
				Title:            "Dune",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
			authorWorks: []models.Book{
				{ForeignID: "OL26431102W", Title: "Dune – Der Wüstenplanet"},
				{ForeignID: "OL893415W", Title: "Dune"},
			},
		},
	}
	agg := newTestAggregator(primary)

	got, err := agg.GetBookByISBN(context.Background(), "9783453321229")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary work", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
	if len(primary.searchBookQueries) == 0 || primary.searchBookQueries[0] != "isbn:9783453321229" {
		t.Fatalf("first search query = %v, want isbn:9783453321229 first", primary.searchBookQueries)
	}
}

func TestAggregator_GetBookByISBN_ContinuesPrimaryFallbackForSecondaryCanonicalWork(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		getByISBN: &models.Book{
			ForeignID:        "OL24742012W",
			Title:            "DUNE",
			Description:      "Wrong OpenLibrary ISBN hit description long enough to avoid enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Brian Herbert"},
		},
		searchBooksByQuery: map[string][]models.Book{
			"isbn:9783453321229": {{
				ForeignID: "OL24742012W",
				Title:     "DUNE",
				Author:    &models.Author{Name: "Brian Herbert"},
			}},
			"Dune – Der Wüstenplanet: Roman Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"title:Dune – Der Wüstenplanet: Roman author:Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"Dune – Der Wüstenplanet Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"title:Dune – Der Wüstenplanet author:Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"Dune Frank Herbert": {
				{
					ForeignID:    "OL893415W",
					Title:        "Dune",
					EditionCount: 120,
					Author:       &models.Author{Name: "Frank Herbert"},
				},
				{
					ForeignID: "OL24742012W",
					Title:     "DUNE",
					Author:    &models.Author{Name: "Brian Herbert"},
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL893415W": {
				ForeignID:        "OL893415W",
				Title:            "Dune",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:1285431693",
			Title:            "Dune – Der Wüstenplanet: Roman",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			Language:         "ger",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453321229")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want secondary-canonicalized OpenLibrary work", got)
	}
	if primary.getByISBNCalls != 1 || dnb.getByISBNCalls != 1 {
		t.Fatalf("GetBookByISBN calls primary=%d dnb=%d, want 1/1", primary.getByISBNCalls, dnb.getByISBNCalls)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_ReusesCachedAuthorWorksForPrimaryCanonicalization(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{
			name: "openlibrary",
			getByISBNByISBN: map[string]*models.Book{
				"9783453321229": {
					ForeignID:        "OL-DUNE-DE",
					Title:            "Dune – Der Wüstenplanet",
					Description:      "Duplicate OpenLibrary work description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Frank Herbert", ForeignID: "OL79034A", MetadataProvider: "openlibrary"},
				},
				"9783453321243": {
					ForeignID:        "OL-MESSIAH-DE",
					Title:            "Dune Messiah – Der Herr des Wüstenplaneten",
					Description:      "Duplicate OpenLibrary work description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Frank Herbert", ForeignID: "OL79034A", MetadataProvider: "openlibrary"},
				},
			},
			searchBooksByQuery: map[string][]models.Book{
				"isbn:9783453321229": {{
					ForeignID: "OL-DUNE-DE",
					Title:     "Dune – Der Wüstenplanet",
					Author:    &models.Author{Name: "Frank Herbert"},
				}},
				"isbn:9783453321243": {{
					ForeignID: "OL-MESSIAH-DE",
					Title:     "Dune Messiah – Der Herr des Wüstenplaneten",
					Author:    &models.Author{Name: "Frank Herbert"},
				}},
			},
			getBookByID: map[string]*models.Book{
				"OL-DUNE": {
					ForeignID:        "OL-DUNE",
					Title:            "Dune",
					Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Frank Herbert"},
				},
				"OL-MESSIAH": {
					ForeignID:        "OL-MESSIAH",
					Title:            "Dune Messiah",
					Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Frank Herbert"},
				},
			},
			authorWorks: []models.Book{
				{ForeignID: "OL-DUNE-DE", Title: "Dune – Der Wüstenplanet"},
				{ForeignID: "OL-DUNE", Title: "Dune"},
				{ForeignID: "OL-MESSIAH-DE", Title: "Dune Messiah – Der Herr des Wüstenplaneten"},
				{ForeignID: "OL-MESSIAH", Title: "Dune Messiah"},
			},
		},
	}
	agg := newTestAggregator(primary)

	first, err := agg.GetBookByISBN(context.Background(), "9783453321229")
	if err != nil {
		t.Fatalf("GetBookByISBN first: %v", err)
	}
	second, err := agg.GetBookByISBN(context.Background(), "9783453321243")
	if err != nil {
		t.Fatalf("GetBookByISBN second: %v", err)
	}
	if first == nil || first.ForeignID != "OL-DUNE" || first.MetadataProvider != "openlibrary" {
		t.Fatalf("first = %+v, want OL-DUNE", first)
	}
	if second == nil || second.ForeignID != "OL-MESSIAH" || second.MetadataProvider != "openlibrary" {
		t.Fatalf("second = %+v, want OL-MESSIAH", second)
	}
	if primary.authorWorksCalls != 1 {
		t.Fatalf("author works calls = %d, want 1", primary.authorWorksCalls)
	}
	if primary.getBookCalls != 2 || !equalStringSlices(primary.gotBookIDs, []string{"OL-DUNE", "OL-MESSIAH"}) {
		t.Fatalf("GetBook calls=%d ids=%v, want OL-DUNE and OL-MESSIAH", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_AuthorWorksCanonicalizationDoesNotEnrichAllWorks(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{
			name: "openlibrary",
			getByISBN: &models.Book{
				ForeignID:        "OL-DUNE-DE",
				Title:            "Dune – Der Wüstenplanet",
				Description:      "Duplicate OpenLibrary work description long enough to avoid enrichment.",
				Language:         "ger",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert", ForeignID: "OL79034A", MetadataProvider: "openlibrary"},
			},
			getBookByID: map[string]*models.Book{
				"OL-DUNE": {
					ForeignID:        "OL-DUNE",
					Title:            "Dune",
					Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
					MetadataProvider: "openlibrary",
					Author:           &models.Author{Name: "Frank Herbert"},
				},
			},
			authorWorks: []models.Book{
				{ForeignID: "OL-DUNE-DE", Title: "Dune – Der Wüstenplanet"},
				{ForeignID: "OL-DUNE", Title: "Dune"},
				{ForeignID: "OL-MESSIAH", Title: "Dune Messiah"},
			},
		},
	}
	enricher := &mockProvider{
		name:        "googlebooks",
		searchBooks: []models.Book{{ImageURL: "https://books.google.com/cover.jpg"}},
	}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.GetBookByISBN(context.Background(), "9783453321229")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-DUNE" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary work", got)
	}
	if primary.authorWorksCalls != 1 {
		t.Fatalf("author works calls = %d, want 1", primary.authorWorksCalls)
	}
	if len(enricher.searchBookQueries) != 0 {
		t.Fatalf("enricher SearchBooks queries = %v, want none during author-works canonicalization", enricher.searchBookQueries)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesDNBNoisyTranslatedTitle(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Dune – Der Wüstenplanet: Roman Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"title:Dune – Der Wüstenplanet: Roman author:Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"Dune – Der Wüstenplanet Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"title:Dune – Der Wüstenplanet author:Frank Herbert": {{
				ForeignID: "OL26431102W",
				Title:     "Dune – Der Wüstenplanet",
				Author:    &models.Author{Name: "Frank Herbert"},
			}},
			"Dune Frank Herbert": {
				{
					ForeignID:    "OL893415W",
					Title:        "Dune",
					EditionCount: 120,
					Author:       &models.Author{Name: "Frank Herbert"},
				},
				{
					ForeignID:    "OL27969474W",
					Title:        "Dune",
					EditionCount: 1,
					Author:       &models.Author{Name: "Frank Herbert"},
				},
			},
		},
		getBook: &models.Book{
			ForeignID:        "OL893415W",
			Title:            "Dune",
			Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:1285431693",
			Title:            "Dune – Der Wüstenplanet: Roman",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			Language:         "ger",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453323131")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary work", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesGermanEditionTitleMatchToTopRankedWork(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Der Wüstenplanet Frank Herbert": {
				{
					ForeignID: "OL893415W",
					Title:     "Dune",
					Author:    &models.Author{Name: "Frank Herbert"},
					Editions: []models.Edition{{
						ForeignID: "OL32663508M",
						Title:     "Der Wüstenplanet.",
						Language:  "ger",
					}},
				},
				{ForeignID: "OL893502W", Title: "Heretics of Dune", Author: &models.Author{Name: "Frank Herbert"}},
				{
					ForeignID: "OL26431102W",
					Title:     "Dune – Der Wüstenplanet",
					Author:    &models.Author{Name: "Frank Herbert"},
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL893415W": {
				ForeignID:        "OL893415W",
				Title:            "Dune",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:1070335703",
			Title:            "Der Wüstenplanet",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			Language:         "ger",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Herbert, Frank"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453317178")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary edition-title match", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_UsesEditionTitleMatchWithoutSourceLanguage(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Der Wüstenplanet Frank Herbert": {{
				ForeignID: "OL893415W",
				Title:     "Dune",
				Author:    &models.Author{Name: "Frank Herbert"},
				Editions: []models.Edition{{
					ForeignID: "OL32663508M",
					Title:     "Der Wüstenplanet",
					Language:  "ger",
				}},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL893415W": {
				ForeignID:        "OL893415W",
				Title:            "Dune",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:1070335703",
			Title:            "Der Wüstenplanet",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453317178")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary edition-title match", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesDerivedTitleExactDuplicateByRank(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Dune Frank Herbert": {
				{ForeignID: "OL893415W", Title: "Dune", EditionCount: 120, Author: &models.Author{Name: "Frank Herbert"}},
				{ForeignID: "OL27969474W", Title: "Dune", EditionCount: 1, Author: &models.Author{Name: "Frank Herbert"}},
			},
		},
		getBook: &models.Book{
			ForeignID:        "OL893415W",
			Title:            "Dune",
			Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:1070335703",
			Title:            "Dune der Wüstenplanet Roman",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			Language:         "ger",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Herbert, Frank"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453317178")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL893415W" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want top-ranked exact OpenLibrary work", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL893415W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL893415W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsIndistinguishableExactTitleAuthorDuplicates(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Cien años de soledad Gabriel García Márquez": {
				{
					ForeignID: "OL274505W",
					Title:     "Cien años de soledad",
					Author:    &models.Author{Name: "Gabriel García Márquez"},
				},
				{
					ForeignID: "OL28027117W",
					Title:     "Cien Años de Soledad",
					Author:    &models.Author{Name: "Gabriel García Márquez"},
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL274505W": {
				ForeignID:        "OL274505W",
				Title:            "Cien años de soledad",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Gabriel García Márquez"},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "Cien años de soledad",
		Author: &models.Author{Name: "Gabriel García Márquez"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for indistinguishable duplicates")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no canonical fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookBilingualSegmentsBeatFullTitleDuplicate(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Cien años de soledad / One Hundred Years of Solitude Gabriel García Márquez": {{
				ForeignID:    "OL24213362W",
				Title:        "Cien años de soledad / One Hundred Years of Solitude",
				EditionCount: 1,
				Author:       &models.Author{Name: "Gabriel García Márquez"},
			}},
			"One Hundred Years of Solitude Gabriel García Márquez": {{
				ForeignID:    "OL274505W",
				Title:        "Cien años de soledad",
				EditionCount: 206,
				Author:       &models.Author{Name: "Gabriel García Márquez"},
				Editions: []models.Edition{{
					ForeignID: "OL30448691M",
					Title:     "One Hundred Years of Solitude",
					Language:  "eng",
				}},
			}},
			"Cien años de soledad Gabriel García Márquez": {{
				ForeignID:    "OL274505W",
				Title:        "Cien años de soledad",
				EditionCount: 206,
				Author:       &models.Author{Name: "Gabriel García Márquez"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL274505W": {
				ForeignID:        "OL274505W",
				Title:            "Cien años de soledad",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Gabriel García Márquez"},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "Cien años de soledad / One Hundred Years of Solitude",
		Author: &models.Author{Name: "Gabriel García Márquez"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL274505W" {
		t.Fatalf("got %+v, want canonical Cien años work", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL274505W" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL274505W", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookPrefersDominantCatalogSignalForMixedScriptTitle(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Awlād ḥāratinā (Arabic) أولاد حارتنا Naguib Mahfouz": {
				{
					ForeignID:    "OL43785192W",
					Title:        "Awlād ḥāratinā",
					EditionCount: 1,
					Author:       &models.Author{Name: "Naguib Mahfouz"},
				},
				{
					ForeignID:    "OL1599698W",
					Title:        "أولاد حارتنا",
					EditionCount: 31,
					Author:       &models.Author{Name: "Naguib Mahfouz"},
					Editions: []models.Edition{{
						ForeignID: "OL41514107M",
						Title:     "Awlād ḥāratinā",
					}},
				},
			},
			"أولاد حارتنا Naguib Mahfouz": {{
				ForeignID:    "OL1599698W",
				Title:        "أولاد حارتنا",
				EditionCount: 31,
				Author:       &models.Author{Name: "Naguib Mahfouz"},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL1599698W": {
				ForeignID:        "OL1599698W",
				Title:            "أولاد حارتنا",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Naguib Mahfouz"},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "Awlād ḥāratinā (Arabic) أولاد حارتنا",
		Author: &models.Author{Name: "Naguib Mahfouz"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL1599698W" {
		t.Fatalf("got %+v, want high-edition canonical OpenLibrary work", got)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsIndistinguishableTitleOnlyDuplicates(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"活着 Yu Hua": {
				{ForeignID: "OL25129388W", Title: "活着", EditionCount: 1, Author: &models.Author{Name: "Yu Hua"}},
				{ForeignID: "OL40693424W", Title: "活着", EditionCount: 1, Author: &models.Author{Name: "Yu Hua"}},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "活着",
		Author: &models.Author{Name: "Yu Hua"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for indistinguishable title-only duplicates")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no canonical fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsEditionExactDuplicateWorks(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"title:活着 author:余华": {
				{
					ForeignID:    "OL25129388W",
					Title:        "活着",
					EditionCount: 1,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL33426721M",
						Title:     "活着",
					}},
				},
				{
					ForeignID:    "OL20903102W",
					Title:        "活着",
					EditionCount: 1,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL28313256M",
						Title:     "活着",
					}},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "活着",
		Author: &models.Author{Name: "余华"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for edition-exact duplicate works")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no ambiguous primary fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsLowCountToLiveDuplicateWorks(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"title:To Live author:余华": {
				{
					ForeignID:    "OL15861449W",
					Title:        "To live",
					EditionCount: 1,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL24770141M",
						Title:     "To live",
					}},
				},
				{
					ForeignID:    "OL8036242W",
					Title:        "To Live",
					EditionCount: 2,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL8362701M",
						Title:     "To Live",
					}},
				},
				{
					ForeignID:    "OL25686018W",
					Title:        "To Live",
					EditionCount: 3,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL34497083M",
						Title:     "To Live",
					}},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "To Live",
		Author: &models.Author{Name: "余华"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for low-count duplicate works")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no ambiguous primary fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsLowCountToLiveTitleOnlyFallback(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"To Live Yu Hua": {
				{
					ForeignID:    "OL15861449W",
					Title:        "To live",
					EditionCount: 1,
					Author: &models.Author{
						Name:           "余华",
						AlternateNames: []string{"Yu Hua", "Hua Yu"},
					},
				},
				{
					ForeignID:    "OL8036242W",
					Title:        "To Live",
					EditionCount: 2,
					Author: &models.Author{
						Name:           "余华",
						AlternateNames: []string{"Yu Hua", "Hua Yu"},
					},
				},
			},
			"To Live": {{
				ForeignID:    "OL15861449W",
				Title:        "To live",
				EditionCount: 1,
				Author: &models.Author{
					Name:           "余华",
					AlternateNames: []string{"Yu Hua", "Hua Yu"},
				},
			}},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "To Live",
		Author: &models.Author{Name: "Yu Hua"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for low-count title-only duplicate fallback")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no low-count fallback fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsLowCountHuoZheDuplicateWorks(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"title:Huo Zhe author:余华": {
				{
					ForeignID:    "OL3240289W",
					Title:        "Huo zhe",
					EditionCount: 5,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL19977538M",
						Title:     "Huo zhe",
					}},
				},
				{
					ForeignID:    "OL10634244W",
					Title:        "Huo zhe",
					EditionCount: 1,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL21007264M",
						Title:     "Huo zhe.",
					}},
				},
				{
					ForeignID:    "OL19963794W",
					Title:        "Huo zhe",
					EditionCount: 1,
					Author:       &models.Author{Name: "余华"},
					Editions: []models.Edition{{
						ForeignID: "OL27144013M",
						Title:     "Huo zhe",
					}},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "Huo Zhe",
		Author: &models.Author{Name: "余华"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatal("canonicalPrimaryBook ok = true, want false for low-count duplicate works")
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no ambiguous primary fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_CanonicalPrimaryBookPrefersPrimaryAuthorOverAliasDuplicate(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"One Hundred Years of Solitude Gabriel Garcia Marquez": {
				{
					ForeignID: "OL274505W",
					Title:     "Cien años de soledad",
					Author:    &models.Author{Name: "Gabriel García Márquez"},
					Editions: []models.Edition{{
						ForeignID: "OL30448691M",
						Title:     "One Hundred Years of Solitude",
						Language:  "eng",
					}},
				},
				{
					ForeignID: "OL29355621W",
					Title:     "One Hundred Years of Solitude",
					Author: &models.Author{
						Name:           "Gregory Rabassa",
						AlternateNames: []string{"Gabriel Garcia Marquez", "Gabriel García Márquez"},
					},
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL274505W": {
				ForeignID:        "OL274505W",
				Title:            "Cien años de soledad",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Gabriel García Márquez"},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "One Hundred Years of Solitude",
		Author: &models.Author{Name: "Gabriel Garcia Marquez"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL274505W" {
		t.Fatalf("got %+v, want canonical Cien años work", got)
	}
}

func TestAggregator_CanonicalPrimaryBookRejectsCompilationBeforeSwappedAuthorQuery(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Three-Body Problem Liu Cixin": {{
				ForeignID: "OL44576333W",
				Title:     "Cixin Liu Bestselling Collecting Books Series, Set of 4 Books. the Three-Body Problem, the Wandering Earth, the Dark Forest and Death's End",
				Author:    &models.Author{Name: "Cixin Liu"},
			}},
			"The Three-Body Problem Cixin Liu": {{
				ForeignID: "OL17267881W",
				Title:     "三体",
				Author: &models.Author{
					Name:           "刘慈欣",
					AlternateNames: []string{"Liu Cixin", "Cixin Liu"},
				},
				Editions: []models.Edition{{
					ForeignID: "OL25840917M",
					Title:     "The Three-Body Problem",
					Language:  "eng",
				}},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL17267881W": {
				ForeignID:        "OL17267881W",
				Title:            "三体",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author: &models.Author{
					Name:           "刘慈欣",
					AlternateNames: []string{"Liu Cixin", "Cixin Liu"},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "The Three-Body Problem",
		Author: &models.Author{Name: "Liu Cixin"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL17267881W" {
		t.Fatalf("got %+v, want canonical Three-Body work", got)
	}
	if len(primary.searchBookQueries) < 3 || primary.searchBookQueries[2] != "The Three-Body Problem Cixin Liu" {
		t.Fatalf("search queries = %v, want swapped author query after rejecting compilation", primary.searchBookQueries)
	}
}

func TestAggregator_CanonicalPrimaryBookUsesSwappedEastAsianAuthorForCJKTitle(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"三体 Liu Cixin": {{
				ForeignID: "OL17267881W",
				Title:     "三体",
				Author: &models.Author{
					Name:           "刘慈欣",
					AlternateNames: []string{"Liu Cixin", "Cixin Liu"},
				},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL17267881W": {
				ForeignID:        "OL17267881W",
				Title:            "三体",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author: &models.Author{
					Name:           "刘慈欣",
					AlternateNames: []string{"Liu Cixin", "Cixin Liu"},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "三体",
		Author: &models.Author{Name: "Cixin Liu"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL17267881W" {
		t.Fatalf("got %+v, want canonical Three-Body work", got)
	}
}

func TestAggregator_CanonicalPrimaryBookMatchesEditionTitleWithAuthorAlias(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Master and Margarita Mikhail Bulgakov": {{
				ForeignID: "OL676009W",
				Title:     "Мастер и Маргарита",
				Author: &models.Author{
					Name:           "Михаил Афанасьевич Булгаков",
					AlternateNames: []string{"Mikhail Bulgakov", "Bulgakov, Mikhail"},
				},
				Editions: []models.Edition{{
					ForeignID: "OL1413669M",
					Title:     "The Master & Margarita",
					Language:  "eng",
				}},
			}},
		},
		getBookByID: map[string]*models.Book{
			"OL676009W": {
				ForeignID:        "OL676009W",
				Title:            "Мастер и Маргарита",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author: &models.Author{
					Name:           "Михаил Афанасьевич Булгаков",
					AlternateNames: []string{"Mikhail Bulgakov", "Bulgakov, Mikhail"},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "The Master and Margarita",
		Author: &models.Author{Name: "Mikhail Bulgakov"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL676009W" {
		t.Fatalf("got %+v, want canonical Master and Margarita work", got)
	}
}

func TestAggregator_CanonicalPrimaryBookUsesEditionArticleEquivalenceByRank(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Master and Margarita Mikhail Bulgakov": {
				{
					ForeignID: "OL676009W",
					Title:     "Мастер и Маргарита",
					Author: &models.Author{
						Name:           "Михаил Афанасьевич Булгаков",
						AlternateNames: []string{"Mikhail Bulgakov", "Bulgakov, Mikhail"},
					},
					Editions: []models.Edition{{
						ForeignID: "OL1413669M",
						Title:     "The Master & Margarita",
						Language:  "eng",
					}},
				},
				{
					ForeignID: "OL39795340W",
					Title:     "Master and Margarita",
					Author: &models.Author{
						Name:           "Михаил Афанасьевич Булгаков",
						AlternateNames: []string{"Mikhail Bulgakov", "Bulgakov, Mikhail"},
					},
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL676009W": {
				ForeignID:        "OL676009W",
				Title:            "Мастер и Маргарита",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author: &models.Author{
					Name:           "Михаил Афанасьевич Булгаков",
					AlternateNames: []string{"Mikhail Bulgakov", "Bulgakov, Mikhail"},
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "Master and Margarita",
		Author: &models.Author{Name: "Mikhail Bulgakov"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok {
		t.Fatal("canonicalPrimaryBook ok = false, want true")
	}
	if got == nil || got.ForeignID != "OL676009W" {
		t.Fatalf("got %+v, want canonical Master and Margarita work", got)
	}
}

func TestAggregator_GetBookByISBN_PrefersRightSubtitleBeforeLeftFallback(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Hunger Games: Catching Fire Suzanne Collins": {
				{ForeignID: "OL-HUNGER-GAMES", Title: "The Hunger Games", Author: &models.Author{Name: "Suzanne Collins"}},
			},
			"Catching Fire Suzanne Collins": {
				{ForeignID: "OL-CATCHING-FIRE", Title: "Catching Fire", Author: &models.Author{Name: "Suzanne Collins"}},
			},
			"The Hunger Games Suzanne Collins": {
				{ForeignID: "OL-HUNGER-GAMES", Title: "The Hunger Games", Author: &models.Author{Name: "Suzanne Collins"}},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-CATCHING-FIRE": {
				ForeignID:        "OL-CATCHING-FIRE",
				Title:            "Catching Fire",
				Description:      "OpenLibrary description long enough to avoid extra enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Suzanne Collins"},
			},
			"OL-HUNGER-GAMES": {
				ForeignID:        "OL-HUNGER-GAMES",
				Title:            "The Hunger Games",
				Description:      "OpenLibrary parent description long enough to avoid extra enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Suzanne Collins"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:catching-fire",
			Title:            "The Hunger Games: Catching Fire",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Suzanne Collins"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780439023498")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-CATCHING-FIRE" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want Catching Fire OpenLibrary work", got)
	}
	if !equalStringSlices(primary.gotBookIDs, []string{"OL-CATCHING-FIRE"}) {
		t.Fatalf("GetBook IDs = %v, want only OL-CATCHING-FIRE", primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_PreservesExactFullTitleOverSegmentFallback(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Foo: Bar Jane Author": {
				{ForeignID: "OL-FOO-BAR", Title: "Foo: Bar", Author: &models.Author{Name: "Jane Author"}},
			},
			"Bar Jane Author": {
				{ForeignID: "OL-BAR", Title: "Bar", Author: &models.Author{Name: "Jane Author"}},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-FOO-BAR": {
				ForeignID:        "OL-FOO-BAR",
				Title:            "Foo: Bar",
				Description:      "OpenLibrary exact full-title description long enough to avoid extra enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
			"OL-BAR": {
				ForeignID:        "OL-BAR",
				Title:            "Bar",
				Description:      "OpenLibrary segment-title description long enough to avoid extra enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Jane Author"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:foo-bar",
			Title:            "Foo: Bar",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Jane Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000011")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-FOO-BAR" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want exact full-title OpenLibrary work", got)
	}
	if !equalStringSlices(primary.gotBookIDs, []string{"OL-FOO-BAR"}) {
		t.Fatalf("GetBook IDs = %v, want only OL-FOO-BAR", primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_FallsBackToLeftTitleWhenRightSubtitleMisses(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Body Keeps the Score: Brain, Mind, and Body in the Healing of Trauma Bessel van der Kolk": {
				{ForeignID: "OL-BODY", Title: "The Body Keeps the Score", Author: &models.Author{Name: "Bessel van der Kolk"}},
			},
			"The Body Keeps the Score Bessel van der Kolk": {
				{ForeignID: "OL-BODY", Title: "The Body Keeps the Score", Author: &models.Author{Name: "Bessel van der Kolk"}},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-BODY": {
				ForeignID:        "OL-BODY",
				Title:            "The Body Keeps the Score",
				Description:      "OpenLibrary description long enough to avoid extra enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Bessel van der Kolk"},
			},
		},
	}
	hardcover := &mockProvider{
		name: "hardcover",
		getByISBN: &models.Book{
			ForeignID:        "hc:the-body-keeps-the-score",
			Title:            "The Body Keeps the Score: Brain, Mind, and Body in the Healing of Trauma",
			Description:      "Hardcover description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "hardcover",
			Author:           &models.Author{Name: "Bessel van der Kolk"},
		},
	}
	agg := newTestAggregator(primary, hardcover)

	got, err := agg.GetBookByISBN(context.Background(), "9780143127741")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-BODY" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want left-title OpenLibrary work", got)
	}
	if !equalStringSlices(primary.gotBookIDs, []string{"OL-BODY"}) {
		t.Fatalf("GetBook IDs = %v, want only OL-BODY", primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_DerivedPartialCandidateDoesNotBeatExactTitleCandidate(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Dune Frank Herbert": {
				{ForeignID: "OL-DUNE-MESSIAH", Title: "Dune Messiah", Author: &models.Author{Name: "Frank Herbert"}},
				{ForeignID: "OL-DUNE", Title: "Dune", Author: &models.Author{Name: "Frank Herbert"}},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-DUNE": {
				ForeignID:        "OL-DUNE",
				Title:            "Dune",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:dune-translated",
			Title:            "Dune – Der Wüstenplanet",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453317178")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-DUNE" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want exact title candidate over partial derived match", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL-DUNE" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL-DUNE", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_KeepsSecondaryWhenDerivedTitleHasOnlyPartialCandidates(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Dune Frank Herbert": {
				{ForeignID: "OL-DUNE-MESSIAH", Title: "Dune Messiah", Author: &models.Author{Name: "Frank Herbert"}},
				{ForeignID: "OL-HERETICS", Title: "Heretics of Dune", Author: &models.Author{Name: "Frank Herbert"}},
			},
		},
	}
	dnb := &mockProvider{
		name: "dnb",
		getByISBN: &models.Book{
			ForeignID:        "dnb:dune-translated",
			Title:            "Dune – Der Wüstenplanet",
			Description:      "DNB description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "dnb",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.GetBookByISBN(context.Background(), "9783453317178")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "dnb:dune-translated" || got.MetadataProvider != "dnb" {
		t.Fatalf("got %+v, want original DNB result when derived title has only partial primary matches", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no canonical primary fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_KeepsSecondaryWhenPrimaryOnlyFindsShorterParent(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"Dune Messiah Frank Herbert": {
				{ForeignID: "OL-DUNE", Title: "Dune", Author: &models.Author{Name: "Frank Herbert"}},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-DUNE": {
				ForeignID:        "OL-DUNE",
				Title:            "Dune",
				Description:      "Wrong OpenLibrary parent description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "Frank Herbert"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:dune-messiah",
			Title:            "Dune Messiah",
			Description:      "Google Books description long enough to avoid enrichment if canonicalization fails.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Frank Herbert"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780593098233")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "gb:dune-messiah" || got.MetadataProvider != "googlebooks" {
		t.Fatalf("got %+v, want original Google Books sequel result", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no canonical parent fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestWeakPartialTitleMatchRejectsShorterSubsetOnlyWithoutSegmentSeparator(t *testing.T) {
	if !weakPartialTitleMatch("Dune Messiah", "Dune") {
		t.Fatal("Dune Messiah vs Dune should be a weak partial match")
	}
	if weakPartialTitleMatch("The Body Keeps the Score: Brain, Mind, and Body in the Healing of Trauma", "The Body Keeps the Score") {
		t.Fatal("subtitle segment should allow shorter canonical title")
	}
	if weakPartialTitleMatch("Dune – Der Wüstenplanet", "Dune") {
		t.Fatal("translated segment should allow shorter canonical title")
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesSecondaryHitPrefersExactPrimaryTitle(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooks: []models.Book{
			{
				ForeignID: "OL-ANTHOLOGY",
				Title:     "The Martian / Artemis / Project Hail Mary",
				Author:    &models.Author{Name: "Andy Weir"},
			},
			{
				ForeignID: "OL-PHM",
				Title:     "Project Hail Mary",
				Author:    &models.Author{Name: "Andy Weir"},
			},
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
		t.Fatalf("got %+v, want exact-title OpenLibrary book", got)
	}
	if primary.getBookCalls != 1 || primary.gotBookIDs[0] != "OL-PHM" {
		t.Fatalf("GetBook calls=%d ids=%v, want OL-PHM", primary.getBookCalls, primary.gotBookIDs)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesSecondaryHitWithSubtitleAndCaseVariant(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooks: []models.Book{{
			ForeignID: "OL-BODY",
			Title:     "The Body Keeps the Score",
			Author:    &models.Author{Name: "Bessel van der Kolk"},
		}},
		getBook: &models.Book{
			ForeignID:        "OL-BODY",
			Title:            "The Body Keeps the Score",
			Description:      "OpenLibrary canonical description long enough to avoid extra enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Bessel van der Kolk"},
		},
	}
	hardcover := &mockProvider{
		name: "hardcover",
		getByISBN: &models.Book{
			ForeignID:        "hc:the-body-keeps-the-score",
			Title:            "THE BODY KEEPS THE SCORE: Brain, Mind, and Body in the Healing of Trauma",
			MetadataProvider: "hardcover",
			Author:           &models.Author{Name: "Bessel van der Kolk"},
		},
	}
	agg := newTestAggregator(primary, hardcover)

	got, err := agg.GetBookByISBN(context.Background(), "9780143127741")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-BODY" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary book", got)
	}
}

func TestAggregator_GetBookByISBN_CanonicalizesSecondaryHitWithAuthorPunctuationVariant(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooks: []models.Book{{
			ForeignID: "OL-LHOD",
			Title:     "The Left Hand of Darkness",
			Author:    &models.Author{Name: "Ursula K. Le Guin"},
		}},
		getBook: &models.Book{
			ForeignID:        "OL-LHOD",
			Title:            "The Left Hand of Darkness",
			Description:      "OpenLibrary canonical description long enough to avoid extra enrichment.",
			MetadataProvider: "openlibrary",
			Author:           &models.Author{Name: "Ursula K. Le Guin"},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:left-hand",
			Title:            "The Left Hand of Darkness",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "Ursula Le Guin"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780441478125")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "OL-LHOD" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("got %+v, want canonical OpenLibrary book", got)
	}
}

func TestAggregator_GetBookByISBN_KeepsSecondaryForIndistinguishablePrimaryDuplicates(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooks: []models.Book{
			{ForeignID: "OL1W", Title: "The Book", Author: &models.Author{Name: "A. Author"}},
			{ForeignID: "OL2W", Title: "The Book", Author: &models.Author{Name: "A. Author"}},
		},
		getBookByID: map[string]*models.Book{
			"OL1W": {
				ForeignID:        "OL1W",
				Title:            "The Book",
				Description:      "OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "A. Author"},
			},
		},
	}
	google := &mockProvider{
		name: "googlebooks",
		getByISBN: &models.Book{
			ForeignID:        "gb:ambiguous",
			Title:            "The Book",
			Description:      "Secondary provider description long enough to avoid enrichment.",
			MetadataProvider: "googlebooks",
			Author:           &models.Author{Name: "A. Author"},
		},
	}
	agg := newTestAggregator(primary, google)

	got, err := agg.GetBookByISBN(context.Background(), "9780000000007")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got == nil || got.ForeignID != "gb:ambiguous" || got.MetadataProvider != "googlebooks" {
		t.Fatalf("got %+v, want secondary provider result after ambiguous primary duplicates", got)
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("GetBook calls=%d ids=%v, want no ambiguous primary fetch", primary.getBookCalls, primary.gotBookIDs)
	}
}

// TestAggregator_CanonicalPrimaryBookDeclinesWhenRunnerUpIsClose covers the
// runner-up gap. compareBookCandidate is a lexicographic ordering, so before
// this it would pick a candidate one point of fuzzy title score ahead of
// another and report no ambiguity at all: a confident answer resting on
// nothing.
//
// Two provider results, neither an exact title, scoring 96 and 92. The titles
// are constructed rather than drawn from a real catalogue, because the window
// this rule adds is narrow and deliberately so: candidates that score EQUALLY
// were already declined as ambiguous, and edition variants of one work escape
// through the shared dedup key. What is left is the 1-to-4-point band, and
// that is what this pins.
//
// The right answer there is to decline. The book stays unmatched and can be
// matched by hand; attaching the wrong work's metadata to it is not something
// the user can undo by noticing later.
func TestAggregator_CanonicalPrimaryBookDeclinesWhenRunnerUpIsClose(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Silver Chair C. S. Lewis": {
				{
					ForeignID:    "OL-SILVER-A",
					Title:        "The Silver Chairs",
					Author:       &models.Author{Name: "C. S. Lewis"},
					EditionCount: 40,
				},
				{
					ForeignID:    "OL-SILVER-B",
					Title:        "The Silvor Chair",
					Author:       &models.Author{Name: "C. S. Lewis"},
					EditionCount: 40,
				},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "The Silver Chair",
		Author: &models.Author{Name: "C. S. Lewis"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if ok {
		t.Fatalf("canonicalPrimaryBook ok = true (got %+v), want false: neither candidate is far enough ahead of the other to be trusted", got)
	}
	if got != nil {
		t.Fatalf("got %+v, want no canonical match", got)
	}
}

// TestAggregator_CanonicalPrimaryBookAcceptsAClearWinner is the other side of
// the same rule, and the reason it is not simply "decline whenever there are
// two results". A candidate that is far enough clear of the runner-up is still
// accepted, so the gap narrows what gets auto-matched rather than stopping it.
func TestAggregator_CanonicalPrimaryBookAcceptsAClearWinner(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchBooksByQuery: map[string][]models.Book{
			"The Silver Chair C. S. Lewis": {
				{
					ForeignID:    "OL-SILVER-A",
					Title:        "The Silver Chair",
					Author:       &models.Author{Name: "C. S. Lewis"},
					EditionCount: 40,
				},
				{
					ForeignID:    "OL-SILVER-C",
					Title:        "The Silver Chair Companion Reader and Study Guide",
					Author:       &models.Author{Name: "C. S. Lewis"},
					EditionCount: 40,
				},
			},
		},
		getBookByID: map[string]*models.Book{
			"OL-SILVER-A": {
				ForeignID:        "OL-SILVER-A",
				Title:            "The Silver Chair",
				Description:      "Canonical OpenLibrary description long enough to avoid enrichment.",
				MetadataProvider: "openlibrary",
				Author:           &models.Author{Name: "C. S. Lewis"},
			},
		},
	}
	agg := newTestAggregator(primary)
	source := models.Book{
		Title:  "The Silver Chair",
		Author: &models.Author{Name: "C. S. Lewis"},
	}

	got, ok := agg.canonicalPrimaryBook(context.Background(), "", source)
	if !ok || got == nil {
		t.Fatal("canonicalPrimaryBook declined a candidate with an exact title and a distant runner-up")
	}
	if got.ForeignID != "OL-SILVER-A" {
		t.Fatalf("picked %q, want OL-SILVER-A", got.ForeignID)
	}
}
