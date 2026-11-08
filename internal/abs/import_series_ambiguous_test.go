package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// runSeriesMatchImportStats is runSeriesMatchImport returning the run's stats.
func runSeriesMatchImportStats(t *testing.T, importer *Importer, provider metadata.Provider, item NormalizedLibraryItem, mutate func(*ImportConfig)) *ImportStats {
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
	stats, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return stats
}

func createSeriesTwin(t *testing.T, importer *Importer, foreignID, title string) *models.Series {
	t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := importer.series.CreateOrGet(context.Background(), s); err != nil {
		t.Fatalf("create series %s: %v", foreignID, err)
	}
	return s
}

// Two local series share the Hardcover series title and neither holds the
// item's book, so the link stays ambiguous. That outcome must be recorded, or
// every run asks Hardcover again for an item it can never link (#2173), and the
// run summary must count it.
func TestSeriesMatchState_AmbiguousLocalSeriesIsRecordedAndCounted(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	createSeriesTwin(t, importer, "calibre:series:different-seasons", "Different Seasons")
	createSeriesTwin(t, importer, "abs:series:lib-other:li-other", "Different Seasons")

	first := matchedProvider(item)
	stats := runSeriesMatchImportStats(t, importer, first, item, nil)
	if first.searchSeriesCalls != 1 {
		t.Fatalf("first run SearchSeries calls = %d, want 1", first.searchSeriesCalls)
	}
	if stats.SeriesLinkFailed != 1 {
		t.Errorf("first run SeriesLinkFailed = %d, want 1", stats.SeriesLinkFailed)
	}
	state := requireSeriesMatchState(t, importer, item)
	if state == nil || state.Outcome != seriesMatchOutcomeAmbiguous {
		t.Errorf("state after ambiguous link = %+v, want %q", state, seriesMatchOutcomeAmbiguous)
	}

	second := matchedProvider(item)
	runSeriesMatchImport(t, importer, second, item, nil)
	if second.searchSeriesCalls != 0 || len(second.catalogCalls) != 0 {
		t.Fatalf("second run made Hardcover calls: search=%d catalog=%v, want none", second.searchSeriesCalls, second.catalogCalls)
	}

	forced := matchedProvider(item)
	runSeriesMatchImport(t, importer, forced, item, func(c *ImportConfig) { c.Full = true })
	if forced.searchSeriesCalls != 1 {
		t.Fatalf("full run made %d searches, want 1", forced.searchSeriesCalls)
	}
}

// The usual shape on an install that ran the Calibre import before #3368: the
// ABS import created a series and linked the book, then Calibre created a
// second row with the same title. The book already belongs to one of them, so
// that row is the one Hardcover links to.
func TestHardcoverSeries_TwinTitlePrefersSeriesHoldingTheBook(t *testing.T) {
	t.Parallel()
	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	item := hardcoverSeriesABSItem()
	item.Title = "Different Seasons"
	item.Series = []NormalizedSeries{{ID: "s-different-seasons", Name: "Different Seasons", Sequence: "1"}}

	// Hardcover matching off: the ABS series row is created and holds the book.
	runSeriesMatchImport(t, importer, &stubABSMetadataProvider{}, item, nil)
	absSeries, err := seriesRepo.GetByForeignID(context.Background(), absForeignID("series", item.LibraryID, seriesExternalID(item.Series[0])))
	if err != nil || absSeries == nil {
		t.Fatalf("setup: ABS series = %+v, err %v", absSeries, err)
	}
	createSeriesTwin(t, importer, "calibre:series:different-seasons", "Different Seasons")

	enableHardcoverSeriesMatching(t, importer)
	provider := matchedProvider(item)
	provider.series = map[string][]metadata.SeriesSearchResult{"Different Seasons": provider.series[item.Title]}
	stats := runSeriesMatchImportStats(t, importer, provider, item, nil)
	if provider.searchSeriesCalls != 1 {
		t.Fatalf("SearchSeries calls = %d, want 1", provider.searchSeriesCalls)
	}
	if stats.SeriesLinkFailed != 0 {
		t.Fatalf("SeriesLinkFailed = %d, want 0", stats.SeriesLinkFailed)
	}
	linked, err := seriesRepo.GetByHardcoverSeriesID(context.Background(), "hc-series:2173")
	if err != nil {
		t.Fatalf("GetByHardcoverSeriesID: %v", err)
	}
	if linked == nil || linked.ID != absSeries.ID {
		t.Fatalf("Hardcover series linked to %+v, want the ABS series %d that holds the book", linked, absSeries.ID)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Fatalf("state = %+v, want matched", state)
	}
}
