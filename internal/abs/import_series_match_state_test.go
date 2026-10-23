package abs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// seriesMatchFixture is a matched Hardcover series for hardcoverSeriesABSItem.
func seriesMatchCatalog() (metadata.SeriesSearchResult, *metadata.SeriesCatalog) {
	result := metadata.SeriesSearchResult{
		ForeignID:  "hc-series:2173",
		ProviderID: "2173",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
	}
	catalog := &metadata.SeriesCatalog{
		ForeignID:  result.ForeignID,
		ProviderID: result.ProviderID,
		Title:      result.Title,
		AuthorName: result.AuthorName,
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:different-seasons",
			Title:     "Different Seasons",
			Position:  "1",
			Book: models.Book{
				ForeignID:        "hc:different-seasons",
				Title:            "Different Seasons",
				MetadataProvider: providerHardcover,
				Author:           &models.Author{Name: "Stephen King"},
			},
		}},
	}
	return result, catalog
}

// runSeriesMatchImport runs one import of item with a brand new aggregator, so
// the 24h search cache can never hide a repeat call: every provider call counted
// is a call a restarted or expired-cache Bindery would really make.
func runSeriesMatchImport(t *testing.T, importer *Importer, provider metadata.Provider, item NormalizedLibraryItem, mutate func(*ImportConfig)) {
	t.Helper()
	importer.WithMetadata(metadata.NewAggregator(provider))
	importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	if _, err := importer.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func matchedProvider(item NormalizedLibraryItem) *stubABSMetadataProvider {
	result, catalog := seriesMatchCatalog()
	return hardcoverSeriesProvider(item.Title, result, catalog)
}

func requireSeriesMatchState(t *testing.T, importer *Importer, item NormalizedLibraryItem) *models.ABSSeriesMatchState {
	t.Helper()
	state, err := importer.seriesMatchState.Get(context.Background(), DefaultSourceID, item.LibraryID, item.ItemID)
	if err != nil {
		t.Fatalf("Get state: %v", err)
	}
	return state
}

func TestSeriesMatchState_MatchedItemIsNotRequeriedOnSecondRun(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	first := matchedProvider(item)
	runSeriesMatchImport(t, importer, first, item, nil)
	if first.searchSeriesCalls != 1 {
		t.Fatalf("first run SearchSeries calls = %d, want 1", first.searchSeriesCalls)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Fatalf("state after match = %+v, want matched", state)
	}

	second := matchedProvider(item)
	runSeriesMatchImport(t, importer, second, item, nil)
	if second.searchSeriesCalls != 0 || len(second.catalogCalls) != 0 {
		t.Fatalf("second run made Hardcover calls: search=%d catalog=%v, want none", second.searchSeriesCalls, second.catalogCalls)
	}
}

func TestSeriesMatchState_RecordedMissIsNotRetriedUntilTTL(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	importer.seriesMatchClock = func() time.Time { return now }

	nothing := func() *stubABSMetadataProvider { return &stubABSMetadataProvider{} }

	first := nothing()
	runSeriesMatchImport(t, importer, first, item, nil)
	if first.searchSeriesCalls != 1 {
		t.Fatalf("first run SearchSeries calls = %d, want 1", first.searchSeriesCalls)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMiss {
		t.Fatalf("state after miss = %+v, want miss", state)
	}

	now = now.Add(seriesMatchMissTTL - time.Hour)
	inside := nothing()
	runSeriesMatchImport(t, importer, inside, item, nil)
	if inside.searchSeriesCalls != 0 {
		t.Fatalf("run inside TTL made %d searches, want 0", inside.searchSeriesCalls)
	}

	// Past the TTL plus the longest jitter, a miss is always due again.
	now = now.Add(time.Hour + seriesMatchMissJitter + time.Second)
	expired := nothing()
	runSeriesMatchImport(t, importer, expired, item, nil)
	if expired.searchSeriesCalls != 1 {
		t.Fatalf("run past TTL made %d searches, want 1", expired.searchSeriesCalls)
	}
}

func TestSeriesMatchState_ChangedItemRematches(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	// A recorded miss on the old title.
	runSeriesMatchImport(t, importer, &stubABSMetadataProvider{}, item, nil)

	// The title (the query for an item with no series) changes in ABS.
	renamed := item
	renamed.Title = "Different Seasons"
	provider := matchedProvider(renamed)
	runSeriesMatchImport(t, importer, provider, renamed, nil)
	if provider.searchSeriesCalls != 1 {
		t.Fatalf("renamed item made %d searches, want 1", provider.searchSeriesCalls)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Fatalf("state after rename = %+v, want matched", state)
	}

	// Now matched. Gaining an ABS series changes the queries, so it re-matches.
	withSeries := renamed
	withSeries.Series = []NormalizedSeries{{ID: "s1", Name: "Different Seasons", Sequence: "1"}}
	again := matchedProvider(renamed)
	again.series = map[string][]metadata.SeriesSearchResult{"Different Seasons": again.series[renamed.Title]}
	runSeriesMatchImport(t, importer, again, withSeries, nil)
	if again.searchSeriesCalls != 1 {
		t.Fatalf("item with new series made %d searches, want 1", again.searchSeriesCalls)
	}
}

func TestSeriesMatchState_FullForcesRematch(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	runSeriesMatchImport(t, importer, matchedProvider(item), item, nil)
	skipped := matchedProvider(item)
	runSeriesMatchImport(t, importer, skipped, item, nil)
	if skipped.searchSeriesCalls != 0 {
		t.Fatalf("setup: unchanged run made %d searches, want 0", skipped.searchSeriesCalls)
	}

	forced := matchedProvider(item)
	runSeriesMatchImport(t, importer, forced, item, func(c *ImportConfig) { c.Full = true })
	if forced.searchSeriesCalls != 1 {
		t.Fatalf("full run made %d searches, want 1", forced.searchSeriesCalls)
	}
}

func TestSeriesMatchState_DryRunWritesNothing(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	runSeriesMatchImport(t, importer, matchedProvider(item), item, func(c *ImportConfig) { c.DryRun = true })
	if state := requireSeriesMatchState(t, importer, item); state != nil {
		t.Fatalf("dry run recorded %+v, want no state", state)
	}

	// Nothing was recorded, so the first real run still has to search.
	real := matchedProvider(item)
	runSeriesMatchImport(t, importer, real, item, nil)
	if real.searchSeriesCalls != 1 {
		t.Fatalf("real run after dry run made %d searches, want 1", real.searchSeriesCalls)
	}
}

type failingSeriesSearchProvider struct{ *stubABSMetadataProvider }

func (failingSeriesSearchProvider) SearchSeries(context.Context, string, int) ([]metadata.SeriesSearchResult, error) {
	return nil, errors.New("429 try again")
}

func TestSeriesMatchState_FailedSearchIsNotRecordedAsMiss(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	runSeriesMatchImport(t, importer, failingSeriesSearchProvider{&stubABSMetadataProvider{}}, item, nil)
	if state := requireSeriesMatchState(t, importer, item); state != nil {
		t.Fatalf("failed search recorded %+v, want no state so the next run retries", state)
	}
}

func TestSeriesMatchState_AdoptsMatchMadeBeforeStateWasRecorded(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()

	// An import from before this table existed: matched, nothing recorded.
	recorder := importer.seriesMatchState
	importer.seriesMatchState = nil
	runSeriesMatchImport(t, importer, matchedProvider(item), item, nil)
	importer.seriesMatchState = recorder
	if state := requireSeriesMatchState(t, importer, item); state != nil {
		t.Fatalf("setup recorded %+v, want none", state)
	}

	provider := matchedProvider(item)
	runSeriesMatchImport(t, importer, provider, item, nil)
	if provider.searchSeriesCalls != 0 {
		t.Fatalf("upgrade run made %d searches, want 0 for an already linked item", provider.searchSeriesCalls)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Fatalf("state after adoption = %+v, want matched", state)
	}
}

func TestSeriesMatchMissExpiry_StaysWithinJitterWindow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, hash := range []string{"0000", "ffff", "8000", "not hex", ""} {
		got := seriesMatchMissExpiry(at, hash).Sub(at)
		if got < seriesMatchMissTTL || got > seriesMatchMissTTL+seriesMatchMissJitter {
			t.Fatalf("expiry for %q = %v, want within [TTL, TTL+jitter]", hash, got)
		}
	}
}
