package importer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// seedDiscSplitAudiobook writes one track into each of the discs subfolders
// under <libDir>/<author>/<title>/ and returns the book folder and the disc
// folders. The tracks carry unreadable tags, so the scan falls back to the
// folder hierarchy — the layout the reporter has (#2723).
func seedDiscSplitAudiobook(t *testing.T, libDir, author, title string, discs int) (bookDir string, discDirs []string) {
	t.Helper()
	bookDir = filepath.Join(libDir, author, title)
	for d := 1; d <= discs; d++ {
		discDir := filepath.Join(bookDir, fmt.Sprintf("CD%d", d))
		writeFileAt(t, filepath.Join(discDir, fmt.Sprintf("%s - %02d.mp3", title, d)))
		discDirs = append(discDirs, discDir)
	}
	return bookDir, discDirs
}

// TestScanLibrary_DiscSplitAudiobookReconcilesOneBook is the #2723 regression:
// an audiobook split across CD subfolders must reconcile as the one book its
// folder names. Before the fix the disc folder was taken for the book folder,
// so every track parsed to "CD1"/"CD2", matched nothing (reason
// no_title_match), and the second disc was dropped by the one-audiobook-per-
// book claim even when the first disc matched.
func TestScanLibrary_DiscSplitAudiobookReconcilesOneBook(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "OL-at-disc", Name: "Amy Tan", SortName: "Tan, Amy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-kgw-disc", AuthorID: author.ID, Title: "The Kitchen God's Wife", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	bookDir, discDirs := seedDiscSplitAudiobook(t, libraryDir, "Amy Tan", "The Kitchen God's Wife", 2)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Clean(files[0].Path) != filepath.Clean(bookDir) {
		got := make([]string, 0, len(files))
		for _, f := range files {
			got = append(got, f.Path)
		}
		t.Fatalf("disc-split audiobook must be one row for the book folder %q, got %d row(s): %v", bookDir, len(files), got)
	}
	if files[0].Format != models.MediaTypeAudiobook {
		t.Errorf("folder row format = %q, want %q", files[0].Format, models.MediaTypeAudiobook)
	}
	// The recorded folder is the book, so both discs' tracks move and delete
	// with it.
	for _, disc := range discDirs {
		if !pathUnderDir(disc, filepath.Clean(files[0].Path)) {
			t.Errorf("recorded folder %q must cover disc %q", files[0].Path, disc)
		}
	}
	// The second disc used to be reported unmatched once the first claimed the
	// book; a reconciled disc set leaves nothing behind.
	if leftover := readUnmatchedFiles(t, ctx, s); len(leftover) != 0 {
		t.Errorf("reconciled disc set must leave nothing unmatched, got %d: %+v", len(leftover), leftover)
	}
}

// TestScanLibrary_SingleDiscFolderReconcilesToBookFolder is the same shape with
// one disc folder: a "CD1" is still a disc folder, so the book is the folder
// above it. Before the fix a single-disc audiobook in that shape was unmatched
// for the same reason as the two-disc one.
func TestScanLibrary_SingleDiscFolderReconcilesToBookFolder(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "OL-at-single", Name: "Amy Tan", SortName: "Tan, Amy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-kgw-single", AuthorID: author.ID, Title: "The Kitchen God's Wife", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	bookDir, _ := seedDiscSplitAudiobook(t, libraryDir, "Amy Tan", "The Kitchen God's Wife", 1)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Clean(files[0].Path) != filepath.Clean(bookDir) {
		got := make([]string, 0, len(files))
		for _, f := range files {
			got = append(got, f.Path)
		}
		t.Fatalf("single-disc audiobook must be recorded as its book folder %q, got %d row(s): %v", bookDir, len(files), got)
	}
}

// TestScanLibrary_FlatBookFolderUnchanged guards the #2719 boundary: a book
// whose tracks sit directly in its own folder is still recorded as that folder,
// with no disc folder involved.
func TestScanLibrary_FlatBookFolderUnchanged(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "OL-at-flat", Name: "Amy Tan", SortName: "Tan, Amy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-kgw-flat", AuthorID: author.ID, Title: "The Kitchen God's Wife", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	bookDir := filepath.Join(libraryDir, "Amy Tan", "The Kitchen God's Wife")
	writeFileAt(t, filepath.Join(bookDir, "The Kitchen God's Wife - 01.mp3"))

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Clean(files[0].Path) != filepath.Clean(bookDir) {
		got := make([]string, 0, len(files))
		for _, f := range files {
			got = append(got, f.Path)
		}
		t.Fatalf("flat book folder must be recorded as %q, got %d row(s): %v", bookDir, len(files), got)
	}
}
