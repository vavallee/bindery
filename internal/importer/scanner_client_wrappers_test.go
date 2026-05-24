package importer

// Smoke tests for the tryImportNZBGet / tryImportSABnzbd / tryImportTransmission
// wrapper functions. These wrappers were modified in #766 to pass a formatHint
// argument to tryImportInternal; the tests below verify that each wrapper
// delegates to tryImportInternal without panicking. The import is directed at
// an empty temporary directory so tryImportInternal returns early ("no book
// files found") — no live download client is required because the cleanup
// closure is only invoked after a fully successful import.

import (
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
	s.tryImportTransmission(ctx, dl, t.TempDir())
}
