package importer

// Regression test for a nil-pointer panic in the audiobook import branch of
// tryImportInternal. When a completed download is detected as an audiobook
// (any file has an audio extension) but no book row resolves — the download
// has no BookID, the lookup errored, or the row was deleted between grab and
// import — the branch computed AudiobookDestDir, which dereferences
// book.ReleaseDate/Title in renamer.apply and panicked the scan goroutine.
//
// The ebook branch already treats book == nil as "unmatched, fail with an
// actionable status"; the audiobook branch must do the same rather than crash.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestTryImportInternal_UnmatchedAudiobookDoesNotPanic(t *testing.T) {
	libraryDir := t.TempDir()
	s, _, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "")

	// A download directory containing a lone .m4b so detectDownloadFormat
	// returns audiobook and the path exists (we get past the stat / file-walk).
	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "book.m4b"), []byte("audio"), 0o600); err != nil {
		t.Fatalf("write m4b: %v", err)
	}

	// A completed download with NO BookID — book resolves to nil.
	dl := &models.Download{
		GUID:   "guid-unmatched-audiobook",
		Title:  "Some Unmatched Audiobook",
		Status: models.StateCompleted,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	// Before the fix this panicked on the nil book deref inside
	// AudiobookDestDir -> renamer.apply. It must now return cleanly.
	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("unmatched audiobook must end in StateImportFailed (mirroring the ebook branch), got %q", got.Status)
	}
}
