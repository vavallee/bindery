// Package jobs provides a small tracker for detached background goroutines so
// the process can drain them on shutdown before it tears down shared resources
// (notably the database).
//
// Several long-running jobs — the ABS import, the Grimmory bulk sync, the
// manual library scan, and a handful of startup syncs — are launched as
// detached goroutines that must outlive the HTTP request that triggered them.
// Historically that was done with context.WithoutCancel(r.Context()) or
// context.Background(): the response returning no longer killed the job, but
// nothing cancelled or waited on the job at process shutdown either. On SIGTERM
// the server would call database.Close() while such a job was mid-flight,
// producing "database is closed" errors, and a Grimmory sync dying mid-upload
// would re-push everything on the next run because the push is recorded only
// after success (#1458).
//
// Group decouples the two concerns. Jobs launched through it run on a
// shutdown-scoped context that is NOT tied to any request (so a response
// returning does not cancel them) but IS cancelled when the process begins
// shutting down. A WaitGroup tracks every in-flight job so shutdown can drain
// them, within a bounded grace window, before closing the database.
package jobs

import (
	"context"
	"sync"
	"time"
)

// Group tracks detached background goroutines derived from a single
// shutdown-scoped context. It is safe for concurrent use.
type Group struct {
	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	mu     sync.Mutex
	active map[int64]string // in-flight job id -> name, for shutdown logging
	seq    int64
	closed bool
}

// NewGroup returns a Group whose jobs derive from a context that is cancelled
// either when parent is cancelled or when Shutdown is called. Pass the
// process-lifetime context (the one wired to SIGINT/SIGTERM) as parent so the
// group is also cancelled if the process context is.
func NewGroup(parent context.Context) *Group {
	ctx, cancel := context.WithCancel(parent)
	return &Group{
		ctx:    ctx,
		cancel: cancel,
		active: make(map[int64]string),
	}
}

// Context returns the group's shutdown-scoped context. Jobs launched via Go
// already receive it; this is exposed for call sites that need to observe
// cancellation directly (or thread it into a helper that launches its own
// goroutines).
func (g *Group) Context() context.Context { return g.ctx }

// Go launches fn as a tracked background goroutine, passing the group's
// shutdown-scoped context. fn should return promptly once that context is
// cancelled. name is a short label used only for shutdown logging when a job
// overruns the grace window.
//
// If the group has already begun shutting down, Go is a no-op: the context is
// already cancelled, and running the job would only race the resource teardown
// the shutdown is about to perform.
func (g *Group) Go(name string, fn func(ctx context.Context)) {
	if fn == nil {
		return
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.seq++
	id := g.seq
	g.active[id] = name
	g.wg.Add(1)
	g.mu.Unlock()

	go func() {
		defer g.wg.Done()
		defer func() {
			g.mu.Lock()
			delete(g.active, id)
			g.mu.Unlock()
		}()
		fn(g.ctx)
	}()
}

// Shutdown signals every tracked job to stop by cancelling the shutdown-scoped
// context, then waits up to grace for the jobs to return. It returns the names
// of any jobs still running when the grace window expired; the slice is empty
// when everything drained cleanly. After Shutdown returns, further Go calls are
// no-ops. Shutdown is idempotent.
func (g *Group) Shutdown(grace time.Duration) []string {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()

	g.cancel()

	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(grace):
		return g.activeNames()
	}
}

// activeNames snapshots the labels of the jobs still in flight.
func (g *Group) activeNames() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	names := make([]string, 0, len(g.active))
	for _, n := range g.active {
		names = append(names, n)
	}
	return names
}
