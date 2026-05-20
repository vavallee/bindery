package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestRecoverInterruptedImports_SweepsWedgedImporting is the regression test
// for issue #706 finding 1: a download wedged in the non-terminal
// StateImporting (process crashed mid-move) must be swept back to
// StateImportFailed on startup so the scanner's retry path can pick it up.
func TestRecoverInterruptedImports_SweepsWedgedImporting(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	dlRepo := db.NewDownloadRepo(database)
	histRepo := db.NewHistoryRepo(database)
	s := NewScanner(dlRepo, db.NewDownloadClientRepo(database),
		db.NewBookRepo(database), db.NewAuthorRepo(database), histRepo,
		t.TempDir(), "", "", "", "")

	// A download a prior process left mid-import.
	wedged := &models.Download{GUID: "wedged", Title: "Crashed Mid-Move",
		NZBURL: "x", Status: models.StateImporting}
	if err := dlRepo.Create(ctx, wedged); err != nil {
		t.Fatal(err)
	}
	// An untouched terminal download — must not be disturbed.
	done := &models.Download{GUID: "done", Title: "Already Imported",
		NZBURL: "x", Status: models.StateImported}
	if err := dlRepo.Create(ctx, done); err != nil {
		t.Fatal(err)
	}

	s.RecoverInterruptedImports(ctx)

	got, err := dlRepo.GetByGUID(ctx, "wedged")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("wedged download status = %q, want %q — startup sweep must re-queue it for retry",
			got.Status, models.StateImportFailed)
	}
	if got.ErrorMessage == "" {
		t.Error("expected an actionable error message on the recovered download")
	}

	gotDone, err := dlRepo.GetByGUID(ctx, "done")
	if err != nil {
		t.Fatal(err)
	}
	if gotDone.Status != models.StateImported {
		t.Errorf("terminal download status = %q, want %q — sweep must not touch it",
			gotDone.Status, models.StateImported)
	}

	// The sweep must emit a history event so the recovery is visible.
	events, err := histRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType == models.HistoryEventImportFailed {
			found = true
		}
	}
	if !found {
		t.Error("expected an importFailed history event for the recovered download")
	}
}

// TestTryImportInternal_RetryIsIdempotent is the regression test for issue #706
// finding 2: re-running an import whose book file already landed on disk and was
// recorded in book_files — but which crashed before the terminal status was
// written — must NOT re-import the file or add a duplicate book_files row. The
// retry must short-circuit straight to StateImported.
func TestTryImportInternal_RetryIsIdempotent(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	src := filepath.Join(downloadPath, "book.epub")
	if err := os.WriteFile(src, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// copy mode: the source survives the import, so a retry walks the same
	// download folder again — the exact case where a double-add can occur.
	s, dl, dlRepo, bookRepo, ctx := dataLossFixture(t, libraryDir, "copy")

	// First import: succeeds normally and writes the book_files row.
	s.tryImportInternal(ctx, dl, downloadPath, "transmission", "tor-1", nil)

	filesAfterFirst, err := bookRepo.ListFiles(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(filesAfterFirst) != 1 {
		t.Fatalf("after first import: %d book_files rows, want 1", len(filesAfterFirst))
	}
	destPath := filesAfterFirst[0].Path

	// Reproduce the post-crash state precisely: the book_files row + the file on
	// disk survive (the first import wrote them), but the download crashed before
	// the terminal status was set, so the startup sweep re-queued it to
	// StateImportFailed. The download record is non-terminal again and a retry is
	// about to run. (We cannot reuse the StateImported row from above — that
	// transition is terminal — so build the row directly in the post-sweep state,
	// matching exactly what RecoverInterruptedImports produces.)
	retryDL := &models.Download{
		GUID:   "guid-retry-idem",
		Title:  dl.Title,
		BookID: dl.BookID,
		Status: models.StateImportFailed,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, retryDL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("precondition: imported file should still be on disk: %v", err)
	}

	s.tryImportInternal(ctx, retryDL, downloadPath, "transmission", "tor-1", nil)

	// The idempotency guard must short-circuit: no second book_files row.
	filesAfterRetry, err := bookRepo.ListFiles(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(filesAfterRetry) != 1 {
		t.Errorf("after retry: %d book_files rows, want 1 — the retry double-added the file", len(filesAfterRetry))
	}

	// And the retry must still drive the download to the terminal imported state.
	gotRetry, err := dlRepo.GetByGUID(ctx, retryDL.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRetry.Status != models.StateImported {
		t.Errorf("after retry status = %q, want %q — an idempotent retry must still finalise the download",
			gotRetry.Status, models.StateImported)
	}
}

// TestBlockStaleImportFailures_VanishedSourceEndsBlocked is the regression test
// for issue #706 finding 4: a StateImportFailed download whose source is no
// longer present in the client's (complete) source list must transition to the
// terminal StateImportBlocked rather than sitting in StateImportFailed forever
// below the retry limit.
func TestBlockStaleImportFailures_VanishedSourceEndsBlocked(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	s := NewScanner(dlRepo, clientRepo,
		db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "")

	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 8080}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	clientID := client.ID

	// A failed import that is still below the retry limit — its only problem is
	// that the torrent has been removed from the client.
	vanished := &models.Download{GUID: "vanished", Title: "Torrent Removed",
		NZBURL: "x", Status: models.StateImportFailed, DownloadClientID: &clientID}
	if err := dlRepo.Create(ctx, vanished); err != nil {
		t.Fatal(err)
	}

	// seenSourceIDs is empty: the torrent did not appear in this poll cycle.
	// sourceListIsComplete=true because a torrent client enumerates every
	// torrent, so a missing entry definitively means the source is gone.
	s.blockStaleImportFailures(ctx, map[int64]bool{}, true, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == clientID
	})

	got, err := dlRepo.GetByGUID(ctx, "vanished")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Errorf("vanished-source download status = %q, want %q — a download whose source is gone must end terminally blocked, not stuck",
			got.Status, models.StateImportBlocked)
	}
	if got.ErrorMessage == "" {
		t.Error("expected an actionable error message explaining the source is gone")
	}
}

// TestBlockStaleImportFailures_RetryExhaustedEndsBlocked verifies the
// retry-budget half of issue #706 finding 4: a StateImportFailed download whose
// source is STILL present but whose retry count is exhausted must be terminally
// blocked rather than left stuck.
func TestBlockStaleImportFailures_RetryExhaustedEndsBlocked(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	s := NewScanner(dlRepo, clientRepo,
		db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "")

	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 8080}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	clientID := client.ID

	exhausted := &models.Download{GUID: "exhausted", Title: "Retries Burned",
		NZBURL: "x", Status: models.StateImportFailed, DownloadClientID: &clientID}
	if err := dlRepo.Create(ctx, exhausted); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < importRetryLimit; i++ {
		if err := dlRepo.IncrementImportRetryCount(ctx, exhausted.ID); err != nil {
			t.Fatal(err)
		}
	}

	// The source IS still present (seenSourceIDs contains it) — only the retry
	// budget is spent. It must still be blocked.
	s.blockStaleImportFailures(ctx, map[int64]bool{exhausted.ID: true}, true,
		func(d models.Download) bool {
			return d.DownloadClientID != nil && *d.DownloadClientID == clientID
		})

	got, err := dlRepo.GetByGUID(ctx, "exhausted")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Errorf("retry-exhausted download status = %q, want %q",
			got.Status, models.StateImportBlocked)
	}
}

// TestTryImportInternal_ExternalModeParksNonTerminal is the regression test for
// issue #706 finding 3: an external-mode hand-off must leave the download in the
// NON-terminal StateImportExternal (so searchWanted can skip it and ScanLibrary
// can reconcile the file) rather than jumping to terminal StateImported, which
// caused a silent re-download loop.
func TestTryImportInternal_ExternalModeParksNonTerminal(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, dlRepo, bookRepo, ctx := dataLossFixture(t, libraryDir, "external")

	s.tryImportInternal(ctx, dl, downloadPath, "transmission", "tor-1", nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportExternal {
		t.Errorf("external-mode download status = %q, want %q — must be parked non-terminal, not terminal-imported",
			got.Status, models.StateImportExternal)
	}

	// The book must stay Wanted so ScanLibrary can reconcile the file once the
	// external tool places it (ScanLibrary only reconciles Wanted books).
	book, err := bookRepo.GetByID(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want %q — external hand-off must leave the book reconcilable",
			book.Status, models.BookStatusWanted)
	}
}
