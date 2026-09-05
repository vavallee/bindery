package grimmory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/jobs"
)

// TestStart_ShutDownGroupRollsBackProgress is the regression guard for #2372.
//
// Start publishes running=true and a Running progress snapshot before handing
// the work to the jobs group. jobs.Group.Go is a documented no-op once the group
// has begun shutting down, so ignoring its return left Progress() describing a
// sync that would never finish for the rest of the process's life.
func TestStart_ShutDownGroupRollsBackProgress(t *testing.T) {
	group := jobs.NewGroup(context.Background())
	if names := group.Shutdown(time.Second); len(names) != 0 {
		t.Fatalf("shutting down an idle group reported jobs still running: %v", names)
	}

	s := NewSyncer(nil, nil).WithJobs(group)
	cfg := PushConfig{Enabled: true, BaseURL: "http://grimmory.example", APIKey: "k"}

	if err := s.Start(context.Background(), cfg); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start on a shut-down group: got %v, want ErrShuttingDown", err)
	}

	progress := s.Progress()
	if progress.Running {
		t.Error("Progress still reports a running sync that was never launched")
	}
	if progress.FinishedAt == nil {
		t.Error("Progress has no FinishedAt, so the UI polls a run that never ends")
	}
	if progress.Error == "" {
		t.Error("Progress carries no error, so nothing explains why the sync did not run")
	}

	// The gate has to be released too, otherwise a later Start answers
	// ErrSyncAlreadyRunning forever.
	if err := s.Start(context.Background(), cfg); errors.Is(err, ErrSyncAlreadyRunning) {
		t.Error("the running gate was not released; every later sync is refused")
	}
}
