package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// libraryDirs lists the directories under root, relative and sorted by walk
// order, so a test can assert on the shape of the library rather than on one
// guessed path.
func libraryDirs(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == root || !info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	return out
}

// #2504. The single-file audiobook branch created its destination directory
// and then had no cleanup on any error path, so a placement that failed left
// the empty directory behind. destDir comes from UniqueDir, which only stats
// for existence, so the next attempt read that empty directory as a collision
// and built "Title (2)" beside it. The reporter saw six books in one batch
// each with an empty original and a populated " (2)".
//
// The failure here is a source the copy cannot open, which is the same shape
// as the reporter's transient "operation not permitted": it fails after the
// directory exists and before anything lands in it.
func TestSingleFileAudiobook_FailedPlacementLeavesNoEmptyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	libraryDir := t.TempDir()
	s, dl, _, _, ctx := dataLossFixture(t, libraryDir, "copy")

	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatalf("write m4b: %v", err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o600) })

	// The download path is the file itself, not a folder holding it, which is
	// what sends this down the single-file branch. A folder goes through
	// CopyDirCtx, which already removes its destination on error.
	s.tryImportInternal(ctx, dl, src, "", "", "", nil, nil)

	for _, d := range libraryDirs(t, libraryDir) {
		entries, err := os.ReadDir(filepath.Join(libraryDir, d))
		if err != nil {
			t.Fatalf("read %s: %v", d, err)
		}
		if len(entries) == 0 && d != "Author A" {
			t.Errorf("failed import left an empty directory %q, which the next attempt reads as a collision", d)
		}
	}
}

// The symptom the orphan produces, end to end: the same book imported again
// after a failure must land in its own folder, not in a " (2)" beside an empty
// one.
func TestSingleFileAudiobook_RetryAfterFailureDoesNotDuplicateTheFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	libraryDir := t.TempDir()
	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "copy")

	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatalf("write m4b: %v", err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	s.tryImportInternal(ctx, dl, src, "", "", "", nil, nil)

	// The transient condition clears and the book is grabbed again. A fresh row
	// rather than a reset one: the first attempt ends in StateImportBlocked,
	// which the state machine will not send back to completed, so a real
	// recovery is a new grab.
	if err := os.Chmod(src, 0o600); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	retry := &models.Download{
		GUID:   "guid-dl-test-retry",
		Title:  dl.Title,
		BookID: dl.BookID,
		Status: models.StateCompleted,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, retry); err != nil {
		t.Fatalf("create retry download: %v", err)
	}
	s.tryImportInternal(ctx, retry, src, "", "", "", nil, nil)

	for _, d := range libraryDirs(t, libraryDir) {
		if filepath.Base(d) != "" && len(d) > 4 && d[len(d)-4:] == " (2)" {
			t.Errorf("retry created a duplicate folder %q beside the orphan the first attempt left", d)
		}
	}
}

// The other half of #2504: the per-file branch guarded its cleanup on
// len(placed) > 0, so a failure on the FIRST file skipped it entirely. That is
// precisely the case with nothing to weigh against removing the folder, since
// nothing had been placed in it, and it is what a permission error affecting
// every file looks like.
//
// Two files in separate subdirectories, so they share no strict subdir under
// the download root and the import goes per file. The first is unreadable.
func TestPerFileAudiobook_FailureOnTheFirstFileLeavesNoEmptyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	libraryDir := t.TempDir()
	s, dl, _, _, ctx := dataLossFixture(t, libraryDir, "copy")

	downloadDir := t.TempDir()
	first := filepath.Join(downloadDir, "disc1", "01.mp3")
	second := filepath.Join(downloadDir, "disc2", "02.mp3")
	for _, p := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.Chmod(first, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(first, 0o600) })

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, []string{first, second})

	for _, d := range libraryDirs(t, libraryDir) {
		entries, err := os.ReadDir(filepath.Join(libraryDir, d))
		if err != nil {
			t.Fatalf("read %s: %v", d, err)
		}
		if len(entries) == 0 && d != "Author A" {
			t.Errorf("failed import left an empty directory %q, which the next attempt reads as a collision", d)
		}
	}
}
