package metadata

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAggregator_SearchAuthorCandidates_IncludesEnrichersAndSkipsUnconfigured(t *testing.T) {
	primary := &mockProvider{
		name: "openlibrary",
		searchAuthors: []models.Author{
			{ForeignID: "OL13200512A", Name: "Emilia Jae", MetadataProvider: "openlibrary"},
		},
	}
	hardcover := &mockProvider{
		name: "hardcover",
		searchAuthors: []models.Author{
			{ForeignID: "hc:emilia-jae", Name: "Emilia Jae", Description: "Fantasy author."},
		},
	}
	unconfigured := &mockProvider{name: "googlebooks", searchAuthErr: ErrProviderNotConfigured}
	agg := newTestAggregator(primary, hardcover, unconfigured)

	got, err := agg.SearchAuthorCandidates(context.Background(), "emilia jae")
	if err != nil {
		t.Fatalf("SearchAuthorCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want 2: %+v", len(got), got)
	}
	if got[0].ForeignID != "OL13200512A" || got[1].ForeignID != "hc:emilia-jae" {
		t.Fatalf("candidate order/ids = %+v", got)
	}
	if got[1].MetadataProvider != "hardcover" {
		t.Fatalf("hardcover provider default = %q, want hardcover", got[1].MetadataProvider)
	}
	if len(primary.searchAuthorQueries) != 1 || primary.searchAuthorQueries[0] != "emilia jae" {
		t.Fatalf("primary queries = %+v", primary.searchAuthorQueries)
	}
	if len(hardcover.searchAuthorQueries) != 1 || hardcover.searchAuthorQueries[0] != "emilia jae" {
		t.Fatalf("hardcover queries = %+v", hardcover.searchAuthorQueries)
	}
}

func TestAggregator_SearchAuthorCandidates_DeduplicatesForeignIDs(t *testing.T) {
	primary := &mockProvider{
		name:          "openlibrary",
		searchAuthors: []models.Author{{ForeignID: "OL1A", Name: "Author"}},
	}
	enricher := &mockProvider{
		name:          "openlibrary",
		searchAuthors: []models.Author{{ForeignID: "OL1A", Name: "Author"}},
	}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchAuthorCandidates(context.Background(), "author")
	if err != nil {
		t.Fatalf("SearchAuthorCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1: %+v", len(got), got)
	}
}
