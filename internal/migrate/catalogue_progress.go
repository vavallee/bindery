package migrate

import (
	"context"
	"sync"

	"github.com/vavallee/bindery/internal/models"
)

// CatalogueProgress is the state of the detached catalogue backfill that an
// import starts for the authors it added. It is held in memory only, so a
// restart loses it.
type CatalogueProgress struct {
	Running bool
	Total   int
	Done    int
}

// catalogueTracker counts a catalogue backfill. The backfill goroutine
// outlives the import that started it, so the counters are guarded by their
// own mutex and are not touched when the import itself finishes.
type catalogueTracker struct {
	mu    sync.Mutex
	total int
	done  int
}

// add registers n more authors for the backfill. Called synchronously at
// dispatch, so the total is visible before the import reports done.
func (t *catalogueTracker) add(n int) {
	t.mu.Lock()
	t.total += n
	t.mu.Unlock()
}

// finish records one author as handled, whether its fetch worked or not.
func (t *catalogueTracker) finish() {
	t.mu.Lock()
	t.done++
	t.mu.Unlock()
}

// reset zeroes the counters for a new import. A backfill still running from an
// earlier import is left alone: the new batch is added on top of it, so its
// remaining authors are never dropped from the count.
func (t *catalogueTracker) reset() {
	t.mu.Lock()
	if t.done >= t.total {
		t.total, t.done = 0, 0
	}
	t.mu.Unlock()
}

func (t *catalogueTracker) snapshot() CatalogueProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return CatalogueProgress{Running: t.done < t.total, Total: t.total, Done: t.done}
}

// track wraps onFetch so each call counts as done when it returns, and also
// when it panics (the deferred finish still runs).
func (t *catalogueTracker) track(onFetch func(*models.Author)) func(*models.Author) {
	return func(a *models.Author) {
		defer t.finish()
		onFetch(a)
	}
}

type catalogueTrackerKey struct{}

// withCatalogueTracker carries a tracker to dispatchCatalogueFetch without
// widening the signatures of ImportReadarr and ImportCSVAuthors. Callers that
// pass no tracker (the CLI, the CSV upload) get the old untracked behaviour.
func withCatalogueTracker(ctx context.Context, t *catalogueTracker) context.Context {
	return context.WithValue(ctx, catalogueTrackerKey{}, t)
}

func catalogueTrackerFrom(ctx context.Context) *catalogueTracker {
	t, _ := ctx.Value(catalogueTrackerKey{}).(*catalogueTracker)
	return t
}
