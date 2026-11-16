package abs

import (
	"context"
	"testing"
)

// Merging the two local series an item was ambiguous between is enough for
// the next normal run to link it: the merge makes the ambiguous outcome due,
// so nobody has to know to run a full import (#2173).
func TestSeriesMatchState_MergeRetriesAmbiguousOnNextRun(t *testing.T) {
	t.Parallel()
	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	keep := createSeriesTwin(t, importer, "calibre:series:different-seasons", "Different Seasons")
	twin := createSeriesTwin(t, importer, "abs:series:lib-other:li-other", "Different Seasons")

	runSeriesMatchImport(t, importer, matchedProvider(item), item, nil)
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeAmbiguous {
		t.Fatalf("state = %+v, want ambiguous", state)
	}

	if _, err := seriesRepo.Merge(context.Background(), keep.ID, []int64{twin.ID}, ""); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	next := matchedProvider(item)
	stats := runSeriesMatchImportStats(t, importer, next, item, nil)
	if next.searchSeriesCalls != 1 {
		t.Fatalf("normal run after the merge made %d searches, want 1", next.searchSeriesCalls)
	}
	if stats.SeriesLinkFailed != 0 {
		t.Errorf("SeriesLinkFailed = %d, want 0", stats.SeriesLinkFailed)
	}
	if state := requireSeriesMatchState(t, importer, item); state == nil || state.Outcome != seriesMatchOutcomeMatched {
		t.Errorf("state after the retry = %+v, want matched", state)
	}
}
