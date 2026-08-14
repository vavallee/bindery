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
	"log/slog"
	"runtime/debug"
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
	//nolint:gosec // G118: cancel is retained on the Group and invoked in Shutdown; gosec cannot track a CancelFunc stored in a struct field
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
// It reports whether the job was launched. If the group has already begun
// shutting down, Go is a no-op and returns false: the context is already
// cancelled, and running the job would only race the resource teardown the
// shutdown is about to perform. Callers that publish "a job is running" state
// before calling Go must undo it when Go returns false, or that state sticks
// for the rest of the process's life.
func (g *Group) Go(name string, fn func(ctx context.Context)) bool {
	if fn == nil {
		return false
	}

	id, ok := g.begin(name)
	if !ok {
		return false
	}

	go func() {
		defer g.end(id)
		guard(name, func() { fn(g.ctx) })
	}()
	return true
}

// Run is Go's synchronous twin: it runs fn on the calling goroutine, still
// tracked by the group and still passing the shutdown-scoped context, and
// returns once fn does. Use it for work that is already on its own goroutine
// (a cron callback, say) but whose lifetime shutdown must nevertheless wait on
// — without it, such a job runs on a context that is cancelled the moment
// SIGTERM lands and can still be mid-write when database.Close() fires.
//
// It reports whether fn ran; false means the group had already begun shutting
// down, in which case fn is not called at all.
func (g *Group) Run(name string, fn func(ctx context.Context)) bool {
	if fn == nil {
		return false
	}

	id, ok := g.begin(name)
	if !ok {
		return false
	}
	defer g.end(id)

	guard(name, func() { fn(g.ctx) })
	return true
}

// begin registers an in-flight job, reporting false if the group is closed.
func (g *Group) begin(name string) (int64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return 0, false
	}
	g.seq++
	id := g.seq
	g.active[id] = name
	g.wg.Add(1)
	return id, true
}

// end deregisters an in-flight job and releases the WaitGroup token.
func (g *Group) end(id int64) {
	g.mu.Lock()
	delete(g.active, id)
	g.mu.Unlock()
	g.wg.Done()
}

// guard runs fn and turns a panic into a logged error instead of a dead
// process. Jobs launched through a Group are detached: unlike a handler behind
// chi's middleware.Recoverer or a cron callback behind scheduler.runJob, there
// is nothing above them on the stack to recover, so an unhandled panic in any
// one of them takes the whole service down. Containing it here gives every
// tracked job the same blast radius the request path already had.
//
// Recovering does not make the job's own state consistent — a job that keeps a
// "running" flag or a progress snapshot must still reset it in its own defer.
func guard(name string, fn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("background job panicked",
				"job", name, "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	fn()
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
