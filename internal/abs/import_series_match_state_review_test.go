package abs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// scriptedSeriesProvider fails chosen searches and catalog fetches.
type scriptedSeriesProvider struct {
	*stubABSMetadataProvider
	failSearch  map[string]bool
	failCatalog map[string]bool
}

func (p scriptedSeriesProvider) SearchSeries(ctx context.Context, query string, limit int) ([]metadata.SeriesSearchResult, error) {
	if p.failSearch[query] {
		return nil, errors.New("429 try again")
	}
	return p.stubABSMetadataProvider.SearchSeries(ctx, query, limit)
}

func (p scriptedSeriesProvider) GetSeriesCatalog(ctx context.Context, foreignID string) (*metadata.SeriesCatalog, error) {
	if p.failCatalog[foreignID] {
		return nil, errors.New("catalog 500")
	}
	return p.stubABSMetadataProvider.GetSeriesCatalog(ctx, foreignID)
}

func sagaCatalog(foreignID string) *metadata.SeriesCatalog {
	book := func(id, title, pos string) metadata.SeriesCatalogBook {
		return metadata.SeriesCatalogBook{
			ForeignID: id, Title: title, Position: pos,
			Book: models.Book{ForeignID: id, Title: title, MetadataProvider: providerHardcover, Author: &models.Author{Name: "Ada Writer"}},
		}
	}
	return &metadata.SeriesCatalog{
		ForeignID: foreignID, ProviderID: foreignID, Title: "Saga Cycle", AuthorName: "Ada Writer",
		Books: []metadata.SeriesCatalogBook{book("hc:saga-alpha", "Saga Cycle Alpha", "1"), book("hc:saga-beta", "Saga Cycle Beta", "2")},
	}
}

func sagaItem(id, title, seq string) NormalizedLibraryItem {
	item := sampleABSItem()
	item.ItemID = id
	item.Title = title
	item.ASIN = ""
	item.Authors = []NormalizedAuthor{{ID: "author-ada", Name: "Ada Writer"}}
	item.Series = []NormalizedSeries{{ID: "abs-saga", Name: "Saga Cycle", Sequence: seq}}
	return item
}

func sagaProvider() *stubABSMetadataProvider {
	result := metadata.SeriesSearchResult{ForeignID: "hc-series:saga", ProviderID: "saga", Title: "Saga Cycle", AuthorName: "Ada Writer"}
	return hardcoverSeriesProvider("Saga Cycle", result, sagaCatalog("hc-series:saga"))
}

// A new volume joining a series a sibling already linked to Hardcover gets its
// ABS series membership before the Hardcover match runs. That membership is
// evidence about the sibling, not this item, so it must not be adopted as a
// match: the item still needs its Hardcover identity, position and merge.
func TestSeriesMatchState_NewSiblingInLinkedSeriesIsStillMatched(t *testing.T) {
	t.Parallel()
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	a := sagaItem("li-saga-a", "Saga Cycle Alpha", "1")
	b := sagaItem("li-saga-b", "Saga Cycle Beta", "2")

	runSeriesMatchImport(t, importer, sagaProvider(), a, nil)
	if state := requireSeriesMatchState(t, importer, a); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Fatalf("setup: state for A = %+v, want matched", state)
	}

	provider := sagaProvider()
	runSeriesMatchImport(t, importer, provider, b, nil)
	if provider.searchSeriesCalls != 1 {
		t.Fatalf("new sibling made %d searches, want 1", provider.searchSeriesCalls)
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	var found bool
	for _, bk := range books {
		if bk.ForeignID == "hc:saga-beta" && bk.MetadataProvider == providerHardcover {
			found = true
		}
	}
	if !found {
		t.Fatalf("books = %+v, want the new sibling relinked to hc:saga-beta", books)
	}
}

func TestSeriesMatchState_RecordsNothingWhenSeriesCannotBeLinked(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	result, catalog := seriesMatchCatalog()
	catalog.Title = "" // upsertHardcoverSeries cannot link a catalog with no title
	runSeriesMatchImport(t, importer, hardcoverSeriesProvider(item.Title, result, catalog), item, nil)
	if state := requireSeriesMatchState(t, importer, item); state != nil {
		t.Fatalf("unlinkable series recorded %+v, want no state", state)
	}
}

func ambiguousProvider(failQuery string) scriptedSeriesProvider {
	stub := &stubABSMetadataProvider{
		series: map[string][]metadata.SeriesSearchResult{
			"Saga Cycle": {
				{ForeignID: "hc-series:one", ProviderID: "one", Title: "Saga Cycle", AuthorName: "Ada Writer"},
				{ForeignID: "hc-series:two", ProviderID: "two", Title: "Saga Cycle", AuthorName: "Ada Writer"},
			},
		},
		catalogs: map[string]*metadata.SeriesCatalog{
			"hc-series:one": sagaCatalog("hc-series:one"),
			"hc-series:two": sagaCatalog("hc-series:two"),
		},
	}
	return scriptedSeriesProvider{stubABSMetadataProvider: stub, failSearch: map[string]bool{failQuery: true}}
}

func TestSeriesMatchState_AmbiguousResultIsRecordedOnlyWhenComplete(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)

	complete := sagaItem("li-ambiguous-ok", "Saga Cycle Alpha", "1")
	runSeriesMatchImport(t, importer, ambiguousProvider("no such query"), complete, nil)
	if state := requireSeriesMatchState(t, importer, complete); state == nil || state.Outcome != seriesMatchOutcomeMiss {
		t.Fatalf("complete ambiguous run state = %+v, want a recorded miss", state)
	}

	// Same ambiguity, but a second series query failed, so the answer is partial.
	partial := sagaItem("li-ambiguous-partial", "Saga Cycle Alpha", "1")
	partial.Series = append(partial.Series, NormalizedSeries{ID: "abs-other", Name: "Other Saga", Sequence: "1"})
	runSeriesMatchImport(t, importer, ambiguousProvider("Other Saga"), partial, nil)
	if state := requireSeriesMatchState(t, importer, partial); state != nil {
		t.Fatalf("partial ambiguous run recorded %+v, want no state", state)
	}
}

func TestSeriesMatchState_FailedCatalogFetchIsNotRecordedAsMiss(t *testing.T) {
	t.Parallel()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	result, catalog := seriesMatchCatalog()
	provider := scriptedSeriesProvider{
		stubABSMetadataProvider: hardcoverSeriesProvider(item.Title, result, catalog),
		failCatalog:             map[string]bool{result.ForeignID: true},
	}
	runSeriesMatchImport(t, importer, provider, item, nil)
	if state := requireSeriesMatchState(t, importer, item); state != nil {
		t.Fatalf("failed catalog fetch recorded %+v, want no state", state)
	}
}

func TestSeriesMatchInputHash_TracksEveryInput(t *testing.T) {
	t.Parallel()
	item := sagaItem("li-hash", "Saga Cycle Alpha", "1")
	hash := func(it NormalizedLibraryItem, author string, bookID int64) string {
		return seriesMatchInputHash(it, hardcoverSeriesQueries(it), author, bookID)
	}
	base := hash(item, "Ada Writer", 7)
	if hash(item, "Ada Writer", 7) != base {
		t.Fatal("hash is not stable for identical inputs")
	}
	otherSeq := item
	otherSeq.Series = []NormalizedSeries{{ID: "abs-saga", Name: "Saga Cycle", Sequence: "2"}}
	otherTitle := item
	otherTitle.Title = "Saga Cycle Alpha Revised" // queries unchanged, series is present
	cases := map[string]string{
		"resolved author": hash(item, "Someone Else", 7),
		"series sequence": hash(otherSeq, "Ada Writer", 7),
		"item title":      hash(otherTitle, "Ada Writer", 7),
		"book id":         hash(item, "Ada Writer", 8),
	}
	for name, got := range cases {
		if got == base {
			t.Fatalf("hash ignores the %s", name)
		}
	}
}

func TestSeriesMatchMissExpiry_OffsetIsDerivedFromHash(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"0000" + "aa": 0,
		"8000" + "aa": seriesMatchMissJitter / 2,
		"4000" + "aa": seriesMatchMissJitter / 4,
	}
	for hash, offset := range cases {
		if got, want := seriesMatchMissExpiry(at, hash), at.Add(seriesMatchMissTTL+offset); !got.Equal(want) {
			t.Fatalf("expiry for %q = %v, want %v", hash, got, want)
		}
	}
}
