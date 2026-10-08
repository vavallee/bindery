package importer

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartScan_QueuesARequestMadeDuringAScan is the reproduction for #3014: a
// scan requested while one is walking used to be refused, so a file placed in
// a folder the walk had already passed was not picked up until the next
// scheduled scan. The request is now queued, and however many arrive, exactly
// one follow-up scan runs once the current one finishes.
func TestStartScan_QueuesARequestMadeDuringAScan(t *testing.T) {
	s, _, ctx := singleFlightFixture(t)

	var runs atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	s.testScanHook = func() {
		if runs.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
	}

	if err := s.StartScan(ctx); err != nil {
		t.Fatalf("first StartScan: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first scan never started")
	}

	// Three more requests while the first scan is walking.
	for i := range 3 {
		err := s.StartScan(ctx)
		if errors.Is(err, ErrScanAlreadyRunning) {
			t.Fatalf("request %d during a running scan was refused instead of queued: %v", i+1, err)
		}
	}
	close(release)

	deadline := time.After(5 * time.Second)
	for runs.Load() < 2 || s.scanRunning.Load() {
		select {
		case <-deadline:
			t.Fatalf("follow-up scan did not run and finish: runs=%d running=%v", runs.Load(), s.scanRunning.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Give a wrongly queued third scan the chance to show up.
	time.Sleep(100 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("scans run = %d, want 2: the queued requests must coalesce into one follow-up", got)
	}
	if s.scanRunning.Load() {
		t.Fatal("the single-flight gate is still held after the follow-up scan")
	}
}
