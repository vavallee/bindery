package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A job launched via Go runs on the group's shutdown-scoped context, and
// Shutdown cancels that context and waits for the job to drain.
func TestGroup_CancelAndWaitDrains(t *testing.T) {
	g := NewGroup(context.Background())

	started := make(chan struct{})
	var finished atomic.Bool

	g.Go("drainer", func(ctx context.Context) {
		close(started)
		<-ctx.Done() // block until Shutdown cancels the group context
		finished.Store(true)
	})

	<-started

	still := g.Shutdown(2 * time.Second)
	if len(still) != 0 {
		t.Fatalf("expected all jobs drained, still running: %v", still)
	}
	if !finished.Load() {
		t.Fatal("job did not observe cancellation / did not finish before Shutdown returned")
	}
}

// The response-return decoupling: a job keeps running after the (simulated)
// request context is cancelled, because it runs on the group's context, not the
// request's.
func TestGroup_JobSurvivesRequestContextCancel(t *testing.T) {
	g := NewGroup(context.Background())

	reqCtx, cancelReq := context.WithCancel(context.Background())

	running := make(chan struct{})
	release := make(chan struct{})
	checked := make(chan struct{})
	var sawJobCtxAlive atomic.Bool

	g.Go("survivor", func(jobCtx context.Context) {
		close(running)
		<-release
		// The request context is cancelled by now; the job context must not be.
		// Record and signal before returning so this read can't race Shutdown's
		// cancellation below.
		sawJobCtxAlive.Store(jobCtx.Err() == nil)
		close(checked)
	})

	<-running
	cancelReq() // simulate the HTTP response returning
	_ = reqCtx
	close(release)
	<-checked // job has recorded its observation; only now begin shutdown

	if still := g.Shutdown(2 * time.Second); len(still) != 0 {
		t.Fatalf("expected clean drain, still running: %v", still)
	}
	if !sawJobCtxAlive.Load() {
		t.Fatal("job context was cancelled by the request context — WithoutCancel property regressed")
	}
}

// A job that ignores cancellation and overruns the grace window is reported by
// name, and Shutdown returns instead of hanging forever.
func TestGroup_GraceTimeoutReportsRunningJobs(t *testing.T) {
	g := NewGroup(context.Background())

	release := make(chan struct{})
	defer close(release) // let the stuck goroutine exit at test end

	started := make(chan struct{})
	g.Go("stuck-job", func(ctx context.Context) {
		close(started)
		<-release // deliberately ignores ctx cancellation
	})
	<-started

	start := time.Now()
	still := g.Shutdown(50 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown hung past its grace window: %v", elapsed)
	}
	if len(still) != 1 || still[0] != "stuck-job" {
		t.Fatalf("expected [stuck-job] still running, got %v", still)
	}
}

// After Shutdown, Go is a no-op so late arrivals can't touch resources that are
// being torn down. Shutdown is also idempotent.
func TestGroup_GoAfterShutdownIsNoOp(t *testing.T) {
	g := NewGroup(context.Background())

	if still := g.Shutdown(time.Second); len(still) != 0 {
		t.Fatalf("empty group should drain immediately, got %v", still)
	}

	var ran atomic.Bool
	g.Go("late", func(ctx context.Context) { ran.Store(true) })

	// Second Shutdown must not hang and must report nothing.
	if still := g.Shutdown(time.Second); len(still) != 0 {
		t.Fatalf("idempotent Shutdown reported jobs: %v", still)
	}
	if ran.Load() {
		t.Fatal("Go after Shutdown launched a job")
	}
}

// A cancelled parent context propagates into the group's job context.
func TestGroup_ParentCancelPropagates(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	g := NewGroup(parent)

	observed := make(chan struct{})
	g.Go("child", func(ctx context.Context) {
		<-ctx.Done()
		close(observed)
	})

	cancelParent()

	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not observe parent context cancellation")
	}

	_ = g.Shutdown(time.Second)
}
