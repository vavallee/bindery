package concurrency

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBounded_RespectsLimit(t *testing.T) {
	const items = 100
	const cap = 4

	var active, maxActive int32
	work := make([]int, items)
	for i := range work {
		work[i] = i
	}

	RunBounded(context.Background(), work, cap, func(_ context.Context, _ int) {
		now := atomic.AddInt32(&active, 1)
		for {
			prev := atomic.LoadInt32(&maxActive)
			if now <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, now) {
				break
			}
		}
		// Hold the slot long enough that several goroutines pile up.
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&active, -1)
	})

	if got := atomic.LoadInt32(&maxActive); got > cap {
		t.Fatalf("max concurrent observed = %d, want <= %d", got, cap)
	}
	if got := atomic.LoadInt32(&maxActive); got < 2 {
		t.Fatalf("expected parallel execution, maxActive = %d", got)
	}
}

func TestRunBounded_AllItemsProcessed(t *testing.T) {
	const items = 50
	var processed int32
	work := make([]int, items)
	for i := range work {
		work[i] = i
	}

	var seen sync.Map
	RunBounded(context.Background(), work, 8, func(_ context.Context, n int) {
		atomic.AddInt32(&processed, 1)
		seen.Store(n, struct{}{})
	})

	if got := atomic.LoadInt32(&processed); got != items {
		t.Fatalf("processed = %d, want %d", got, items)
	}
	for i := 0; i < items; i++ {
		if _, ok := seen.Load(i); !ok {
			t.Fatalf("item %d was not processed", i)
		}
	}
}

func TestRunBounded_CtxCancelExitsCleanly(t *testing.T) {
	const items = 100
	var started, finished int32

	ctx, cancel := context.WithCancel(context.Background())
	work := make([]int, items)
	for i := range work {
		work[i] = i
	}

	done := make(chan struct{})
	go func() {
		RunBounded(ctx, work, 4, func(ctx context.Context, _ int) {
			atomic.AddInt32(&started, 1)
			select {
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
			atomic.AddInt32(&finished, 1)
		})
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("RunBounded did not return after ctx cancel; started=%d finished=%d",
			atomic.LoadInt32(&started), atomic.LoadInt32(&finished))
	}

	// All workers we actually launched must have returned.
	s := atomic.LoadInt32(&started)
	f := atomic.LoadInt32(&finished)
	if s != f {
		t.Fatalf("goroutine leak: started=%d finished=%d", s, f)
	}
	if s >= items {
		t.Fatalf("expected ctx cancel to short-circuit fan-out; started=%d items=%d", s, items)
	}
}

func TestRunBounded_NilFnIsNoop(t *testing.T) {
	// Just must not panic.
	RunBounded[int](context.Background(), []int{1, 2, 3}, 4, nil)
}

func TestRunBounded_EmptyItemsIsNoop(t *testing.T) {
	called := false
	RunBounded(context.Background(), []int{}, 4, func(_ context.Context, _ int) {
		called = true
	})
	if called {
		t.Fatal("fn called for empty input")
	}
}

func TestRunBounded_NonPositiveCapBecomesOne(t *testing.T) {
	var maxActive, active int32
	work := []int{1, 2, 3, 4, 5}

	RunBounded(context.Background(), work, 0, func(_ context.Context, _ int) {
		now := atomic.AddInt32(&active, 1)
		for {
			prev := atomic.LoadInt32(&maxActive)
			if now <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, now) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&active, -1)
	})

	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("maxActive = %d, want 1 (cap<=0 should serialize)", got)
	}
}

func TestRunBoundedWithTimeout_TimeoutFires(t *testing.T) {
	items := []int{0, 1, 2, 3, 4}
	results := RunBoundedWithTimeout(
		context.Background(),
		items,
		4,
		50*time.Millisecond,
		func(ctx context.Context, n int) (int, error) {
			if n == 2 {
				// Slow worker, well beyond the per-call deadline.
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return n * 10, nil
				}
			}
			return n * 10, nil
		},
	)

	if len(results) != len(items) {
		t.Fatalf("results len = %d, want %d", len(results), len(items))
	}
	for i, r := range results {
		if i == 2 {
			if r.Done {
				t.Errorf("slow item should not be Done, got value=%v", r.Value)
			}
			if r.Err == nil {
				t.Errorf("slow item should have a ctx-deadline error, got nil")
			}
			continue
		}
		if !r.Done {
			t.Errorf("item %d should be Done, err=%v", i, r.Err)
		}
		if r.Value != i*10 {
			t.Errorf("item %d value = %d, want %d", i, r.Value, i*10)
		}
	}
}

func TestRunBoundedWithTimeout_ErrorsRecorded(t *testing.T) {
	boom := errors.New("boom")
	items := []int{1, 2, 3}
	results := RunBoundedWithTimeout(
		context.Background(),
		items,
		2,
		time.Second,
		func(_ context.Context, n int) (string, error) {
			if n == 2 {
				return "", boom
			}
			return "ok", nil
		},
	)

	if results[1].Done {
		t.Fatal("erroring item should not be Done")
	}
	if !errors.Is(results[1].Err, boom) {
		t.Fatalf("err = %v, want boom", results[1].Err)
	}
	for _, i := range []int{0, 2} {
		if !results[i].Done || results[i].Value != "ok" {
			t.Errorf("item %d unexpected: %+v", i, results[i])
		}
	}
}

func TestRunBoundedWithTimeout_RespectsLimit(t *testing.T) {
	const items = 40
	const cap = 3
	var active, maxActive int32

	work := make([]int, items)
	for i := range work {
		work[i] = i
	}

	RunBoundedWithTimeout(
		context.Background(),
		work,
		cap,
		time.Second,
		func(_ context.Context, _ int) (int, error) {
			now := atomic.AddInt32(&active, 1)
			for {
				prev := atomic.LoadInt32(&maxActive)
				if now <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, now) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&active, -1)
			return 0, nil
		},
	)

	if got := atomic.LoadInt32(&maxActive); got > cap {
		t.Fatalf("max concurrent = %d, want <= %d", got, cap)
	}
}

func TestRunBoundedWithTimeout_EmptyInput(t *testing.T) {
	results := RunBoundedWithTimeout[int, int](
		context.Background(), nil, 4, time.Second,
		func(_ context.Context, n int) (int, error) { return n, nil },
	)
	if len(results) != 0 {
		t.Fatalf("expected empty results, got %d", len(results))
	}
}
