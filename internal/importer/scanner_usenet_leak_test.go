package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestTryImportInternal_UsenetEbookPrunesJobFolder reproduces cleb's report:
// a single-file ebook grabbed via SABnzbd (import.mode=hardlink, which #1542
// remaps to move for usenet) imports cleanly, but the now-empty job folder is
// left behind under complete/ebooks/. It must be pruned like the audiobook
// path already does.
func TestTryImportInternal_UsenetEbookPrunesJobFolder(t *testing.T) {
	libraryDir := t.TempDir()
	completeDir := t.TempDir()
	jobDir := filepath.Join(completeDir, "Natalie Haynes - Stone Blind (epub)")
	if err := os.MkdirAll(jobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	epubSrc := filepath.Join(jobDir, "Stone Blind.epub")
	if err := os.WriteFile(epubSrc, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// import.mode=hardlink; the SABnzbd client type must remap it to move.
	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "hardlink")

	// cleanupClientType "sabnzbd" is what triggers effectiveConfiguredMode's
	// usenet remap.
	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("download status = %q, want imported", got.Status)
	}

	// The empty job folder must be gone.
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("usenet ebook job folder left behind (empty-dir leak): stat err = %v", err)
	}
	// The parent complete/ dir must survive (shared category root).
	if _, err := os.Stat(completeDir); err != nil {
		t.Errorf("complete dir must not be pruned: %v", err)
	}
}

// TestTryImportInternal_UsenetEbookFilePath_PrunesJobFolder covers the case
// where the completed-job path resolves to the single ebook FILE rather than
// its containing folder. cleanupMovedSources must still prune the now-empty
// parent job folder.
func TestTryImportInternal_UsenetEbookFilePath_PrunesJobFolder(t *testing.T) {
	libraryDir := t.TempDir()
	completeDir := t.TempDir()
	jobDir := filepath.Join(completeDir, "Natalie Haynes - Stone Blind (epub)")
	if err := os.MkdirAll(jobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	epubSrc := filepath.Join(jobDir, "Stone Blind.epub")
	if err := os.WriteFile(epubSrc, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "hardlink")

	// downloadPath is the FILE itself (single-file job), not the job folder.
	s.tryImportInternal(ctx, dl, epubSrc, "sabnzbd", "nzo-1", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("download status = %q, want imported", got.Status)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("empty job folder left behind when downloadPath is the file: stat err = %v", err)
	}
	if _, err := os.Stat(completeDir); err != nil {
		t.Errorf("complete dir must survive: %v", err)
	}
}

// TestCleanupMovedSources_FilePathPrunesParentButKeepsSiblings verifies the
// single-file-job fix at the unit level: when downloadPath is the file itself,
// its parent job folder is pruned when empty — but a sibling in that folder
// (or in the folder above) keeps the relevant directory intact.
func TestCleanupMovedSources_FilePathPrunesParent(t *testing.T) {
	s := &Scanner{libraryDir: t.TempDir()}
	category := t.TempDir() // e.g. complete/ebooks
	jobDir := filepath.Join(category, "Some Book (epub)")
	if err := os.MkdirAll(jobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(jobDir, "book.epub")
	// Model post-move state: file already moved away.

	// downloadPath is the FILE.
	s.cleanupMovedSources(file, []string{file})

	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("empty job folder should be pruned when downloadPath is a file, stat err = %v", err)
	}
	// The category dir above the job folder must never be pruned.
	if _, err := os.Stat(category); err != nil {
		t.Errorf("category dir above the job folder must survive: %v", err)
	}
}

// TestCleanupMovedSources_FilePathKeepsNonEmptyParent ensures the file-path
// branch still respects the non-empty-directory guard: a job folder holding a
// leftover sibling (e.g. a .nfo) is kept, never force-removed.
func TestCleanupMovedSources_FilePathKeepsNonEmptyParent(t *testing.T) {
	s := &Scanner{libraryDir: t.TempDir()}
	jobDir := t.TempDir()
	file := filepath.Join(jobDir, "book.epub")
	sibling := filepath.Join(jobDir, "extra.nfo")
	if err := os.WriteFile(sibling, []byte("nfo"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.cleanupMovedSources(file, []string{file})

	if _, err := os.Stat(jobDir); err != nil {
		t.Errorf("job folder with a leftover sibling must be kept: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling file must survive: %v", err)
	}
}
