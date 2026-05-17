package metadata

import (
	"context"
	"errors"
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
	if got[0].ImageURL != "https://img/dune.jpg" || got[0].AverageRating != 4.5 || got[0].Description == "" {
		t.Fatalf("supplemental metadata was not merged: %+v", got[0])
	}
	if got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("supplemental-only book missing: %+v", got[1])
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
