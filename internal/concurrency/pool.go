// Package concurrency provides small primitives for bounding goroutine
// fan-out in handlers and background jobs.
//
// The deep-audit Wave 3 / I work uncovered three call sites that spawned one
// goroutine per item without any cap (`for _, x := range xs { go fn(x) }`):
// bulk author/book/wanted "search" actions could fire 500 simultaneous
// indexer searches off a single click, the queue list endpoint synchronously
// polled every downloader client, and Series.Fill kicked off one goroutine
// per book in a series. RunBounded replaces all three with a fixed-cap
// semaphore pattern; RunBoundedWithTimeout extends that to per-call
// deadlines so one slow upstream can't gate the rest of the result set.
package concurrency

import (
	"context"
	"sync"
	"time"
)

// RunBounded runs fn for each item with at most maxConcurrent in flight.
// It blocks until every fn returns or ctx is canceled. If ctx is canceled
// mid-fan-out, no further items are launched and the call returns as soon
// as the already-running fns finish; the caller is responsible for making
// fn itself ctx-aware if it should stop early.
//
// maxConcurrent <= 0 is treated as 1 so callers can't accidentally
// serialize or unbound the pool by passing a misconfigured value.
func RunBounded[T any](ctx context.Context, items []T, maxConcurrent int, fn func(context.Context, T)) {
	if fn == nil || len(items) == 0 {
		return
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(ctx, item)
		}()
	}
	wg.Wait()
}

// BoundedResult pairs a per-item outcome with whether it actually
// completed within the per-call deadline. Items whose fn returned an
// error, whose timeout fired, or whose parent ctx was canceled before
// they ran have Done=false and Value=zero(R). Err carries the fn error
// when Done=false and the fn itself returned non-nil; it is nil for
// timeout / ctx-cancel skips so callers can distinguish "the upstream
// said no" from "we never heard back".
type BoundedResult[R any] struct {
	Value R
	Err   error
	Done  bool
}

// RunBoundedWithTimeout runs fn for each item with a per-call timeout and
// at most maxConcurrent in flight. The returned slice is indexed in lock
// step with items: results[i] corresponds to items[i]. Items whose fn
// returned an error or whose per-call deadline fired carry Done=false;
// successful results carry Done=true and the fn's return value.
//
// perCallTimeout <= 0 disables the per-call deadline (each fn runs under
// ctx alone). maxConcurrent <= 0 is treated as 1.
func RunBoundedWithTimeout[T, R any](
	ctx context.Context,
	items []T,
	maxConcurrent int,
	perCallTimeout time.Duration,
	fn func(context.Context, T) (R, error),
) []BoundedResult[R] {
	results := make([]BoundedResult[R], len(items))
	if fn == nil || len(items) == 0 {
		return results
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for i, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results
		}
		i, item := i, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			callCtx := ctx
			var cancel context.CancelFunc
			if perCallTimeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, perCallTimeout)
				defer cancel()
			}

			value, err := fn(callCtx, item)
			if err != nil {
				results[i].Err = err
				return
			}
			results[i].Value = value
			results[i].Done = true
		}()
	}
	wg.Wait()
	return results
}
