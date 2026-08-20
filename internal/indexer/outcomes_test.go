package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestHardFailures covers the split auto-grab depends on (#1936): only an
// indexer that was actually asked and refused counts as a failure.
func TestHardFailures(t *testing.T) {
	outcomes := []IndexerDebug{
		{IndexerName: "answered", ResultCount: 3},
		{IndexerName: "answered-empty", ResultCount: 0},
		{IndexerName: "refused", Error: "indexer error 101: Account suspended"},
		{IndexerName: "unreachable", Error: "dial tcp: connection refused"},
		// Skipped entries were never contacted. One can still carry the error
		// that got it benched, which must not be counted a second time.
		{IndexerName: "disabled", Skipped: true, SkipReason: "disabled"},
		{IndexerName: "benched", Skipped: true, SkipReason: "rate limited", Error: "indexer error 500"},
	}

	failed := HardFailures(outcomes)
	if len(failed) != 2 {
		t.Fatalf("HardFailures returned %d, want 2: %+v", len(failed), failed)
	}
	for i, want := range []string{"refused", "unreachable"} {
		if failed[i].IndexerName != want {
			t.Errorf("failure %d = %q, want %q", i, failed[i].IndexerName, want)
		}
	}
	if got := HardFailures(nil); got != nil {
		t.Errorf("HardFailures(nil) = %+v, want nil", got)
	}
}

// TestAttempted counts the indexers a search actually reached out to, which is
// the denominator in "N of M indexers failed".
func TestAttempted(t *testing.T) {
	outcomes := []IndexerDebug{
		{IndexerName: "a"},
		{IndexerName: "b", Error: "boom"},
		{IndexerName: "c", Skipped: true, SkipReason: "disabled"},
	}
	if got := Attempted(outcomes); got != 2 {
		t.Errorf("Attempted = %d, want 2", got)
	}
	if got := Attempted(nil); got != 0 {
		t.Errorf("Attempted(nil) = %d, want 0", got)
	}
}

// TestSearchBookWithOutcomes_ReportsPerIndexerResults is the seam itself: the
// automatic grab path can now tell "nothing matched" from "we could not ask",
// which SearchBook cannot express because it returns results and no error.
func TestSearchBookWithOutcomes_ReportsPerIndexerResults(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel>
			<item><title>An Author - A Book EPUB</title><guid>g1</guid>
			<enclosure url="http://example.invalid/g1" length="1024"/></item>
		</channel></rss>`))
	}))
	defer ok.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()

	s := newTestSearcher()
	indexers := []models.Indexer{
		{ID: 1, Name: "good", URL: ok.URL, APIKey: "k", Enabled: true, Categories: []int{7020}},
		{ID: 2, Name: "broken", URL: broken.URL, APIKey: "k", Enabled: true, Categories: []int{7020}},
		{ID: 3, Name: "off", URL: ok.URL, APIKey: "k", Enabled: false, Categories: []int{7020}},
	}

	results, outcomes := s.SearchBookWithOutcomes(context.Background(), indexers,
		MatchCriteria{Title: "A Book", Author: "An Author"})

	if len(outcomes) != 3 {
		t.Fatalf("got %d outcomes, want one per indexer: %+v", len(outcomes), outcomes)
	}
	byName := map[string]IndexerDebug{}
	for _, o := range outcomes {
		byName[o.IndexerName] = o
	}
	if byName["broken"].Error == "" {
		t.Error("the failing indexer reported no error, which is the whole point of this method")
	}
	if !byName["off"].Skipped {
		t.Error("the disabled indexer was not reported as skipped")
	}
	if byName["good"].Error != "" {
		t.Errorf("the healthy indexer reported an error: %q", byName["good"].Error)
	}
	if len(HardFailures(outcomes)) != 1 {
		t.Errorf("HardFailures over a live search = %d, want 1", len(HardFailures(outcomes)))
	}
	if Attempted(outcomes) != 2 {
		t.Errorf("Attempted over a live search = %d, want 2", Attempted(outcomes))
	}
	// The results still come back exactly as SearchBook would have returned
	// them; this method adds information rather than changing behaviour.
	if len(results) == 0 {
		t.Error("the surviving indexer's results were lost")
	}
}
