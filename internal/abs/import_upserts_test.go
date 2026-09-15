package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestLookupUpstreamBook_SkipsProviderFlaggedCompanionMaterial pins the
// #2235 regression this file exists to close: before #2235,
// OpenLibrary's client dropped companion material (study guides, film
// tie-ins, etc.) before GetAuthorWorks ever returned it, so this title-match
// loop never saw one. #2235 changed that client to flag rather than drop —
// so ProviderNoiseSignal can count and report it in fetchAuthorBooks's own
// discovery pipeline instead of it vanishing invisibly — but
// lookupUpstreamBook never routes through filterengine at all; it title-
// matches raw GetAuthorWorks results directly. Without an explicit skip
// here, a flagged companion work can now win an ABS import's title match
// (or turn a previously-unambiguous match ambiguous), a regression this test
// would have caught before it shipped.
func TestLookupUpstreamBook_SkipsProviderFlaggedCompanionMaterial(t *testing.T) {
	author := &models.Author{ForeignID: "OL1A", Name: "Andy Weir"}

	provider := &stubABSMetadataProvider{
		works: map[string][]models.Book{
			"OL1A": {
				{
					ForeignID: "OL1W", Title: "Project Hail Mary Study Guide",
					Observations: []models.FilterObservation{
						{Signal: models.SignalProviderOpenLibraryNoise, Reason: "test fixture: companion material"},
					},
				},
			},
		},
	}

	importer := (&Importer{}).WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.Title = "Project Hail Mary Study Guide"
	item.ASIN = ""

	match, matchedBy, ambiguous, err := importer.lookupUpstreamBook(context.Background(), author, item)
	if err != nil {
		t.Fatalf("lookupUpstreamBook: %v", err)
	}
	if ambiguous {
		t.Error("ambiguous = true, want false (the only candidate is flagged noise and should be skipped, not counted)")
	}
	if match != nil {
		t.Errorf("match = %+v, want nil: a provider-flagged companion work must never be returned as an upstream match", match)
	}
	if matchedBy != "" {
		t.Errorf("matchedBy = %q, want \"\"", matchedBy)
	}
}

// TestLookupUpstreamBook_StillMatchesRealWorkAlongsideFlaggedNoise proves the
// skip in TestLookupUpstreamBook_SkipsProviderFlaggedCompanionMaterial is
// scoped to the flagged record only — a real, unflagged work with the exact
// item title still matches normally when a flagged companion work happens to
// share the author's catalogue.
func TestLookupUpstreamBook_StillMatchesRealWorkAlongsideFlaggedNoise(t *testing.T) {
	author := &models.Author{ForeignID: "OL1A", Name: "Andy Weir"}

	provider := &stubABSMetadataProvider{
		works: map[string][]models.Book{
			"OL1A": {
				{
					ForeignID: "OL1W-guide", Title: "Project Hail Mary",
					Observations: []models.FilterObservation{
						{Signal: models.SignalProviderOpenLibraryNoise, Reason: "test fixture: companion material"},
					},
				},
				{ForeignID: "OL1W-real", Title: "Project Hail Mary"},
			},
		},
		books: map[string]*models.Book{
			"OL1W-real": {ForeignID: "OL1W-real", Title: "Project Hail Mary"},
		},
	}

	importer := (&Importer{}).WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.ASIN = ""

	match, _, ambiguous, err := importer.lookupUpstreamBook(context.Background(), author, item)
	if err != nil {
		t.Fatalf("lookupUpstreamBook: %v", err)
	}
	if ambiguous {
		t.Fatal("ambiguous = true, want false: excluding the flagged duplicate should leave exactly one real candidate")
	}
	if match == nil || match.ForeignID != "OL1W-real" {
		t.Errorf("match = %+v, want the unflagged work OL1W-real", match)
	}
}
