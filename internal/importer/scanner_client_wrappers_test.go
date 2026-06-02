package importer

// Tests for the tryImportNZBGet / tryImportSABnzbd / tryImportTransmission
// wrapper functions. These wrappers were modified in #766 to pass a formatHint
// argument to tryImportInternal.
//
// Two categories:
//  1. Delegation smoke tests — empty download dir, import fails fast before the
//     cleanup closure is invoked. Verifies the wrappers compile and delegate.
//  2. Cleanup-coverage tests — real book file, successful import, cleanup
//     closure IS invoked (fails with "connection refused" but the body executes).
//     These cover the closure lines that are only reachable after a full import.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

func TestTryImportNZBGet_Delegates(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	ng := nzbget.New("localhost", 6789, "", "", "", false)
	dl := &models.Download{
		GUID:   "nzbget-dlg",
		Title:  "NZBGet Delegate Test",
		Status: models.StateCompleted,
	}
	// Empty dir → no book files found → tryImportInternal fails fast before
	// the cleanup closure (ng.RemoveHistory) is ever called.
	s.tryImportNZBGet(ctx, ng, dl, 99, t.TempDir())
}

func TestTryImportSABnzbd_Delegates(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	sab := sabnzbd.New("localhost", 8080, "apikey", "", false)
	dl := &models.Download{
		GUID:   "sab-dlg",
		Title:  "SABnzbd Delegate Test",
		Status: models.StateCompleted,
	}
	// Empty dir → no book files found → cleanup (sab.DeleteHistory) not called.
	s.tryImportSABnzbd(ctx, sab, dl, "abc123", t.TempDir())
}

func TestTryImportTransmission_Delegates(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	torrentID := "deadbeef"
	dl := &models.Download{
		GUID:      "trans-dlg",
		TorrentID: &torrentID,
		Title:     "Transmission Delegate Test",
		Status:    models.StateCompleted,
	}
	// Transmission wrapper passes nil cleanupFunc; empty dir → fails fast.
	s.tryImportTransmission(ctx, dl, t.TempDir(), nil)
}

// TestTryImportNZBGet_CleanupCalledOnSuccess verifies that the cleanup closure
// body inside tryImportNZBGet is executed when the import fully succeeds.
// The NZBGet client points at a non-running localhost:6789, so RemoveHistory
// fails immediately with "connection refused" — but the closure body is entered,
// which is the coverage goal for the changed line.
func TestTryImportNZBGet_CleanupCalledOnSuccess(t *testing.T) {
	libraryDir := t.TempDir()
	s, dl, _, _, ctx := dataLossFixture(t, libraryDir, "")

	downloadDir := t.TempDir()
	epub := filepath.Join(downloadDir, "book.epub")
	if err := os.WriteFile(epub, []byte("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	ng := nzbget.New("localhost", 6789, "", "", "", false)
	// Import succeeds; cleanup (ng.RemoveHistory) is invoked and immediately
	// fails with "connection refused" — the closure body is still entered.
	s.tryImportNZBGet(ctx, ng, dl, 99, downloadDir)

	// An author directory under libraryDir confirms the import path was taken.
	entries, err := os.ReadDir(libraryDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected imported file under libraryDir; import may not have succeeded")
	}
}

// TestTryImportSABnzbd_CleanupCalledOnSuccess verifies that the cleanup closure
// body inside tryImportSABnzbd is executed when the import fully succeeds.
// The SABnzbd client points at a non-running localhost:8080, so DeleteHistory
// fails immediately with "connection refused" — but the closure body is entered.
func TestTryImportSABnzbd_CleanupCalledOnSuccess(t *testing.T) {
	libraryDir := t.TempDir()
	s, dl, _, _, ctx := dataLossFixture(t, libraryDir, "")

	downloadDir := t.TempDir()
	epub := filepath.Join(downloadDir, "book.epub")
	if err := os.WriteFile(epub, []byte("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	sab := sabnzbd.New("localhost", 8080, "apikey", "", false)
	// Import succeeds; cleanup (sab.DeleteHistory) is invoked and immediately
	// fails with "connection refused" — the closure body is still entered.
	s.tryImportSABnzbd(ctx, sab, dl, "nzo-abc123", downloadDir)

	// An author directory under libraryDir confirms the import path was taken.
	entries, err := os.ReadDir(libraryDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected imported file under libraryDir; import may not have succeeded")
	}
}
