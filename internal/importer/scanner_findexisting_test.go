package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// findExistingFixture builds a Scanner with separate libraryDir / audiobookDir
// and seeds each one with a same-titled file under the same author folder so
// FindExisting must use the media-type hint to pick the right root.
func findExistingFixture(t *testing.T, libraryDir, audiobookDir string) *Scanner {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewHistoryRepo(database),
		libraryDir, audiobookDir, "", "", "",
	)
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindExisting_EbookPicksLibraryRoot covers the symmetric half of #488: an
// ebook entry with same-titled files in BOTH roots returns the libraryDir hit.
func TestFindExisting_EbookPicksLibraryRoot(t *testing.T) {
	libDir := t.TempDir()
	abDir := t.TempDir()
	ebookPath := filepath.Join(libDir, "Jane Doe", "Title - Jane Doe.epub")
	audioPath := filepath.Join(abDir, "Jane Doe", "Title - Jane Doe.m4b")
	writeFile(t, ebookPath)
	writeFile(t, audioPath)

	s := findExistingFixture(t, libDir, abDir)
	got := s.FindExisting(context.Background(), "Title", "Jane Doe", models.MediaTypeEbook)
	if got != ebookPath {
		t.Errorf("ebook lookup: got %q, want %q", got, ebookPath)
	}
}

// TestFindExisting_AudiobookPicksAudiobookRoot is the #488 case: an audiobook
// book row must NOT be matched against a same-titled ebook in libraryDir.
func TestFindExisting_AudiobookPicksAudiobookRoot(t *testing.T) {
	libDir := t.TempDir()
	abDir := t.TempDir()
	ebookPath := filepath.Join(libDir, "Jane Doe", "Title - Jane Doe.epub")
	audioPath := filepath.Join(abDir, "Jane Doe", "Title - Jane Doe.m4b")
	writeFile(t, ebookPath)
	writeFile(t, audioPath)

	s := findExistingFixture(t, libDir, abDir)
	got := s.FindExisting(context.Background(), "Title", "Jane Doe", models.MediaTypeAudiobook)
	if got != audioPath {
		t.Errorf("audiobook lookup: got %q, want %q", got, audioPath)
	}
}

// TestFindExisting_BothWalksAllRoots preserves the pre-fix behaviour for
// dual-format and unspecified entries: libraryDir is walked first, so an
// ebook in libraryDir wins over an audiobook in audiobookDir.
func TestFindExisting_BothWalksAllRoots(t *testing.T) {
	libDir := t.TempDir()
	abDir := t.TempDir()
	ebookPath := filepath.Join(libDir, "Jane Doe", "Title - Jane Doe.epub")
	audioPath := filepath.Join(abDir, "Jane Doe", "Title - Jane Doe.m4b")
	writeFile(t, ebookPath)
	writeFile(t, audioPath)

	s := findExistingFixture(t, libDir, abDir)
	got := s.FindExisting(context.Background(), "Title", "Jane Doe", models.MediaTypeBoth)
	if got != ebookPath {
		t.Errorf("both lookup: got %q, want %q", got, ebookPath)
	}

	gotEmpty := s.FindExisting(context.Background(), "Title", "Jane Doe", "")
	if gotEmpty != ebookPath {
		t.Errorf("empty media-type lookup: got %q, want %q", gotEmpty, ebookPath)
	}
}

// TestFindExisting_AudiobookFallsBackToLibraryDir covers the seannymurrs case
// (#454): audiobookDir is unset, so NewScanner aliases it to libraryDir, and
// the audiobook lookup must still find a file under that single root.
func TestFindExisting_AudiobookFallsBackToLibraryDir(t *testing.T) {
	libDir := t.TempDir()
	audioPath := filepath.Join(libDir, "Jane Doe", "Title - Jane Doe.m4b")
	writeFile(t, audioPath)

	s := findExistingFixture(t, libDir, "")
	got := s.FindExisting(context.Background(), "Title", "Jane Doe", models.MediaTypeAudiobook)
	if got != audioPath {
		t.Errorf("audiobook fallback lookup: got %q, want %q", got, audioPath)
	}
}
