package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The stamp behind the wanted sweep's order (books.last_auto_search_at): what
// writes it and what must not. The sweep level behaviour is in
// wanted_sweep_fairness_test.go.

// What leaves the stamp alone: a search nothing was dispatched for, and a
// search whose context ended under it. Each of these books is still owed a
// search, so it must keep its place at the front of the queue.
func TestWantedSearchStamp_NotWrittenWithoutASearch(t *testing.T) {
	paceSweepForTest(t, 0)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	seedWantedForSweep(t, database, "Unstamped", 1)
	books := db.NewBookRepo(database)
	wantedBooks, err := books.ListByStatus(ctx, models.BookStatusWanted)
	if err != nil || len(wantedBooks) != 1 {
		t.Fatalf("seed: %d books, %v", len(wantedBooks), err)
	}
	book := wantedBooks[0]
	stampedAt := func() *time.Time {
		at, err := books.LastAutoSearchedAt(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}

	// The cap refuses the search: nothing was sent.
	refused := &allowanceSearcher{}
	sweepScheduler(database, refused).SearchAndGrabBook(ctx, book)
	if at := stampedAt(); at != nil {
		t.Fatalf("a search every indexer refused stamped the book at %v", at)
	}

	// The context ended under the search (a shutdown mid sweep).
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	open := &allowanceSearcher{}
	open.refill(5)
	sweepScheduler(database, open).SearchAndGrabBook(cancelled, book)
	if at := stampedAt(); at != nil {
		t.Fatalf("a search cancelled under the sweep stamped the book at %v", at)
	}

	// Auto-grab off: no search is dispatched at all.
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "autoGrab.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	sweepScheduler(database, open).SearchAndGrabBook(ctx, book)
	if at := stampedAt(); at != nil {
		t.Fatalf("a search skipped by the auto-grab switch stamped the book at %v", at)
	}
	if err := settings.Set(ctx, "autoGrab.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	// And the search that does reach an indexer stamps it, from any caller of
	// SearchAndGrabBook, so the sweep does not repeat an add or a Search click.
	started := time.Now().Add(-time.Second)
	sweepScheduler(database, open).SearchAndGrabBook(ctx, book)
	at := stampedAt()
	if at == nil || at.Before(started) {
		t.Fatalf("a search that reached an indexer left the stamp at %v", at)
	}
}

// A searcher that cannot say what each indexer did (every stub that only
// implements SearchBook) reads as having searched, so its books rotate
// instead of holding the front of the queue for good.
func TestWantedSearchStamp_SearcherWithoutOutcomesCountsAsSearched(t *testing.T) {
	paceSweepForTest(t, 0)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	seedWantedForSweep(t, database, "Plain", 2)
	books := db.NewBookRepo(database)

	sweepScheduler(database, plainSearcher{}).searchWanted()

	wantedBooks, err := books.ListByStatus(ctx, models.BookStatusWanted)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range wantedBooks {
		at, err := books.LastAutoSearchedAt(ctx, b.ID)
		if err != nil || at == nil {
			t.Errorf("%q: stamp = %v (%v), want a time", b.Title, at, err)
		}
	}
}

type plainSearcher struct{}

func (plainSearcher) SearchBook(context.Context, []models.Indexer, indexer.MatchCriteria) []newznab.SearchResult {
	return nil
}
