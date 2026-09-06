package importer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/jobs"
)

// TestStartScan_ShutDownGroupResetsTheGate is the regression guard for #2372.
//
// jobs.Group.Go is a documented no-op once the group has begun shutting down,
// and StartScan claims the single-flight gate before calling it. Ignoring the
// return left scanRunning true for the rest of the process's life and answered
// the manual-scan endpoint with success for a scan that never ran.
func TestStartScan_ShutDownGroupResetsTheGate(t *testing.T) {
	s, settings, ctx := singleFlightFixture(t)

	group := jobs.NewGroup(context.Background())
	if names := group.Shutdown(time.Second); len(names) != 0 {
		t.Fatalf("shutting down an idle group reported jobs still running: %v", names)
	}
	s.WithJobs(group)

	err := s.StartScan(ctx)
	if err == nil {
		t.Fatal("StartScan reported success for a scan the shut-down jobs group refused to launch")
	}
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("StartScan error: got %v, want ErrShuttingDown", err)
	}
	if s.scanRunning.Load() {
		t.Error("the single-flight gate is still held, so every later scan on this process is refused")
	}
	// Nothing ran, so nothing may have been recorded.
	if setting, err := settings.Get(ctx, "library.lastScan"); err != nil {
		t.Fatal(err)
	} else if setting != nil {
		t.Errorf("a refused scan wrote a lastScan result: %q", setting.Value)
	}
}

// TestStartScan_LiveGroupStillRuns pins the other direction: a healthy group
// launches the scan and the gate is released when it finishes.
func TestStartScan_LiveGroupStillRuns(t *testing.T) {
	s, settings, ctx := singleFlightFixture(t)

	group := jobs.NewGroup(context.Background())
	t.Cleanup(func() { group.Shutdown(5 * time.Second) })
	s.WithJobs(group)

	if err := s.StartScan(ctx); err != nil {
		t.Fatalf("StartScan with a live group: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		setting, err := settings.Get(ctx, "library.lastScan")
		if err != nil {
			t.Fatal(err)
		}
		if setting != nil && !s.scanRunning.Load() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("scan launched through the jobs group never completed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
