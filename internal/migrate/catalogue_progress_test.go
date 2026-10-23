package migrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func authorsN(n int) []*models.Author {
	out := make([]*models.Author, n)
	for i := range out {
		out[i] = &models.Author{ID: int64(i + 1)}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func zeroPace(t *testing.T) {
	old := catalogueFetchPaceInterval
	catalogueFetchPaceInterval = 0
	t.Cleanup(func() { catalogueFetchPaceInterval = old })
}

// The total is set at dispatch, before any fetch has finished, and the count
// climbs as each author's fetch returns.
func TestDispatchCatalogueFetch_CountsEachFinishedAuthor(t *testing.T) {
	zeroPace(t)
	var tr catalogueTracker
	ctx := withCatalogueTracker(context.Background(), &tr)

	release := make(chan struct{})
	dispatchCatalogueFetch(ctx, authorsN(3), func(*models.Author) { <-release })

	if got := tr.snapshot(); got.Total != 3 || got.Done != 0 || !got.Running {
		t.Fatalf("right after dispatch want total 3 done 0 running, got %+v", got)
	}
	close(release)
	waitFor(t, "backfill to finish", func() bool { return !tr.snapshot().Running })
	if got := tr.snapshot(); got.Total != 3 || got.Done != 3 {
		t.Fatalf("want 3 of 3, got %+v", got)
	}
}

// onFetch has no error return, so a failed fetch is one that logs and returns
// without doing the work. It still has to count as done.
func TestDispatchCatalogueFetch_EarlyReturnStillCounts(t *testing.T) {
	zeroPace(t)
	var tr catalogueTracker
	ctx := withCatalogueTracker(context.Background(), &tr)
	dispatchCatalogueFetch(ctx, authorsN(2), func(*models.Author) {})
	waitFor(t, "backfill to finish", func() bool { return !tr.snapshot().Running })
	if got := tr.snapshot(); got.Done != 2 || got.Total != 2 {
		t.Fatalf("want 2 of 2, got %+v", got)
	}
}

// A panicking fetch must not leave the counter hanging below total.
func TestCatalogueTracker_PanicStillCounts(t *testing.T) {
	var tr catalogueTracker
	tr.add(1)
	wrapped := tr.track(func(*models.Author) { panic("boom") })
	func() {
		defer func() { _ = recover() }()
		wrapped(&models.Author{})
	}()
	if got := tr.snapshot(); got.Done != 1 || got.Running {
		t.Fatalf("a panic must still count as done, got %+v", got)
	}
}

// No tracker on the context (the CLI, the CSV upload) keeps the old behaviour.
func TestDispatchCatalogueFetch_NoTrackerStillFetches(t *testing.T) {
	zeroPace(t)
	var wg sync.WaitGroup
	wg.Add(2)
	dispatchCatalogueFetch(context.Background(), authorsN(2), func(*models.Author) { wg.Done() })
	wg.Wait()
}

// The backfill outlives the import: counters stay readable through Progress
// after the import has finished, and the next import start resets them.
func TestReadarrImporter_ProgressCarriesCatalogueAndResets(t *testing.T) {
	zeroPace(t)
	imp := &ReadarrImporter{}
	ctx := withCatalogueTracker(context.Background(), &imp.catalogue)

	release := make(chan struct{})
	dispatchCatalogueFetch(ctx, authorsN(2), func(*models.Author) { <-release })

	// The import itself is not running, yet the backfill is.
	p := imp.Progress()
	if p.Running || !p.CatalogueRunning || p.CatalogueTotal != 2 || p.CatalogueDone != 0 {
		t.Fatalf("want import idle and catalogue 0 of 2 running, got %+v", p)
	}
	close(release)
	waitFor(t, "backfill to finish", func() bool { return !imp.Progress().CatalogueRunning })
	p = imp.Progress()
	if p.CatalogueTotal != 2 || p.CatalogueDone != 2 {
		t.Fatalf("finished counters must stay readable, got %+v", p)
	}

	// The next import start resets them.
	if err := imp.Start(context.Background(), t.TempDir()+"/missing.db"); err != nil {
		t.Fatal(err)
	}
	if p := imp.Progress(); p.CatalogueTotal != 0 || p.CatalogueDone != 0 || p.CatalogueRunning {
		t.Fatalf("Start must reset finished counters, got %+v", p)
	}
	waitFor(t, "import to finish", func() bool { return !imp.Progress().Running })
}

// A backfill still running when the next import starts keeps its counts and
// the new batch adds on top, so nothing already queued is forgotten.
func TestCatalogueTracker_ResetKeepsRunningBackfill(t *testing.T) {
	var tr catalogueTracker
	tr.add(3)
	tr.finish()
	tr.reset()
	if got := tr.snapshot(); got.Total != 3 || got.Done != 1 || !got.Running {
		t.Fatalf("reset must not drop a running backfill, got %+v", got)
	}
	tr.add(2)
	if got := tr.snapshot(); got.Total != 5 {
		t.Fatalf("want total 5, got %+v", got)
	}
}

// End to end through Start: the import reports done while the detached
// backfill is still working, and Progress shows the backfill's counters.
func TestReadarrImporter_StartExposesBackfillAfterImportDone(t *testing.T) {
	zeroPace(t)
	path := newReadarrDBWithAuthor(t, "Ursula Le Guin")
	database := newTestDB(t)
	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
	})
	release := make(chan struct{})
	imp := NewReadarrImporter(db.NewAuthorRepo(database), nil, nil, nil, nil, agg,
		func(*models.Author) { <-release })

	if err := imp.Start(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "import to finish", func() bool { return !imp.Progress().Running })
	p := imp.Progress()
	if p.Error != "" || !p.CatalogueRunning || p.CatalogueTotal != 1 || p.CatalogueDone != 0 {
		t.Fatalf("want import done, backfill 0 of 1 running, got %+v", p)
	}
	close(release)
	waitFor(t, "backfill to finish", func() bool { return !imp.Progress().CatalogueRunning })
	if p := imp.Progress(); p.CatalogueDone != 1 || p.CatalogueTotal != 1 {
		t.Fatalf("want 1 of 1, got %+v", p)
	}
}

func TestCatalogueTracker_ConcurrentRace(t *testing.T) {
	var tr catalogueTracker
	tr.add(200)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); tr.finish() }()
		go func() { defer wg.Done(); _ = tr.snapshot() }()
	}
	wg.Wait()
	if got := tr.snapshot(); got.Done != 200 || got.Running {
		t.Fatalf("got %+v", got)
	}
}
