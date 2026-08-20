package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// outcomeSearcherStub reports per-indexer outcomes alongside results, standing
// in for *indexer.Searcher's SearchBookWithOutcomes.
type outcomeSearcherStub struct {
	results  []newznab.SearchResult
	outcomes []indexer.IndexerDebug
	plainHit bool // set when the scheduler fell back to plain SearchBook
}

func (s *outcomeSearcherStub) SearchBook(context.Context, []models.Indexer, indexer.MatchCriteria) []newznab.SearchResult {
	s.plainHit = true
	return s.results
}

func (s *outcomeSearcherStub) SearchBookWithOutcomes(context.Context, []models.Indexer, indexer.MatchCriteria) ([]newznab.SearchResult, []indexer.IndexerDebug) {
	return s.results, s.outcomes
}

// plainSearcherStub implements only Searcher, like every stub written before
// #1936 and like any caller that has not been upgraded.
type plainSearcherStub struct{ called bool }

func (s *plainSearcherStub) SearchBook(context.Context, []models.Indexer, indexer.MatchCriteria) []newznab.SearchResult {
	s.called = true
	return nil
}

// TestSearchBookWithOutcomes_UsesOutcomesWhenAvailable and its sibling below
// pin the optional-capability wiring: the scheduler takes the richer method
// when the searcher has it, and still works when it does not.
func TestSearchBookWithOutcomes_UsesOutcomesWhenAvailable(t *testing.T) {
	stub := &outcomeSearcherStub{
		results:  []newznab.SearchResult{{Title: "hit"}},
		outcomes: []indexer.IndexerDebug{{IndexerName: "a"}, {IndexerName: "b", Error: "boom"}},
	}
	s := &Scheduler{searcher: stub}

	results, outcomes := s.searchBookWithOutcomes(context.Background(), nil, indexer.MatchCriteria{})

	if len(results) != 1 {
		t.Errorf("results = %d, want 1", len(results))
	}
	if len(outcomes) != 2 {
		t.Errorf("outcomes = %d, want 2", len(outcomes))
	}
	if stub.plainHit {
		t.Error("fell back to plain SearchBook despite the searcher reporting outcomes")
	}
}

func TestSearchBookWithOutcomes_FallsBackToPlainSearcher(t *testing.T) {
	stub := &plainSearcherStub{}
	s := &Scheduler{searcher: stub}

	_, outcomes := s.searchBookWithOutcomes(context.Background(), nil, indexer.MatchCriteria{})

	if !stub.called {
		t.Error("plain SearchBook was not called")
	}
	if outcomes != nil {
		t.Errorf("outcomes = %v, want nil for a searcher that cannot report them", outcomes)
	}
}

// TestDescribeIndexerFailures is the #1936 record itself: three indexers, two
// hard-failing, and the grab that results says so.
func TestDescribeIndexerFailures(t *testing.T) {
	outcomes := []indexer.IndexerDebug{
		{IndexerID: 1, IndexerName: "survivor", Enabled: true, ResultCount: 2},
		{IndexerID: 2, IndexerName: "suspended", Enabled: true, Error: "indexer error 101: Account suspended"},
		{IndexerID: 3, IndexerName: "flaky", Enabled: true, Error: "dial tcp: connection refused"},
	}
	payload := map[string]any{"guid": "g"}
	describeIndexerFailures(payload, indexer.HardFailures(outcomes), indexer.Attempted(outcomes))

	if got := payload["indexersFailed"]; got != "2 of 3" {
		t.Errorf("indexersFailed = %v, want \"2 of 3\"", got)
	}
	blob, err := json.Marshal(payload["indexerFailures"])
	if err != nil {
		t.Fatalf("marshal failures: %v", err)
	}
	for _, want := range []string{"suspended", "Account suspended", "flaky", "connection refused"} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("recorded failures do not mention %q: %s", want, blob)
		}
	}
	// The payload must survive a round trip, since that is how it is stored.
	if _, err := json.Marshal(payload); err != nil {
		t.Errorf("payload is not serialisable: %v", err)
	}
}

// TestDescribeIndexerFailures_HealthyPoolAddsNothing keeps the common case
// clean: a grab where everything answered carries no extra keys.
func TestDescribeIndexerFailures_HealthyPoolAddsNothing(t *testing.T) {
	outcomes := []indexer.IndexerDebug{
		{IndexerID: 1, IndexerName: "a", ResultCount: 1},
		{IndexerID: 2, IndexerName: "b", ResultCount: 0},
	}
	payload := map[string]any{"guid": "g"}
	describeIndexerFailures(payload, indexer.HardFailures(outcomes), indexer.Attempted(outcomes))

	if len(payload) != 1 {
		t.Errorf("payload gained keys for a healthy pool: %v", payload)
	}
}

// TestHardFailures_SkippedIndexersAreNotFailures: an indexer that was disabled,
// or benched by the #1934 rate-limit cooldown, was never contacted. Reporting it
// as a failure would flag a problem that is already being handled, and would
// make a normal install with one disabled indexer look permanently degraded.
func TestHardFailures_SkippedIndexersAreNotFailures(t *testing.T) {
	outcomes := []indexer.IndexerDebug{
		{IndexerID: 1, IndexerName: "survivor", Enabled: true, ResultCount: 1},
		{IndexerID: 2, IndexerName: "off", Enabled: false, Skipped: true, SkipReason: "disabled"},
		{IndexerID: 3, IndexerName: "benched", Enabled: true, Skipped: true, SkipReason: "rate limited, retrying in 8h0m0s"},
	}

	if failed := indexer.HardFailures(outcomes); len(failed) != 0 {
		t.Errorf("HardFailures = %+v, want none", failed)
	}
	if got := indexer.Attempted(outcomes); got != 1 {
		t.Errorf("Attempted = %d, want 1", got)
	}
}

// TestHardFailures_IgnoresStaleErrorOnSkippedEntry: a skipped entry can still
// carry the error that caused it to be benched. It was not attempted this time.
func TestHardFailures_IgnoresStaleErrorOnSkippedEntry(t *testing.T) {
	outcomes := []indexer.IndexerDebug{
		{IndexerName: "benched", Skipped: true, SkipReason: "rate limited", Error: "indexer error 500"},
	}
	if failed := indexer.HardFailures(outcomes); len(failed) != 0 {
		t.Errorf("HardFailures = %+v, want none", failed)
	}
}
