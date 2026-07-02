package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestAggregator_GetAuthorWorks_WorksProvider(t *testing.T) {
	books := []models.Book{{Title: "Dune"}, {Title: "Dune Messiah"}}
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: books},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 works, got %d", len(got))
	}
	if got[0].Title != "Dune" {
		t.Errorf("first title: want 'Dune', got %q", got[0].Title)
	}
}

// TestAggregator_GetAuthorWorks_CallerCannotPoisonCache verifies that a caller
// mutating the returned slice cannot corrupt the shared cache: both the cache
// set and the cache hit must clone, matching rawPrimaryAuthorWorks.
func TestAggregator_GetAuthorWorks_CallerCannotPoisonCache(t *testing.T) {
	books := []models.Book{{Title: "Dune"}, {Title: "Dune Messiah"}}
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: books},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	first, err := agg.GetAuthorWorks(context.Background(), "OL777A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	// Poison the returned slice — must not bleed into the cached copy.
	first[0].Title = "POISONED"

	primary.authorWorks = nil // force the next call to come from cache
	second, err := agg.GetAuthorWorks(context.Background(), "OL777A")
	if err != nil {
		t.Fatalf("GetAuthorWorks (cache): %v", err)
	}
	if second[0].Title != "Dune" {
		t.Errorf("cache poisoned by caller mutation: got %q, want %q", second[0].Title, "Dune")
	}
}

func TestAggregator_GetAuthorWorks_Fallback(t *testing.T) {
	// Primary does not implement worksProvider → falls back to SearchBooks.
	books := []models.Book{{Title: "Foundation"}, {Title: "Foundation and Empire"}}
	primary := &mockProvider{name: "gb", searchBooks: books}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorks(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthorWorks (fallback): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 works from fallback, got %d", len(got))
	}
}

func TestAggregator_GetAuthorWorks_Cached(t *testing.T) {
	books := []models.Book{{Title: "Ender's Game"}}
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: books},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	_, _ = agg.GetAuthorWorks(context.Background(), "OL555A")
	primary.authorWorks = nil // clear; next call must hit cache

	got, err := agg.GetAuthorWorks(context.Background(), "OL555A")
	if err != nil {
		t.Fatalf("GetAuthorWorks (cache): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Ender's Game" {
		t.Errorf("cached works mismatch: %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_MergesSupplementalByTitle(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{
				ForeignID:        "hc:dune",
				Title:            "Dune",
				Description:      "A desert planet.",
				ImageURL:         "https://img/dune.jpg",
				AverageRating:    4.5,
				RatingsCount:     1000,
				MetadataProvider: "hardcover",
			},
			{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if hardcover.gotAuthorName != "Frank Herbert" {
		t.Fatalf("supplemental author name = %q", hardcover.gotAuthorName)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 merged works, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != "OL1W" || got[0].MetadataProvider != "openlibrary" {
		t.Fatalf("primary identity should win duplicate title: %+v", got[0])
	}
	if got[0].HardcoverForeignID != "hc:dune" {
		t.Fatalf("matched hardcover identity = %q, want hc:dune", got[0].HardcoverForeignID)
	}
	if got[0].ImageURL != "https://img/dune.jpg" || got[0].AverageRating != 4.5 || got[0].Description == "" {
		t.Fatalf("supplemental metadata was not merged: %+v", got[0])
	}
	if got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("supplemental-only book missing: %+v", got[1])
	}
	if got[1].HardcoverForeignID != "" {
		t.Fatalf("unmatched supplemental book should not carry matched hardcover identity: %+v", got[1])
	}
}

func TestMergeAuthorWorkMetadata_GenrePreferHardcover(t *testing.T) {
	// Hardcover supplement replaces OL subjects with its taxonomy.
	dst := models.Book{Genres: []string{"Fiction", "American literature"}}
	src := models.Book{MetadataProvider: "hardcover", Genres: []string{"Fantasy"}}
	mergeAuthorWorkMetadata(&dst, src)
	if want := []string{"Fantasy"}; !slices.Equal(dst.Genres, want) {
		t.Errorf("hardcover genres should replace: want %v, got %v", want, dst.Genres)
	}

	// Non-Hardcover supplement does not overwrite a non-empty genre list.
	dst2 := models.Book{Genres: []string{"Fiction"}}
	src2 := models.Book{MetadataProvider: "googlebooks", Genres: []string{"Fiction / Science Fiction / General"}}
	mergeAuthorWorkMetadata(&dst2, src2)
	if want := []string{"Fiction"}; !slices.Equal(dst2.Genres, want) {
		t.Errorf("non-hardcover must not overwrite: want %v, got %v", want, dst2.Genres)
	}

	// Non-Hardcover still fills an empty genre list (existing behaviour).
	dst3 := models.Book{}
	src3 := models.Book{MetadataProvider: "googlebooks", Genres: []string{"Biography"}}
	mergeAuthorWorkMetadata(&dst3, src3)
	if want := []string{"Biography"}; !slices.Equal(dst3.Genres, want) {
		t.Errorf("fill-empty should still apply: want %v, got %v", want, dst3.Genres)
	}
}

// TestAggregator_GetAuthorWorksForAuthor_PrunesCompilations verifies that works
// an enricher (Hardcover) flags as compilations are removed: the matching
// primary (OpenLibrary) "bundle" work is dropped, the compilation entry itself
// is never added, and genuine books are untouched.
func TestAggregator_GetAuthorWorksForAuthor_PrunesCompilations(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
			{ForeignID: "OL2W", Title: "The Ultimate Dune Omnibus", MetadataProvider: "openlibrary"},
			{ForeignID: "OL3W", Title: "Children of Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{ForeignID: "hc:dune", Title: "Dune", ImageURL: "https://img/dune.jpg", MetadataProvider: "hardcover"},
			{ForeignID: "hc:ultimate-dune", Title: "The Ultimate Dune Omnibus", MetadataProvider: "hardcover", IsCompilation: true},
			{ForeignID: "hc:dune-messiah", Title: "Dune Messiah", MetadataProvider: "hardcover"},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	titles := map[string]bool{}
	for _, b := range got {
		titles[b.Title] = true
	}
	if titles["The Ultimate Dune Omnibus"] {
		t.Errorf("compilation should have been pruned, got: %v", got)
	}
	for _, want := range []string{"Dune", "Children of Dune", "Dune Messiah"} {
		if !titles[want] {
			t.Errorf("expected %q to remain, got: %v", want, titles)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 works after pruning the omnibus, got %d: %+v", len(got), got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_PrunesTechnicalSameNameCollision(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Kingdom's Dawn", MetadataProvider: "openlibrary", Genres: []string{"Juvenile Fiction", "Christian life, fiction"}},
			{ForeignID: "OL2W", Title: "Kingdom's Hope", MetadataProvider: "openlibrary", Genres: []string{"Fiction, fantasy, general"}},
			{ForeignID: "OL3W", Title: "Rise of the Fallen", MetadataProvider: "openlibrary", Genres: []string{"Adventure and adventurers, fiction", "Christian life, fiction"}},
			{ForeignID: "OL4W", Title: "Software Defined Networks", MetadataProvider: "openlibrary", Genres: []string{
				"Computer networks",
				"Software-defined networking (Computer network technology)",
				"Professional, career & trade -> computer science -> networking",
				"Telecommunications",
			}},
		}},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL1480449A", Name: "Chuck Black"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	titles := map[string]bool{}
	for _, b := range got {
		titles[b.Title] = true
	}
	if titles["Software Defined Networks"] {
		t.Fatalf("technical same-name collision should be pruned, got %+v", got)
	}
	for _, want := range []string{"Kingdom's Dawn", "Kingdom's Hope", "Rise of the Fallen"} {
		if !titles[want] {
			t.Fatalf("expected %q to remain, got %+v", want, got)
		}
	}
}

func TestAggregator_GetAuthorWorksForAuthor_MergesSupplementalIntoFirstDuplicateTitle(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
			{ForeignID: "OL2W", Title: "Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{
				ForeignID:        "hc:dune",
				Title:            "Dune",
				ImageURL:         "https://img/dune.jpg",
				AverageRating:    4.5,
				MetadataProvider: "hardcover",
			},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected duplicate primary works to remain for downstream dedup, got %d: %+v", len(got), got)
	}
	if got[0].ImageURL != "https://img/dune.jpg" || got[0].AverageRating != 4.5 {
		t.Fatalf("first duplicate did not receive supplemental metadata: %+v", got[0])
	}
	if got[1].ImageURL != "" || got[1].AverageRating != 0 {
		t.Fatalf("supplemental metadata merged into later duplicate: %+v", got[1])
	}
}

func TestAggregator_GetAuthorWorksForAuthor_EnrichesMissingCoversAfterSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
			{ForeignID: "OL2W", Title: "Heretics of Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{ForeignID: "hc:dune", Title: "Dune", ImageURL: "https://img/dune.jpg", MetadataProvider: "hardcover"},
		},
	}
	google := &mockProvider{
		name:        "googlebooks",
		searchBooks: []models.Book{{Title: "Heretics of Dune", ImageURL: "https://books.google.com/heretics.jpg"}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover, google},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 works, got %d: %+v", len(got), got)
	}
	if got[0].ImageURL != "https://img/dune.jpg" {
		t.Fatalf("matched supplemental cover was not merged: %+v", got[0])
	}
	if got[1].ImageURL != "https://books.google.com/heretics.jpg" {
		t.Fatalf("missing cover was not enriched after supplement: %+v", got[1])
	}
}

func TestAggregator_GetAuthorWorksForAuthor_ContinuesWhenSupplementFails(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover"}}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: errors.New("hardcover unavailable"),
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary result after supplement failure, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_DoesNotCacheUnconfiguredSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: ErrProviderNotConfigured,
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary-only result, got %+v", got)
	}

	hardcover.authorWorksByNameErr = nil
	hardcover.authorWorksByName = []models.Book{{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"}}
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor after config: %v", err)
	}
	if hardcover.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("expected supplemental result after config, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_DoesNotCacheFailedSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: errors.New("hardcover unavailable"),
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary-only result, got %+v", got)
	}

	hardcover.authorWorksByNameErr = nil
	hardcover.authorWorksByName = []models.Book{{ForeignID: "hc:dune-messiah", Title: "Dune Messiah", MetadataProvider: "hardcover"}}
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor after recovery: %v", err)
	}
	if hardcover.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:dune-messiah" {
		t.Fatalf("expected supplemental result after recovery, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_CachesSuccessfulSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected merged works, got %+v", got)
	}

	hardcover.authorWorksByName = nil
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor cached: %v", err)
	}
	if hardcover.calls != 1 {
		t.Fatalf("supplement calls = %d, want 1", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("expected cached supplemental result, got %+v", got)
	}
}

// mockLangFillerProvider is a primary provider that can derive work-level
// languages by edition-sampling (the OpenLibrary capability behind #891).
type mockLangFillerProvider struct {
	mockProvider
	byForeignID map[string]string // foreignID -> derived language
	fillCalls   int
}

func (m *mockLangFillerProvider) FillMissingWorkLanguages(_ context.Context, books []models.Book) int {
	m.fillCalls++
	filled := 0
	for i := range books {
		if books[i].Language != "" {
			continue
		}
		if lang, ok := m.byForeignID[books[i].ForeignID]; ok && lang != "" {
			books[i].Language = lang
			filled++
		}
	}
	return filled
}

func TestAggregator_FillMissingAuthorWorkLanguages_DelegatesToPrimary(t *testing.T) {
	primary := &mockLangFillerProvider{
		mockProvider: mockProvider{name: "ol"},
		byForeignID:  map[string]string{"OLSPAW": "spa"},
	}
	agg := newTestAggregator(primary)

	books := []models.Book{
		{ForeignID: "OLENGW", Language: "eng"},
		{ForeignID: "OLSPAW"},
	}
	filled := agg.FillMissingAuthorWorkLanguages(context.Background(), books)
	if filled != 1 {
		t.Fatalf("expected 1 filled, got %d", filled)
	}
	if primary.fillCalls != 1 {
		t.Errorf("expected primary FillMissingWorkLanguages to be called once, got %d", primary.fillCalls)
	}
	if books[1].Language != "spa" {
		t.Errorf("expected OLSPAW language 'spa', got %q", books[1].Language)
	}
}

func TestAggregator_FillMissingAuthorWorkLanguages_NoOpWhenUnsupported(t *testing.T) {
	// A primary that lacks the capability must be a safe no-op.
	primary := &mockProvider{name: "plain"}
	agg := newTestAggregator(primary)

	books := []models.Book{{ForeignID: "OLW"}}
	if filled := agg.FillMissingAuthorWorkLanguages(context.Background(), books); filled != 0 {
		t.Errorf("expected 0 filled for provider without capability, got %d", filled)
	}
	if books[0].Language != "" {
		t.Errorf("language should be untouched, got %q", books[0].Language)
	}
}
