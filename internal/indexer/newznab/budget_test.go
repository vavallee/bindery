package newznab

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// countingBudget admits the first n requests and refuses the rest.
type countingBudget struct {
	remaining atomic.Int64
	taken     atomic.Int64
}

func newCountingBudget(n int) *countingBudget {
	b := &countingBudget{}
	b.remaining.Store(int64(n))
	return b
}

func (b *countingBudget) Take() bool {
	if b.remaining.Add(-1) < 0 {
		return false
	}
	b.taken.Add(1)
	return true
}

// TestQueryBudget_StopsTheTierCascade is the reason the budget is checked in
// fetchXML rather than once per search: a book search against an indexer that
// returns nothing walks four tiers, so a budget of two must cut it off after
// two requests rather than let the whole cascade run (#2312).
func TestQueryBudget_StopsTheTierCascade(t *testing.T) {
	rec := newQueryRecorder(t)
	c := testNew(rec.server.URL, "key")

	budget := newCountingBudget(2)
	ctx := WithQueryBudget(context.Background(), budget)

	_, err := c.BookSearch(ctx, "Dune", "Frank Herbert", []int{7020})
	if !errors.Is(err, ErrQueryBudgetExhausted) {
		t.Fatalf("BookSearch error = %v, want ErrQueryBudgetExhausted", err)
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("indexer received %d requests, want 2", got)
	}
}

// TestQueryBudget_NilBudgetIsUnrestricted pins the default: a context with no
// budget attached must behave exactly as it did before the cap existed.
func TestQueryBudget_NilBudgetIsUnrestricted(t *testing.T) {
	rec := newQueryRecorder(t)
	c := testNew(rec.server.URL, "key")

	if _, err := c.BookSearch(context.Background(), "Dune", "Frank Herbert", []int{7020}); err != nil {
		t.Fatalf("BookSearch with no budget failed: %v", err)
	}
	if rec.count() < 2 {
		t.Fatalf("an unbudgeted cascade issued only %d requests", rec.count())
	}
	if got := WithQueryBudget(context.Background(), nil); budgetFrom(got) != nil {
		t.Error("a nil budget was attached to the context")
	}
}

// TestQueryBudget_CachedQueriesAreFree: the query cache collapses identical
// URLs before they reach fetchXML (#1814), so a repeat that never leaves the
// process must not spend budget. Counting it would make the cap punish the
// optimisation that already reduces indexer load.
func TestQueryBudget_CachedQueriesAreFree(t *testing.T) {
	rec := newQueryRecorder(t)
	c := testNew(rec.server.URL, "key")

	budget := newCountingBudget(100)
	ctx := WithQueryBudget(context.Background(), budget)

	if _, err := c.Search(ctx, "dune", []int{7020}); err != nil {
		t.Fatalf("first search: %v", err)
	}
	first := budget.taken.Load()
	if _, err := c.Search(ctx, "dune", []int{7020}); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got := budget.taken.Load(); got != first {
		t.Errorf("a cache hit spent budget: %d taken, want %d", got, first)
	}
	if rec.count() != 1 {
		t.Errorf("indexer received %d requests, want 1", rec.count())
	}
}

// TestQueryBudget_ExhaustedIsNotAnIndexerRejection: callers branch on
// IsHardIndexerError to decide whether to record health and start a cooldown,
// and this error is Bindery refusing itself, not the indexer refusing Bindery.
func TestQueryBudget_ExhaustedIsNotAnIndexerRejection(t *testing.T) {
	if IsHardIndexerError(ErrQueryBudgetExhausted) {
		t.Error("ErrQueryBudgetExhausted classifies as a hard indexer error")
	}
	if IsAuthError(ErrQueryBudgetExhausted) || IsRateLimitError(ErrQueryBudgetExhausted) {
		t.Error("ErrQueryBudgetExhausted classifies as an indexer rejection")
	}
}
