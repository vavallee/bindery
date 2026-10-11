package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A torrent client saving into a hidden folder inside the library
// (/data/audiobooks/.torrents, so imports can hardlink and the torrents keep
// seeding) doubled the library scan: every torrent copy was found again and
// listed as an unmatched book, one click from being imported or moved out of
// the client's folder. Hidden folders, folders holding a .binderyignore, and
// the download folders are not library content.

// skipDirsLibrary holds three books in the library proper and copies of them
// in every kind of folder the scan must leave alone. It returns the library
// and the non-hidden download folder inside it.
func skipDirsLibrary(t *testing.T, libraryDir string) (downloads string) {
	t.Helper()
	book := func(dir, title, author string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, title+".epub")
		writeEpubAt(t, p, title, author, "")
		return p
	}
	for _, b := range []struct{ title, author string }{
		{"Fjellvinden", "Kari Nordmann"}, {"Havgapet", "Kari Nordmann"}, {"Skogens sang", "Ola Nordmann"},
	} {
		book(filepath.Join(libraryDir, b.author), b.title, b.author)
		// The torrent client's copies, a trash folder, a hidden folder
		// further down, the download folder and an ignored folder.
		book(filepath.Join(libraryDir, ".torrents", b.title), b.title, b.author)
		book(filepath.Join(libraryDir, ".Trash-1000", "files"), b.title, b.author)
		book(filepath.Join(libraryDir, b.author, ".incomplete"), b.title, b.author)
		book(filepath.Join(libraryDir, "downloads", b.title), b.title, b.author)
		book(filepath.Join(libraryDir, "Scratch", "drafts"), b.title, b.author)
	}
	if err := os.WriteFile(filepath.Join(libraryDir, "Scratch", LibraryIgnoreFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(libraryDir, "downloads")
}

func TestScanLibrary_SkipsHiddenIgnoredAndDownloadFolders(t *testing.T) {
	s, _, _, _, settings, libraryDir, ctx := unmatchedFixture(t)
	downloads := skipDirsLibrary(t, libraryDir)
	s.WithDownloadDir(downloads)
	repo := unitRepoOf(t, s)
	// What an earlier scan listed from the torrent folder.
	stale := filepath.Join(libraryDir, ".torrents", "Fjellvinden", "Fjellvinden.epub")
	seedPendingUnit(t, ctx, repo, stale)

	s.ScanLibrary(ctx)

	if b := readUnmatchedBlob(t, ctx, settings); b.FilesFound != 3 {
		t.Errorf("files_found = %d, want the 3 library books and none of their copies", b.FilesFound)
	}
	for path := range pendingPaths(t, ctx, repo) {
		rel, _ := filepath.Rel(libraryDir, path)
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, string(filepath.Separator)+".") ||
			strings.HasPrefix(rel, "downloads") || strings.HasPrefix(rel, "Scratch") {
			t.Errorf("unmatched list holds %q, from a folder the scan must skip", rel)
		}
	}
	if pendingPaths(t, ctx, repo)[stale] {
		t.Error("the unmatched entry an earlier scan listed from .torrents is still there")
	}
}

// The "already in the library" check walks the same way, so a book whose
// only copy is a torrent's or a download's is not taken as owned.
func TestLibrarySnapshot_SkipsHiddenIgnoredAndDownloadFolders(t *testing.T) {
	libraryDir := t.TempDir()
	skipDirsLibrary(t, libraryDir)
	// The snapshot matches a book through its top-level author folder, so the
	// copies sit under one: a hidden folder, a download folder, an ignored one.
	author := filepath.Join(libraryDir, "Kari Nordmann")
	incoming := filepath.Join(author, "incoming")
	for _, dir := range []string{filepath.Join(author, ".partial"), incoming, filepath.Join(author, "Kladd")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeEpubAt(t, filepath.Join(dir, "Nattseileren.epub"), "Nattseileren", "Kari Nordmann", "")
	}
	if err := os.WriteFile(filepath.Join(author, "Kladd", LibraryIgnoreFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	snap := NewLibrarySnapshot(libraryDir, "").WithExcluded(incoming)
	ctx := context.Background()
	if got := snap.FindExisting(ctx, "Nattseileren", "Kari Nordmann", models.MediaTypeEbook); got != "" {
		t.Errorf("FindExisting = %q, want nothing: the book exists only as copies in skipped folders", got)
	}
	want := filepath.Join(author, "Fjellvinden.epub")
	if got := snap.FindExisting(ctx, "Fjellvinden", "Kari Nordmann", models.MediaTypeEbook); got != want {
		t.Errorf("FindExisting = %q, want the library copy %q", got, want)
	}
}

// The configured root is walked even when its own name starts with a dot,
// and a download folder that IS the library root does not hide the library.
func TestWalkRoot_RootItselfIsNeverSkipped(t *testing.T) {
	for name, excludeRoot := range map[string]bool{"hidden root": false, "root is the download folder": true} {
		root := filepath.Join(t.TempDir(), ".library")
		if err := os.MkdirAll(filepath.Join(root, "Kari Nordmann"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, LibraryIgnoreFile), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		book := filepath.Join(root, "Kari Nordmann", "Fjellvinden.epub")
		writeEpubAt(t, book, "Fjellvinden", "Kari Nordmann", "")
		var excluded []string
		if excludeRoot {
			excluded = []string{root}
		}
		found := false
		_ = walkRoot(root, excluded, func(path string, info os.FileInfo, err error) error {
			if path == book {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("%s: the book under the root was not walked", name)
		}
	}
}

// An excluded download folder named through a symlink is still recognised
// when the walk reaches it under its real path.
func TestWalkRoot_ExcludedFolderThroughASymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "downloads")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, filepath.Join(real, "Fjellvinden.epub"), "Fjellvinden", "Kari Nordmann", "")
	link := filepath.Join(t.TempDir(), "dl")
	symlinkOrSkip(t, real, link)

	_ = walkRoot(root, []string{link}, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(path, ".epub") {
			t.Errorf("walked %q inside the excluded download folder", path)
		}
		return nil
	})
}
