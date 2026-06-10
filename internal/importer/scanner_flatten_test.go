package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// flattenScannerFixture extends dataLossFixture: it flips the seeded book to an
// audiobook, sets the import mode, optionally enables flattening, and builds a
// multi-disc download tree. It returns the Scanner, download, and the download
// path to import.
func flattenScannerFixture(t *testing.T, importMode string, flatten bool) (*Scanner, *models.Download, string) {
	t.Helper()
	libraryDir := t.TempDir()
	s, dl, _, bookRepo, ctx := dataLossFixture(t, libraryDir, importMode)

	book, err := bookRepo.GetByID(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	book.MediaType = models.MediaTypeAudiobook
	if err := bookRepo.Update(ctx, book); err != nil {
		t.Fatal(err)
	}

	if flatten {
		if err := s.settings.Set(ctx, "import.audiobook.flatten_multi_disc", "true"); err != nil {
			t.Fatal(err)
		}
	}

	download := buildMultiDiscDownload(t)
	return s, dl, download
}

// buildMultiDiscDownload creates a download folder with two disc subfolders and
// a cover, returning the folder path.
func buildMultiDiscDownload(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join("Disc 1", "Track 01.mp3"), "d1t1")
	mk(filepath.Join("Disc 1", "Track 02.mp3"), "d1t2")
	mk(filepath.Join("Disc 2", "Track 01.mp3"), "d2t1")
	mk(filepath.Join("Disc 2", "Track 02.mp3"), "d2t2")
	mk("cover.jpg", "img")
	return root
}

// importedAudiobookDir returns the on-disk audiobook destination recorded for
// the book after an import, or fails the test.
func importedAudiobookDir(t *testing.T, s *Scanner) string {
	t.Helper()
	// effectiveAudiobookDir falls back to libraryDir (set as audiobookDir by
	// NewScanner) → AudiobookDestDir = libraryDir/Author A/Title T. There is no
	// year, so the template yields "Title T ()". Resolve via the renamer to stay
	// in lockstep with production naming.
	author := &models.Author{Name: "Author A"}
	book := &models.Book{Title: "Title T"}
	dest, err := s.renamer.AudiobookDestDir(s.audiobookDir, author, book, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestScannerFlatten_CopyMode(t *testing.T) {
	s, dl, downloadPath := flattenScannerFixture(t, "copy", true)
	ctx := context.Background()

	s.tryImportInternal(ctx, dl, downloadPath, "", "", "", nil, nil)

	dest := importedAudiobookDir(t, s)
	assertFlatResult(t, dest)

	// Source must survive (seeding preserved).
	if _, err := os.Stat(filepath.Join(downloadPath, "Disc 1", "Track 01.mp3")); err != nil {
		t.Errorf("copy mode removed the source: %v", err)
	}
}

func TestScannerFlatten_HardlinkMode(t *testing.T) {
	s, dl, downloadPath := flattenScannerFixture(t, "hardlink", true)
	ctx := context.Background()

	s.tryImportInternal(ctx, dl, downloadPath, "", "", "", nil, nil)

	dest := importedAudiobookDir(t, s)
	assertFlatResult(t, dest)

	si, err := os.Stat(filepath.Join(downloadPath, "Disc 1", "Track 01.mp3"))
	if err != nil {
		t.Fatalf("hardlink mode removed the source: %v", err)
	}
	di, _ := os.Stat(filepath.Join(dest, "Part 001.mp3"))
	if !os.SameFile(si, di) {
		t.Error("hardlink scanner import did not share an inode with the source")
	}
}

// TestScannerFlatten_MoveModeNeverFlattens asserts the backend enforces the
// copy/hardlink restriction: with import mode "move" the multi-disc folder is
// moved whole (disc subfolders preserved), never flattened, even if the setting
// is on.
func TestScannerFlatten_MoveModeNeverFlattens(t *testing.T) {
	s, dl, downloadPath := flattenScannerFixture(t, "move", true)
	ctx := context.Background()

	s.tryImportInternal(ctx, dl, downloadPath, "", "", "", nil, nil)

	dest := importedAudiobookDir(t, s)
	// Disc subfolders must be preserved (no flattening in move mode).
	if _, err := os.Stat(filepath.Join(dest, "Disc 1", "Track 01.mp3")); err != nil {
		t.Errorf("move mode must preserve disc-folder layout, missing Disc 1/Track 01.mp3: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Part 001.mp3")); !os.IsNotExist(err) {
		t.Error("move mode must NOT produce flattened Part 001.mp3")
	}
}

// TestScannerFlatten_SettingOffPreservesLayout asserts that with the setting
// off, a multi-disc audiobook keeps its disc-folder layout in copy mode (no
// behaviour change for users who don't opt in).
func TestScannerFlatten_SettingOffPreservesLayout(t *testing.T) {
	s, dl, downloadPath := flattenScannerFixture(t, "copy", false)
	ctx := context.Background()

	s.tryImportInternal(ctx, dl, downloadPath, "", "", "", nil, nil)

	dest := importedAudiobookDir(t, s)
	if _, err := os.Stat(filepath.Join(dest, "Disc 1", "Track 01.mp3")); err != nil {
		t.Errorf("setting-off copy must preserve disc-folder layout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Part 001.mp3")); !os.IsNotExist(err) {
		t.Error("setting-off must NOT flatten")
	}
}

// TestScannerFlatten_SingleDiscUnchanged asserts a single-disc audiobook is not
// flattened even when the setting is on (only multi-disc shapes are targeted).
func TestScannerFlatten_SingleDiscUnchanged(t *testing.T) {
	s, dl, _ := flattenScannerFixture(t, "copy", true)
	ctx := context.Background()

	// Replace the download with a single-disc tree.
	single := t.TempDir()
	for _, n := range []string{"Track 01.mp3", "Track 02.mp3"} {
		dir := filepath.Join(single, "Disc 1")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.tryImportInternal(ctx, dl, single, "", "", "", nil, nil)

	dest := importedAudiobookDir(t, s)
	if _, err := os.Stat(filepath.Join(dest, "Disc 1", "Track 01.mp3")); err != nil {
		t.Errorf("single-disc must preserve layout even with flatten on: %v", err)
	}
}
