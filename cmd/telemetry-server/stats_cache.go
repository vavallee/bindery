package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// defaultStatsTTL bounds how stale /stats and /api/stats can be. The
	// dashboard runs a dozen aggregate queries; a minute of staleness on
	// install counts that move a few times an hour is invisible, and it
	// stops a burst of visitors from running them once per page load.
	defaultStatsTTL = 60 * time.Second
	// statsFillTimeout caps one cache fill. The fill is detached from the
	// request that started it, so this is the only thing that stops it.
	statsFillTimeout = 30 * time.Second
)

// statsCache holds the last computed snapshot and the fill in progress, if
// any. Concurrent misses wait on the same fill instead of each running the
// queries.
type statsCache struct {
	mu      sync.Mutex
	data    *statsData
	expires time.Time
	fill    *statsFill
}

// statsFill is one in-flight queryStats run. done is closed once data and
// err are set.
type statsFill struct {
	done chan struct{}
	data *statsData
	err  error
}

// computeStats returns the dashboard snapshot, from the cache while it is
// younger than s.statsTTL, otherwise from a fresh queryStats run shared by
// every request that misses at the same time. The returned snapshot is
// shared: callers must not modify it.
//
// The fill runs on a context detached from the request that started it,
// with its own timeout, so the first visitor closing the tab does not fail
// the fill for everyone waiting on it. A request whose own context ends
// first stops waiting and gets its context error; the fill carries on and
// still lands in the cache. Errors are never cached.
func (s *server) computeStats(ctx context.Context) (*statsData, error) {
	c := &s.stats
	c.mu.Lock()
	if c.data != nil && time.Now().Before(c.expires) {
		d := c.data
		c.mu.Unlock()
		return d, nil
	}
	// A request that has already gone does not start a fill on its own.
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	f := c.fill
	if f == nil {
		f = &statsFill{done: make(chan struct{})}
		c.fill = f
		go s.runStatsFill(context.WithoutCancel(ctx), f)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.data, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runStatsFill computes one snapshot, publishes it to the waiters on f and,
// on success, to the cache.
func (s *server) runStatsFill(parent context.Context, f *statsFill) {
	ctx, cancel := context.WithTimeout(parent, statsFillTimeout)
	defer cancel()

	query := s.statsQuery
	if query == nil {
		query = s.queryStats
	}
	d, err := func() (d *statsData, err error) {
		// The handler goroutine used to run this, where net/http would have
		// recovered a panic. On its own goroutine a panic would take the
		// process down, so turn it into an error for the waiters.
		defer func() {
			if p := recover(); p != nil {
				d, err = nil, fmt.Errorf("stats query panicked: %v", p)
			}
		}()
		return query(ctx)
	}()

	c := &s.stats
	c.mu.Lock()
	f.data, f.err = d, err
	if err == nil && s.statsTTL > 0 {
		c.data = d
		c.expires = time.Now().Add(s.statsTTL)
	}
	c.fill = nil
	c.mu.Unlock()
	close(f.done)
}
