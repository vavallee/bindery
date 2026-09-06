package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// fakeQuotaStore stands in for *db.IndexerRepo. Keeping it in memory is the
// point: the tests need to reload counts to prove they survive a restart, and a
// real database would only add setup.
type fakeQuotaStore struct {
	mu       sync.Mutex
	counts   map[int64]map[time.Time]int
	loads    int
	writes   int
	prunes   int
	loadErr  error
	writeErr error
}

func newFakeQuotaStore() *fakeQuotaStore {
	return &fakeQuotaStore{counts: make(map[int64]map[time.Time]int)}
}

func (f *fakeQuotaStore) LoadQueryCounts(_ context.Context, since time.Time) (map[int64]map[time.Time]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make(map[int64]map[time.Time]int)
	for id, buckets := range f.counts {
		for hour, n := range buckets {
			if hour.Before(since) {
				continue
			}
			if out[id] == nil {
				out[id] = make(map[time.Time]int)
			}
			out[id][hour] = n
		}
	}
	return out, nil
}

func (f *fakeQuotaStore) AddQueryCount(_ context.Context, id int64, hourStart time.Time, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.counts[id] == nil {
		f.counts[id] = make(map[time.Time]int)
	}
	f.counts[id][hourStart.UTC()] += delta
	return nil
}

func (f *fakeQuotaStore) PruneQueryCounts(_ context.Context, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes++
	for id, buckets := range f.counts {
		for hour := range buckets {
			if hour.Before(before) {
				delete(buckets, hour)
			}
		}
		if len(buckets) == 0 {
			delete(f.counts, id)
		}
	}
	return nil
}

func (f *fakeQuotaStore) total(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := 0
	for _, n := range f.counts[id] {
		sum += n
	}
	return sum
}

func (f *fakeQuotaStore) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

func (f *fakeQuotaStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func capped(limit int) models.Indexer {
	return models.Indexer{ID: 11, Name: "Budgeted Tracker", Enabled: true, DailyQueryLimit: &limit}
}

// emptyIndexer serves a valid but empty Newznab feed and counts the requests it
// received. Empty is deliberate: it makes BookSearch fall all the way through
// the tier cascade, which is the case a per-search cap check would overshoot on.
func emptyIndexer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(testRSSResponse))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newQuotaSearcher(store IndexerQuotaStore, now func() time.Time) *Searcher {
	s := newTestSearcher()
	s.quota = &indexerQuota{store: store, now: now}
	return s
}

// TestQuota_SkipsIndexerOnceCapIsSpent is the behavioural pin for #2312: after a
// sweep has spent the cap, the next search must not reach the indexer at all.
func TestQuota_SkipsIndexerOnceCapIsSpent(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(3)
	idx.URL = srv.URL

	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if got := hits.Load(); got != 3 {
		t.Fatalf("first search issued %d requests, want exactly the cap of 3", got)
	}

	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Messiah", Author: "Herbert"})
	if got := hits.Load(); got != 3 {
		t.Fatalf("second search reached a capped indexer: %d requests total, want 3", got)
	}
}

// TestQuota_CapIsExactMidCascade is why the budget is enforced in fetchXML
// rather than once per search. One BookSearch against an indexer that returns
// nothing walks four tiers, so a check made only at the top of the fan-out would
// let a cap of 2 spend 4.
func TestQuota_CapIsExactMidCascade(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(2)
	idx.URL = srv.URL

	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Frank Herbert"})
	if got := hits.Load(); got != 2 {
		t.Fatalf("cascade issued %d requests against a cap of 2", got)
	}
}

// TestQuota_UnsetLimitIsUnlimited pins the upgrade path: every existing indexer
// has a NULL cap and must behave exactly as it did before.
func TestQuota_UnsetLimitIsUnlimited(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := models.Indexer{ID: 12, Name: "Uncapped", Enabled: true, URL: srv.URL}
	// Distinct titles per search: the newznab client collapses byte-identical
	// query URLs for 90 s (#1814), so repeating one would measure the cache
	// rather than the cap.
	for _, title := range []string{"Dune", "Messiah", "Children"} {
		s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: title, Author: "Frank Herbert"})
	}
	if got := hits.Load(); got < 6 {
		t.Fatalf("an uncapped indexer was throttled: %d requests", got)
	}
	if store.writeCount() != 0 {
		t.Errorf("wrote counts for an indexer with no cap")
	}
}

// TestQuota_ZeroLimitIsUnlimited: typing 0 into the field is how a user clears
// it, and must not mean "never search this indexer again".
func TestQuota_ZeroLimitIsUnlimited(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(0)
	idx.URL = srv.URL
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if hits.Load() == 0 {
		t.Fatal("a zero limit blocked all searching")
	}
}

// TestQuota_NilStoreDisablesTheCap: an un-wired Searcher must ignore the field
// entirely, which is what keeps every caller that never calls WithQuota working.
func TestQuota_NilStoreDisablesTheCap(t *testing.T) {
	srv, hits := emptyIndexer(t)
	s := newTestSearcher()

	idx := capped(1)
	idx.URL = srv.URL
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if hits.Load() < 2 {
		t.Fatalf("an un-wired searcher enforced a cap: %d requests", hits.Load())
	}
}

// TestQuota_WindowRollsHourByHour is the difference between this and a fixed
// window: capacity comes back as the oldest bucket ages out, not all at once.
func TestQuota_WindowRollsHourByHour(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(2)
	idx.URL = srv.URL

	// Each leg searches a different title: the newznab client caches identical
	// query URLs for 90 s of wall-clock time (#1814), which the injected clock
	// does not advance, so reusing a title would measure the cache instead.
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Frank Herbert"})
	if got := hits.Load(); got != 2 {
		t.Fatalf("setup: %d requests, want 2", got)
	}

	// Still inside the window: nothing has freed up.
	now = now.Add(23 * time.Hour)
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Messiah", Author: "Frank Herbert"})
	if got := hits.Load(); got != 2 {
		t.Fatalf("capacity freed up early: %d requests", got)
	}

	// The bucket those two requests landed in has now aged out.
	now = now.Add(2 * time.Hour)
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Children of Dune", Author: "Frank Herbert"})
	if got := hits.Load(); got != 4 {
		t.Fatalf("capacity did not come back after the bucket aged out: %d requests", got)
	}
}

// TestQuota_CountSurvivesRestart is the reason the tally is persisted at all.
// The #1934 cooldown keeps its state in memory, which is fine for an hour and
// useless for a day: an install that restarts nightly would never accumulate a
// count and the cap would never bind.
func TestQuota_CountSurvivesRestart(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	idx := capped(3)
	idx.URL = srv.URL

	first := newQuotaSearcher(store, clock)
	first.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if got := store.total(idx.ID); got != 3 {
		t.Fatalf("persisted %d requests, want 3", got)
	}

	// A fresh process reads the stored buckets and must still consider the cap
	// spent.
	restarted := newQuotaSearcher(store, clock)
	restarted.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if got := hits.Load(); got != 3 {
		t.Fatalf("a restarted searcher spent the cap again: %d requests total", got)
	}
}

// TestQuota_ExhaustedBudgetIsNotAnIndexerFailure is the trap this design has to
// avoid. Capping an indexer is Bindery's decision, and routing it through the
// health store would leave a working indexer showing "daily query cap reached"
// in Settings as though the tracker had rejected us.
func TestQuota_ExhaustedBudgetIsNotAnIndexerFailure(t *testing.T) {
	srv, _ := emptyIndexer(t)
	store := newFakeQuotaStore()
	health := &fakeHealthStore{}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	s := newQuotaSearcher(store, func() time.Time { return now })
	s.health = newTestHealth(health, nil, func() time.Time { return now })

	idx := capped(1)
	idx.URL = srv.URL

	// A cap of 1 against an empty feed runs out mid-cascade, which is the path
	// that reaches the error branch rather than the skip gate.
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Frank Herbert"})

	failures, _ := health.snapshot()
	if len(failures) != 0 {
		t.Fatalf("running out of budget recorded an indexer health failure: %+v", failures)
	}
	if _, held := s.cooldownActive(idx); held {
		t.Error("running out of budget put the indexer into a rate-limit cooldown")
	}
}

// TestQuota_SkipReasonNamesTheCap: the interactive panel renders this string, so
// it has to say what happened and when it clears.
func TestQuota_SkipReasonNamesTheCap(t *testing.T) {
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	q := &indexerQuota{store: store, now: func() time.Time { return now }}

	idx := capped(2)
	budget := q.budget(idx)
	for i := range 2 {
		if !budget.Take() {
			t.Fatalf("budget refused request %d, which is inside the cap of 2", i+1)
		}
	}
	reason, held := q.hold(context.Background(), idx)
	if !held {
		t.Fatal("cap not held after spending it")
	}
	for _, want := range []string{"daily query cap reached", "2 of 2", "frees up in"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
}

// TestQuota_LoadFailureDoesNotBlockSearching: a read error must not turn into a
// refusal to search. Overspending the cap once is a far better failure than an
// install that silently stops looking for books.
func TestQuota_LoadFailureDoesNotBlockSearching(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	store.loadErr = errors.New("database is locked")
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(2)
	idx.URL = srv.URL
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	if hits.Load() == 0 {
		t.Fatal("a failed count load stopped the search entirely")
	}
}

// TestQuota_SearchQueryHonoursTheCap: the freeform /search page is a second
// fan-out with its own loop, and the cooldown had to be added to both. The cap
// is checked there too, deliberately: a user hammering /search can spend a
// tracker's allowance just as effectively as a sweep.
func TestQuota_SearchQueryHonoursTheCap(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(1)
	idx.URL = srv.URL

	s.SearchQuery(context.Background(), []models.Indexer{idx}, "dune")
	s.SearchQuery(context.Background(), []models.Indexer{idx}, "messiah")
	if got := hits.Load(); got != 1 {
		t.Fatalf("freeform search ignored the cap: %d requests, want 1", got)
	}
}

// TestQuota_DebugSearchReportsTheSkip: SearchBookWithDebug is the production
// book path and the one that feeds the interactive panel, so a capped indexer
// has to appear there as skipped-with-a-reason rather than vanish.
func TestQuota_DebugSearchReportsTheSkip(t *testing.T) {
	srv, _ := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(1)
	idx.URL = srv.URL

	s.SearchBookWithDebug(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Herbert"})
	_, dbg := s.SearchBookWithDebug(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Messiah", Author: "Herbert"})

	if len(dbg.Indexers) != 1 {
		t.Fatalf("expected one indexer entry, got %d", len(dbg.Indexers))
	}
	entry := dbg.Indexers[0]
	if !entry.Skipped {
		t.Fatalf("capped indexer was not reported as skipped: %+v", entry)
	}
	if entry.Error != "" {
		t.Errorf("capped indexer reported as an error rather than a skip: %q", entry.Error)
	}
	if !strings.Contains(entry.SkipReason, "daily query cap") {
		t.Errorf("skip reason %q does not name the cap", entry.SkipReason)
	}
}

// TestQuota_BudgetExhaustedIsNotAHardIndexerError pins the classification the
// error branches depend on. IsHardIndexerError means "the indexer rejected us";
// folding our own budget into it would start cooldowns and write health rows.
func TestQuota_BudgetExhaustedIsNotAHardIndexerError(t *testing.T) {
	if newznab.IsHardIndexerError(newznab.ErrQueryBudgetExhausted) {
		t.Error("ErrQueryBudgetExhausted classifies as an indexer rejection")
	}
	if !budgetExhausted(newznab.ErrQueryBudgetExhausted) {
		t.Error("budgetExhausted did not recognise its own error")
	}
	if budgetExhausted(errors.New("connection refused")) {
		t.Error("budgetExhausted matched an unrelated error")
	}
}

// TestQuota_RetriesAFailedLoad: latching the "loaded" flag on the attempt
// rather than on success meant one transient read error disabled the cap for
// the whole life of the process, not "at worst once" as the comment claimed.
func TestQuota_RetriesAFailedLoad(t *testing.T) {
	srv, hits := emptyIndexer(t)
	store := newFakeQuotaStore()
	store.loadErr = errors.New("database is locked")
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	s := newQuotaSearcher(store, func() time.Time { return now })

	idx := capped(2)
	idx.URL = srv.URL

	// First search: the read fails, so the cap cannot be enforced and requests
	// go out. That much is deliberate.
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Frank Herbert"})
	spent := hits.Load()
	if spent == 0 {
		t.Fatal("a failed load stopped the search entirely")
	}

	// The store recovers. The next search must read it rather than run
	// uncapped forever.
	store.mu.Lock()
	store.loadErr = nil
	store.mu.Unlock()

	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Messiah", Author: "Frank Herbert"})
	if store.loadCount() < 2 {
		t.Fatalf("the failed load was never retried: %d load attempts", store.loadCount())
	}
	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Children of Dune", Author: "Frank Herbert"})
	if got := hits.Load(); got > spent+2 {
		t.Errorf("the cap stayed off after the store recovered: %d requests, want at most %d", got, spent+2)
	}
}

// TestQuota_FailedLoadDoesNotDestroyStoredCounts: writing the whole in-memory
// tally meant a process that had failed to read the stored buckets would
// overwrite them with its own small number, so counts an earlier run had
// legitimately recorded vanished from the database.
func TestQuota_FailedLoadDoesNotDestroyStoredCounts(t *testing.T) {
	srv, _ := emptyIndexer(t)
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 10, 5, 0, 0, time.UTC)

	// An earlier run recorded 900 requests, 400 of them in the current hour.
	store.counts[11] = map[time.Time]int{
		now.Add(-2 * time.Hour).Truncate(time.Hour): 500,
		now.Truncate(time.Hour):                     400,
	}
	store.loadErr = errors.New("database is locked")

	s := newQuotaSearcher(store, func() time.Time { return now })
	idx := capped(1000)
	idx.URL = srv.URL

	s.SearchBook(context.Background(), []models.Indexer{idx}, MatchCriteria{Title: "Dune", Author: "Frank Herbert"})

	if got := store.total(11); got < 900 {
		t.Fatalf("stored total fell to %d; the failed load destroyed counts an earlier run recorded", got)
	}
}

// TestQuota_PersistsAcrossAnHourBoundary: flushing only the current hour's
// bucket permanently lost whatever had accumulated in the previous one since
// the last write, and the loss was bounded by how long the indexer stayed idle
// rather than by the flush interval.
func TestQuota_PersistsAcrossAnHourBoundary(t *testing.T) {
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	q := &indexerQuota{store: store, now: func() time.Time { return now }}

	idx := capped(1000)
	budget := q.budget(idx)

	for range 10 {
		budget.Take()
	}
	q.flush(context.Background(), idx, true)

	// Five more inside the flush interval, so nothing is written for them yet.
	now = now.Add(40 * time.Second)
	for range 5 {
		budget.Take()
	}

	// The sweep crosses into the next hour and flushes there.
	now = now.Add(time.Hour)
	budget.Take()
	q.flush(context.Background(), idx, true)

	if got := store.total(idx.ID); got != 16 {
		t.Errorf("persisted %d of 16 requests; the previous hour's tail was dropped", got)
	}
}

// TestQuota_FlushWritesNoEmptyBuckets: a bucket with nothing in it must not
// produce a row, because freesUpLocked picks the oldest bucket regardless of
// its count and a zero row would push the estimate an hour late.
func TestQuota_FlushWritesNoEmptyBuckets(t *testing.T) {
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	q := &indexerQuota{store: store, now: func() time.Time { return now }}

	idx := capped(1000)
	budget := q.budget(idx)
	budget.Take()
	q.flush(context.Background(), idx, true)

	writesAfterFirst := store.writeCount()

	// A later hour with no traffic at all.
	now = now.Add(2 * time.Hour)
	q.flush(context.Background(), idx, true)

	if store.writeCount() != writesAfterFirst {
		t.Errorf("an hour with no requests still wrote a bucket")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for hour, n := range store.counts[idx.ID] {
		if n == 0 {
			t.Errorf("stored a zero-count bucket at %v", hour)
		}
	}
}

// TestQuota_FailedWriteIsRetried: a delta that could not be written has to stay
// owed, or a transient error would silently drop it.
func TestQuota_FailedWriteIsRetried(t *testing.T) {
	store := newFakeQuotaStore()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	q := &indexerQuota{store: store, now: func() time.Time { return now }}

	idx := capped(1000)
	budget := q.budget(idx)
	for range 4 {
		budget.Take()
	}

	store.mu.Lock()
	store.writeErr = errors.New("disk full")
	store.mu.Unlock()
	q.flush(context.Background(), idx, true)
	if got := store.total(idx.ID); got != 0 {
		t.Fatalf("setup: stored %d despite the write failing", got)
	}

	store.mu.Lock()
	store.writeErr = nil
	store.mu.Unlock()
	q.flush(context.Background(), idx, true)
	if got := store.total(idx.ID); got != 4 {
		t.Errorf("stored %d of 4 requests; the failed delta was not retried", got)
	}
}
