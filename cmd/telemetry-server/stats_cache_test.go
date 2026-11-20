package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingQuery stands in for queryStats: it counts calls and, when gate is
// non-nil, signals started and blocks until gate is closed.
type countingQuery struct {
	calls   atomic.Int32
	started chan struct{}
	gate    chan struct{}
	// fail makes the call with this 1-based number return errBoom.
	fail int32
	// sawCtxErr records the fill context's error when the query returns.
	sawCtxErr atomic.Value
}

var errBoom = errors.New("boom")

func (q *countingQuery) run(ctx context.Context) (*statsData, error) {
	n := q.calls.Add(1)
	if q.gate != nil {
		if q.started != nil {
			select {
			case q.started <- struct{}{}:
			default:
			}
		}
		<-q.gate
	}
	if err := ctx.Err(); err != nil {
		q.sawCtxErr.Store(err)
		return nil, err
	}
	if n == q.fail {
		return nil, errBoom
	}
	return &statsData{Active30d: int(n)}, nil
}

func cachedServer(t *testing.T, ttl time.Duration, q *countingQuery) *server {
	t.Helper()
	s := newTestServer(t, "v1.9.5")
	s.statsTTL = ttl
	s.statsQuery = q.run
	return s
}

func TestComputeStats_CachesWithinTTL(t *testing.T) {
	q := &countingQuery{}
	s := cachedServer(t, time.Hour, q)

	first, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("queryStats ran %d times within the TTL, want 1", got)
	}
	if first != second {
		t.Error("second call did not return the cached snapshot")
	}
	if left := time.Until(s.stats.expires); left < 59*time.Minute || left > time.Hour {
		t.Errorf("cache expires in %v, want about the configured TTL of 1h", left)
	}
}

func TestComputeStats_RecomputesAfterTTL(t *testing.T) {
	q := &countingQuery{}
	s := cachedServer(t, time.Hour, q)

	if _, err := s.computeStats(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Age the entry past its TTL rather than sleeping.
	s.stats.mu.Lock()
	s.stats.expires = time.Now().Add(-time.Millisecond)
	s.stats.mu.Unlock()

	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("after TTL: %v", err)
	}
	if got := q.calls.Load(); got != 2 {
		t.Errorf("queryStats ran %d times, want 2 after the TTL passed", got)
	}
	if d.Active30d != 2 {
		t.Errorf("got snapshot %d, want the fresh one (2)", d.Active30d)
	}
}

// A real short TTL, end to end, so the field is proven to drive expiry.
func TestComputeStats_ShortTTLExpires(t *testing.T) {
	q := &countingQuery{}
	s := cachedServer(t, 10*time.Millisecond, q)

	if _, err := s.computeStats(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := s.computeStats(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := q.calls.Load(); got != 2 {
		t.Errorf("queryStats ran %d times, want 2 with a 10ms TTL", got)
	}
}

func TestComputeStats_ConcurrentMissesRunOnce(t *testing.T) {
	q := &countingQuery{started: make(chan struct{}, 1), gate: make(chan struct{})}
	s := cachedServer(t, time.Hour, q)

	const n = 16
	var wg sync.WaitGroup
	results := make([]*statsData, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.computeStats(context.Background())
		}()
	}
	<-q.started
	// Give the other goroutines a moment to queue on the fill; any that
	// arrive after it completes hit the cache, which is just as good.
	time.Sleep(20 * time.Millisecond)
	close(q.gate)
	wg.Wait()

	if got := q.calls.Load(); got != 1 {
		t.Errorf("queryStats ran %d times for %d concurrent misses, want 1", got, n)
	}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Errorf("caller %d got a different snapshot", i)
		}
	}
}

func TestComputeStats_ErrorNotCached(t *testing.T) {
	q := &countingQuery{fail: 1}
	s := cachedServer(t, time.Hour, q)

	if _, err := s.computeStats(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("first: err = %v, want errBoom", err)
	}
	d, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("second: %v (the error was cached)", err)
	}
	if got := q.calls.Load(); got != 2 {
		t.Errorf("queryStats ran %d times, want 2 (error must not be cached)", got)
	}
	if d.Active30d != 2 {
		t.Errorf("got snapshot %d, want 2", d.Active30d)
	}
}

// The visitor who triggers the fill leaves before it finishes. They get
// their own context error back, the fill is not cancelled with them, and
// everyone else, waiting or arriving later, is served from it.
func TestComputeStats_CancelledStarterDoesNotFailFill(t *testing.T) {
	q := &countingQuery{started: make(chan struct{}, 1), gate: make(chan struct{})}
	s := cachedServer(t, time.Hour, q)

	starterCtx, cancelStarter := context.WithCancel(context.Background())
	starterErr := make(chan error, 1)
	go func() {
		_, err := s.computeStats(starterCtx)
		starterErr <- err
	}()
	<-q.started

	waiterRes := make(chan *statsData, 1)
	waiterErr := make(chan error, 1)
	go func() {
		d, err := s.computeStats(context.Background())
		waiterRes <- d
		waiterErr <- err
	}()

	cancelStarter()
	select {
	case err := <-starterErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("starter err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled starter kept waiting on the fill")
	}

	close(q.gate)
	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter: %v (the starter's cancel failed the shared fill)", err)
	}
	d := <-waiterRes
	if v := q.sawCtxErr.Load(); v != nil {
		t.Errorf("fill context was cancelled: %v", v)
	}

	later, err := s.computeStats(context.Background())
	if err != nil {
		t.Fatalf("later: %v", err)
	}
	if later != d {
		t.Error("later request was not served the completed fill from cache")
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("queryStats ran %d times, want 1", got)
	}
}

func TestComputeStats_PanicBecomesError(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	s.statsTTL = time.Hour
	s.statsQuery = func(context.Context) (*statsData, error) { panic("kaboom") }
	if _, err := s.computeStats(context.Background()); err == nil {
		t.Fatal("panicking query returned no error")
	}
	if s.stats.fill != nil {
		t.Error("in-flight marker left behind after a panic")
	}
}

// /stats and /api/stats share one snapshot.
func TestStatsHandlers_ShareCache(t *testing.T) {
	q := &countingQuery{}
	s := cachedServer(t, time.Hour, q)
	s.statsToken = "s3cret"

	for _, h := range []struct {
		path string
		fn   http.HandlerFunc
	}{
		{"/stats", s.handleStatsPage},
		{"/api/stats", s.handleStats},
		{"/stats", s.handleStatsPage},
	} {
		req := httptest.NewRequest(http.MethodGet, h.path, nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		h.fn(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", h.path, rec.Code)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("queryStats ran %d times across /stats and /api/stats, want 1", got)
	}
}

// Concurrent /stats renders read one cached snapshot. Under -race this
// catches any handler or renderer that writes into it.
func TestStatsPage_ConcurrentRendersShareSnapshot(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	s.statsTTL = time.Hour
	now := time.Now().UTC()
	// Two OS buckets tied on count, so a renderer or handler that re-sorted
	// the shared slices would actually swap them and write.
	for _, v := range []struct{ id, os string }{
		{"11111111-1111-1111-1111-111111111111", "linux"},
		{"22222222-2222-2222-2222-222222222222", "darwin"},
	} {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
			 VALUES (?, '1.9.5', ?, 'amd64', ?, ?, 'docker')`, v.id, v.os, now, now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if _, err := s.computeStats(context.Background()); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.handleStatsPage(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
}

// The tie break sort used to run in handleStatsPage on the snapshot it got
// back. With a shared cached snapshot that would be a data race, so it now
// happens once in queryStats; pin that it still happens.
func TestQueryStats_SortsBucketsWithTieBreak(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	now := time.Now().UTC()
	for i, v := range []struct{ id, os string }{
		{"11111111-1111-1111-1111-111111111111", "windows"},
		{"22222222-2222-2222-2222-222222222222", "linux"},
		{"33333333-3333-3333-3333-333333333333", "darwin"},
	} {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO installs (install_id, version, os, arch, first_seen, last_seen, deploy)
			 VALUES (?, '1.9.5', ?, 'amd64', ?, ?, 'docker')`,
			v.id, v.os, now.Add(-time.Duration(i)*time.Hour), now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	d, err := s.queryStats(context.Background())
	if err != nil {
		t.Fatalf("queryStats: %v", err)
	}
	var labels []string
	for _, b := range d.OS {
		labels = append(labels, b.Label)
	}
	want := []string{"darwin", "linux", "windows"}
	if len(labels) != len(want) {
		t.Fatalf("OS buckets = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("OS buckets = %v, want %v (count desc, then label asc)", labels, want)
		}
	}
}
