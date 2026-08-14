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

// A panic inside a tracked job must not take the process down. Detached jobs
// have nothing above them on the stack — no chi middleware.Recoverer, no
// scheduler.runJob — so without the recover in Group this test binary crashes
// outright rather than failing, which is the mutation proof: delete the recover
// and `go test ./internal/jobs/` dies with the panic trace.
func TestGroup_GoRecoversPanic(t *testing.T) {
	g := NewGroup(context.Background())

	panicked := make(chan struct{})
	g.Go("boom", func(ctx context.Context) {
		close(panicked)
		panic("job exploded")
	})
	<-panicked

	// The group must still drain: the panicking goroutine has to release its
	// WaitGroup token and clear itself from the active map.
	if still := g.Shutdown(2 * time.Second); len(still) != 0 {
		t.Fatalf("panicking job left the group undrained: %v", still)
	}
}

// The same containment for the synchronous variant: Run returns normally after
// the job panics instead of unwinding into its caller (a cron callback, an HTTP
// handler) and killing whatever is there.
func TestGroup_RunRecoversPanic(t *testing.T) {
	g := NewGroup(context.Background())
	defer g.Shutdown(time.Second)

	if ran := g.Run("boom", func(ctx context.Context) { panic("job exploded") }); !ran {
		t.Fatal("Run reported the job did not run")
	}
}

// Go reports whether it actually launched, so callers that publish "running"
// state before calling it can undo that state when the group is closed.
func TestGroup_GoReportsWhetherItLaunched(t *testing.T) {
	g := NewGroup(context.Background())

	done := make(chan struct{})
	if launched := g.Go("live", func(ctx context.Context) { close(done) }); !launched {
		t.Fatal("Go on an open group reported not launched")
	}
	<-done

	_ = g.Shutdown(time.Second)

	var ran atomic.Bool
	if launched := g.Go("late", func(ctx context.Context) { ran.Store(true) }); launched {
		t.Error("Go on a closed group reported launched")
	}
	if ran.Load() {
		t.Error("Go after Shutdown ran the job")
	}
}

// Run executes on the caller's goroutine with the group's shutdown-scoped
// context, and Shutdown waits for it — the property the scheduled Hardcover
// sync needs so it isn't still writing when database.Close() fires.
func TestGroup_RunIsTrackedAndWaitedOn(t *testing.T) {
	g := NewGroup(context.Background())

	started := make(chan struct{})
	release := make(chan struct{})
	var sawGroupCtx atomic.Bool
	var returned atomic.Bool

	go func() {
		g.Run("sync-job", func(ctx context.Context) {
			sawGroupCtx.Store(ctx == g.Context())
			close(started)
			<-release
		})
		returned.Store(true)
	}()
	<-started

	// Grace expires while the job is still in flight: it must be reported,
	// which is only possible if Run registered it.
	still := g.Shutdown(100 * time.Millisecond)
	if len(still) != 1 || still[0] != "sync-job" {
		t.Fatalf("Shutdown reported %v, want the in-flight synchronous job", still)
	}
	if !sawGroupCtx.Load() {
		t.Error("Run did not pass the group's shutdown-scoped context")
	}

	close(release)
	// A second Shutdown now drains, proving Run released its WaitGroup token.
	if still := g.Shutdown(2 * time.Second); len(still) != 0 {
		t.Fatalf("job did not release its tracking slot: %v", still)
	}
	if !returned.Load() {
		t.Error("Run did not return after its job finished")
	}
}

// Run is a no-op once the group is closed, and says so, so a caller can't be
// left believing work was done during teardown.
func TestGroup_RunAfterShutdownIsNoOp(t *testing.T) {
	g := NewGroup(context.Background())
	_ = g.Shutdown(time.Second)

	var ran atomic.Bool
	if did := g.Run("late", func(ctx context.Context) { ran.Store(true) }); did {
		t.Error("Run on a closed group reported it ran")
	}
	if ran.Load() {
		t.Error("Run after Shutdown executed the job")
	}
}
