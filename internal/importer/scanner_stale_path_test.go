package importer

// #2186: a library that broke before the derivation fix shipped has no pending
// AddBookFile to trigger a re-derivation — the moved file is already tracked,
// so the scan records it as such and never registers anything. That is why the
// reporter saw "no number of rescans corrects it". The scan's stale-path sweep
// is what makes a rescan correct it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_RepairsBookRenderingAVanishedPath builds the pre-fix state
// directly — two registered rows, both live at registration time, then the
// first file disappears — and asserts a scan moves the book onto the row that
// still resolves.
func TestScanLibrary_RepairsBookRenderingAVanishedPath(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	authorDir := filepath.Join(libDir, "Jane Doe")
	oldDir := filepath.Join(authorDir, "Old Folder")
	newDir := filepath.Join(authorDir, "New Folder")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := filepath.Join(oldDir, "Alpha Adventure.epub")
	newPath := filepath.Join(newDir, "Alpha Adventure.epub")
	for _, p := range []string{oldPath, newPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure",
		models.BookStatusImported, models.MediaTypeEbook)
	// Both paths resolve at registration time, so the older row wins — exactly
	// the state a pre-fix install is left in after a folder rename.
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, newPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		t.Fatal(err)
	}

	before, err := books.GetByID(ctx, book.ID)
	if err != nil || before == nil {
		t.Fatalf("GetByID before scan: %v", err)
	}
	if before.EbookFilePath != oldPath {
		t.Fatalf("fixture did not reproduce the broken state: EbookFilePath = %q, want %q",
			before.EbookFilePath, oldPath)
	}

	s.ScanLibrary(ctx)

	after, err := books.GetByID(ctx, book.ID)
	if err != nil || after == nil {
		t.Fatalf("GetByID after scan: %v", err)
	}
	if after.EbookFilePath != newPath {
		t.Errorf("EbookFilePath after scan = %q, want the live path %q", after.EbookFilePath, newPath)
	}
	if after.Status != models.BookStatusImported {
		t.Errorf("Status after scan = %q, want imported", after.Status)
	}
	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("want both rows kept (the sweep does not delete), got %d", len(files))
	}
}

// TestScanLibrary_LeavesHealthyMultiFileBookAlone: a book with several formats
// that all resolve is the common case, and the sweep must not touch it.
func TestScanLibrary_LeavesHealthyMultiFileBookAlone(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(dir, "Alpha Adventure.epub")
	mobi := filepath.Join(dir, "Alpha Adventure.mobi")
	for _, p := range []string{epub, mobi} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure",
		models.BookStatusImported, models.MediaTypeEbook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, mobi); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	after, err := books.GetByID(ctx, book.ID)
	if err != nil || after == nil {
		t.Fatalf("GetByID after scan: %v", err)
	}
	if after.EbookFilePath != epub {
		t.Errorf("EbookFilePath after scan = %q, want the unchanged %q", after.EbookFilePath, epub)
	}
}

// TestRefreshStaleRenderedPaths_SurvivesAFailedLookup: the sweep runs after the
// scan has already done its real work, so a database error there has to be
// logged and swallowed rather than taken as a scan failure.
func TestRefreshStaleRenderedPaths_SurvivesAFailedLookup(t *testing.T) {
	s, _, _, _, _, ctx := trackedPathsFixture(t)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	// Must not panic and must return: the candidate query fails outright.
	s.refreshStaleRenderedPaths(cancelled)
}

// TestScanLibrary_RepairsBookRenderingALessPreferredFormat (#3467): a book
// imported before the format preference renders its azw3 although a live epub
// sits next to it. Both paths resolve, so the vanished-path check alone would
// leave it; the sweep also compares against the preferred file.
func TestScanLibrary_RepairsBookRenderingALessPreferredFormat(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	azw3 := filepath.Join(dir, "Alpha Adventure.azw3")
	epub := filepath.Join(dir, "Alpha Adventure.epub")
	if err := os.WriteFile(azw3, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure",
		models.BookStatusImported, models.MediaTypeEbook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, azw3); err != nil {
		t.Fatal(err)
	}
	// The epub is registered while its file is not there yet, which leaves the
	// azw3 rendered: the state an upgrade finds in an existing library.
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := books.GetByID(ctx, book.ID)
	if err != nil || before == nil || before.EbookFilePath != azw3 {
		t.Fatalf("fixture did not reproduce the old state: %+v, %v", before, err)
	}

	s.refreshStaleRenderedPaths(ctx)

	after, err := books.GetByID(ctx, book.ID)
	if err != nil || after == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.EbookFilePath != epub {
		t.Errorf("EbookFilePath after sweep = %q, want %q", after.EbookFilePath, epub)
	}
	if files, err := books.ListFiles(ctx, book.ID); err != nil || len(files) != 2 {
		t.Errorf("want both rows kept, got %d, %v", len(files), err)
	}
}
