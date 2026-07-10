package importer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

// singleFlightFixture wires a Scanner with a settings repo so tests can
// observe whether a scan actually ran (a completed scan writes the
// "library.lastScan" setting).
func singleFlightFixture(t *testing.T) (*Scanner, *db.SettingsRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	settings := db.NewSettingsRepo(database)
	s := NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	).WithSettings(settings)
	return s, settings, context.Background()
}

// TestScanLibrary_SingleFlight is the regression test for #1460: a scan
// request while another scan is in flight must be rejected (StartScan) or
// skipped (ScanLibrary, the cron path), never run concurrently.
func TestScanLibrary_SingleFlight(t *testing.T) {
	s, settings, ctx := singleFlightFixture(t)

	// Simulate an in-flight scan holding the single-flight flag.
	if !s.scanRunning.CompareAndSwap(false, true) {
		t.Fatal("fresh scanner unexpectedly has scanRunning set")
	}

	// Manual path: a second StartScan must be rejected with the sentinel.
	if err := s.StartScan(ctx); !errors.Is(err, ErrScanAlreadyRunning) {
		t.Fatalf("StartScan during in-flight scan: got %v, want ErrScanAlreadyRunning", err)
	}

	// Cron path: ScanLibrary must skip — it returns without scanning, so no
	// lastScan result may be written and the flag stays held by the "other"
	// scan.
	s.ScanLibrary(ctx)
	if setting, err := settings.Get(ctx, "library.lastScan"); err != nil {
		t.Fatal(err)
	} else if setting != nil {
		t.Fatalf("ScanLibrary ran concurrently with an in-flight scan: lastScan = %q", setting.Value)
	}
	if !s.scanRunning.Load() {
		t.Fatal("skipped ScanLibrary released a flag it did not acquire")
	}

	// Release the simulated scan; the guard must now admit a scan again and
	// release itself on completion.
	s.scanRunning.Store(false)
	if err := s.StartScan(ctx); err != nil {
		t.Fatalf("StartScan after release: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		setting, err := settings.Get(ctx, "library.lastScan")
		if err != nil {
			t.Fatal(err)
		}
		if setting != nil && !s.scanRunning.Load() {
			break // scan ran to completion and released the guard
		}
		select {
		case <-deadline:
			t.Fatalf("background scan did not complete and release the guard (lastScan set: %v, running: %v)",
				setting != nil, s.scanRunning.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
