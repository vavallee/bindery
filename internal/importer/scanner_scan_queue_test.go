package importer

import (
	"encoding/json"
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

// TestScanLibrary_CronScanRunsAManualRequestQueuedDuringIt: a scheduled scan
// is walking, a manual request is queued behind it, and a second scheduled
// tick skips. Exactly one follow-up runs, from the scheduled scan's own
// goroutine, and its result carries the id the manual request was given.
func TestScanLibrary_CronScanRunsAManualRequestQueuedDuringIt(t *testing.T) {
	s, settings, ctx := singleFlightFixture(t)

	var runs atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	s.testScanHook = func() {
		if runs.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
	}

	cronDone := make(chan struct{})
	go func() {
		s.ScanLibrary(ctx)
		close(cronDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled scan never started")
	}

	id, err := s.StartScanTracked(ctx)
	if !errors.Is(err, ErrScanQueued) || id == "" {
		t.Fatalf("manual request during a scheduled scan: id=%q err=%v, want an id and ErrScanQueued", id, err)
	}
	if running, queued := s.ScanState(); !running || !queued {
		t.Fatalf("ScanState = running %v queued %v, want both", running, queued)
	}

	// A second scheduled tick while the first is still walking skips.
	s.ScanLibrary(ctx)
	if got := runs.Load(); got != 1 {
		t.Fatalf("the second scheduled tick ran a scan beside the first: runs=%d", got)
	}

	close(release)
	select {
	case <-cronDone:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled scan did not return")
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("scans run = %d, want 2: the scheduled scan plus the queued follow-up", got)
	}
	if running, queued := s.ScanState(); running || queued {
		t.Fatalf("ScanState after the follow-up = running %v queued %v, want neither", running, queued)
	}
	setting, err := settings.Get(ctx, "library.lastScan")
	if err != nil || setting == nil {
		t.Fatalf("no scan result: %v", err)
	}
	var result struct {
		ScanID string `json:"scan_id"`
	}
	if err := json.Unmarshal([]byte(setting.Value), &result); err != nil {
		t.Fatal(err)
	}
	if result.ScanID != id {
		t.Errorf("last result scan_id = %q, want the id the manual request was given, %q", result.ScanID, id)
	}
}
