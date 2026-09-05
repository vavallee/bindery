package newznab

import (
	"context"
	"errors"
)

// QueryBudget is the caller's permission to issue one more request to this
// indexer. Take reports whether the request may go out and consumes one unit if
// it may (#2312).
//
// The budget is carried on the context rather than on the Client because clients
// are pooled per (baseURL, apiKey) by the indexer package's clientCache, so two
// indexer rows pointing at the same host share one *Client. A budget bound to
// the client would bill both rows to whichever one built it.
//
// Two such rows do still share that client's query cache, and a waiter on an
// in-flight entry receives the originator's result. So for rows with byte
// identical URL and key, a request can occasionally be attributed to the wrong
// budget, in either direction. The window is the microseconds a fetch is in
// flight, the rows have to be duplicated by hand (Prowlarr gives each indexer
// its own /N/api URL), and the worst outcome is one search returning nothing
// for one indexer, so this is left as-is rather than paid for with a cache
// keyed per indexer row.
type QueryBudget interface {
	Take() bool
}

// ErrQueryBudgetExhausted is returned instead of making a request once the
// budget is spent.
//
// Deliberately not classified by IsHardIndexerError: that predicate means "the
// indexer explicitly rejected this session", and callers use it to decide
// whether to record health or start a cooldown. This rejection is ours, and
// treating it as the indexer's would mark a perfectly healthy indexer as failing
// in Settings for the crime of being budgeted.
var ErrQueryBudgetExhausted = errors.New("indexer daily query cap reached")

type budgetCtxKey struct{}

// WithQueryBudget returns a context that bills every request made through it to
// b. A nil budget is unrestricted, which is what every caller that has not been
// wired up gets.
func WithQueryBudget(ctx context.Context, b QueryBudget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, budgetCtxKey{}, b)
}

// budgetFrom returns the budget attached to ctx, or nil when there is none.
func budgetFrom(ctx context.Context) QueryBudget {
	b, _ := ctx.Value(budgetCtxKey{}).(QueryBudget)
	return b
}
