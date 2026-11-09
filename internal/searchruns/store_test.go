package searchruns

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestStore(max int, ttl time.Duration) (*Store, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	s := NewStore(max, ttl)
	s.now = clk.now
	return s, clk
}

func TestStartRegistersAQueuedRunWithARandomID(t *testing.T) {
	s, _ := newTestStore(10, time.Hour)
	a := s.Start(7, 42, "bulk")
	b := s.Start(7, 42, "bulk")
	if len(a) != 32 || a == b {
		t.Fatalf("want distinct 32 char hex ids, got %q and %q", a, b)
	}
	run, ok := s.Get(a, 0)
	if !ok {
		t.Fatal("a started run must be readable")
	}
	if run.State != StateQueued || run.BookID != 42 || run.Origin != "bulk" {
		t.Fatalf("unexpected run: %+v", run)
	}
}

func TestRunMovesThroughRunningToFinished(t *testing.T) {
	s, clk := newTestStore(10, time.Hour)
	id := s.Start(1, 5, "book")
	s.Running(id)
	if r, _ := s.Get(id, 0); r.State != StateRunning {
		t.Fatalf("want running, got %s", r.State)
	}
	clk.advance(2 * time.Second)
	s.Record(id, Result{Outcome: "grabbed", Indexers: 2, RawResults: 10, AfterFilters: 6, Approved: 3, ElapsedMs: 40,
		Grabbed: &Release{Title: "Dune", Indexer: "NZB", Protocol: "usenet", MediaType: "ebook", Size: 9}})
	s.Finish(id)
	r, _ := s.Get(id, 0)
	if r.State != StateFinished || r.Outcome != "grabbed" || r.Indexers != 2 || r.RawResults != 10 ||
		r.AfterFilters != 6 || r.Approved != 3 || r.Grabbed == nil || r.Grabbed.Title != "Dune" || r.FinishedAt == nil {
		t.Fatalf("unexpected finished run: %+v", r)
	}
}

func TestFinishWithoutAResultSaysNothingSearched(t *testing.T) {
	s, _ := newTestStore(10, time.Hour)
	id := s.Start(1, 5, "book")
	s.Finish(id)
	if r, _ := s.Get(id, 0); r.State != StateFinished || r.Outcome != OutcomeNotSearched {
		t.Fatalf("want finished/%q, got %+v", OutcomeNotSearched, r)
	}
}

// A book searched in two formats is one run: counts add up and a grab in
// either format is what the run reports.
func TestRecordMergesFormats(t *testing.T) {
	s, _ := newTestStore(10, time.Hour)
	id := s.Start(1, 5, "book")
	s.Record(id, Result{Outcome: "grabbed", RawResults: 4, Grabbed: &Release{Title: "E"}})
	s.Record(id, Result{Outcome: "no results", RawResults: 1})
	s.Finish(id)
	r, _ := s.Get(id, 0)
	if r.Outcome != "grabbed" || r.RawResults != 5 || r.Grabbed == nil || r.Grabbed.Title != "E" {
		t.Fatalf("unexpected merge: %+v", r)
	}
}

func TestCountBound(t *testing.T) {
	s, clk := newTestStore(3, time.Hour)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, s.Start(1, int64(i), "bulk"))
		clk.advance(time.Second)
	}
	if n := s.Len(); n != 3 {
		t.Fatalf("want 3 runs kept, got %d", n)
	}
	for _, id := range ids[:2] {
		if _, ok := s.Get(id, 0); ok {
			t.Errorf("oldest run %s should have been evicted", id)
		}
	}
	for _, id := range ids[2:] {
		if _, ok := s.Get(id, 0); !ok {
			t.Errorf("newest run %s should have been kept", id)
		}
	}
}

func TestAgeBoundCountsFromLastUpdate(t *testing.T) {
	s, clk := newTestStore(10, time.Hour)
	old := s.Start(1, 1, "bulk")
	touched := s.Start(1, 2, "bulk")
	clk.advance(50 * time.Minute)
	s.Running(touched) // a long queued bulk run is refreshed when it starts
	clk.advance(20 * time.Minute)
	if _, ok := s.Get(old, 0); ok {
		t.Error("a run idle for more than the ttl must read as unknown")
	}
	if _, ok := s.Get(touched, 0); !ok {
		t.Error("a recently updated run must survive")
	}
	// Pruned on write, not by a goroutine.
	s.Start(1, 3, "bulk")
	if n := s.Len(); n != 2 {
		t.Fatalf("expired run must be dropped on the next write, have %d", n)
	}
}

func TestOwnerScoping(t *testing.T) {
	s, _ := newTestStore(10, time.Hour)
	mine := s.Start(1, 1, "book")
	theirs := s.Start(2, 1, "book")
	unowned := s.Start(0, 1, "scheduled")

	if _, ok := s.Get(mine, 1); !ok {
		t.Error("an owner reads their own run")
	}
	if _, ok := s.Get(theirs, 1); ok {
		t.Error("another user's run must read as unknown")
	}
	if _, ok := s.Get(unowned, 1); ok {
		t.Error("an unowned run is admin only")
	}
	for _, id := range []string{mine, theirs, unowned} {
		if _, ok := s.Get(id, 0); !ok {
			t.Errorf("scope 0 (admin) reads %s", id)
		}
	}
	if _, ok := s.Get("nope", 0); ok {
		t.Error("unknown id")
	}
}

func TestNilStoreIsInert(t *testing.T) {
	var s *Store
	if id := s.Start(1, 1, "x"); id != "" {
		t.Fatalf("nil store hands out no ids, got %q", id)
	}
	s.Running("a")
	s.Record("a", Result{})
	s.Finish("a")
	if _, ok := s.Get("a", 0); ok {
		t.Fatal("nil store knows nothing")
	}
}

func TestContextRoundTrip(t *testing.T) {
	if RunIDFrom(context.Background()) != "" {
		t.Fatal("no id by default")
	}
	ctx := WithRunID(context.Background(), "abc")
	if RunIDFrom(ctx) != "abc" {
		t.Fatal("id must round trip")
	}
	if WithRunID(ctx, "") != ctx {
		t.Fatal("an empty id leaves the context alone")
	}
}

func TestConcurrentUse(t *testing.T) {
	s := NewStore(50, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				id := s.Start(int64(i), int64(j), "bulk")
				s.Running(id)
				s.Record(id, Result{Outcome: "no results"})
				s.Finish(id)
				s.Get(id, 0)
			}
		}(i)
	}
	wg.Wait()
	if s.Len() > 50 {
		t.Fatalf("bound exceeded: %d", s.Len())
	}
}
