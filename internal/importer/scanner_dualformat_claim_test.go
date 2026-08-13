package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// dualFormatFixture creates a Wanted "both" book under an {Author}/{Title}/
// folder and returns the scanner, book repo, that folder, the book and ctx.
func dualFormatFixture(t *testing.T) (*Scanner, *db.BookRepo, string, *models.Book, context.Context) {
	t.Helper()
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:phm", AuthorID: author.ID, Title: "Project Hail Mary", SortTitle: "project hail mary",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeBoth, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(libraryDir, "Andy Weir", "Project Hail Mary")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return s, books, dir, book, ctx
}

// bookFilePaths returns the cleaned book_file paths recorded for a book,
// keyed by format.
func bookFileFormats(t *testing.T, books *db.BookRepo, ctx context.Context, bookID int64) map[string]string {
	t.Helper()
	files, err := books.ListFiles(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Format] = filepath.Clean(f.Path)
	}
	return out
}

// TestScanLibrary_DualFormatFolderAttachesBothInOnePass is the #1957
// regression: a folder holding both an epub and an m4b for the same book must
// attach BOTH in a single scan. The old guard claimed the book for whichever
// file the walk reached first and sent the other to Unmatched, so the
// documented recovery ("set the book to Both and scan again") needed a second
// full scan — and only worked at all if the second file's tags happened to
// parse into a matching author (#1956).
func TestScanLibrary_DualFormatFolderAttachesBothInOnePass(t *testing.T) {
	s, books, dir, book, ctx := dualFormatFixture(t)

	epub := filepath.Join(dir, "Project Hail Mary.epub")
	writeEpubAt(t, epub, "Project Hail Mary", "Andy Weir", "9780593135204")
	m4b := filepath.Join(dir, "Project Hail Mary.m4b")
	if err := os.WriteFile(m4b, buildID3v23("Project Hail Mary", "Andy Weir", ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got := bookFileFormats(t, books, ctx, book.ID)
	if got[models.MediaTypeEbook] != filepath.Clean(epub) {
		t.Errorf("ebook file = %q, want %q (all files: %v)", got[models.MediaTypeEbook], epub, got)
	}
	if got[models.MediaTypeAudiobook] != filepath.Clean(m4b) {
		t.Errorf("audiobook file = %q, want %q (all files: %v)", got[models.MediaTypeAudiobook], m4b, got)
	}
}

// TestScanLibrary_SameFormatFilesStillClaimOnce keeps the hazard the guard was
// written for covered: two files of the SAME format that both fuzzy-match one
// book must not both attach, because the second AddBookFile would overwrite the
// first (last write wins). Only the (book, format) pair is claimed — not the
// book — so this is the assertion that stops the #1957 fix from going too far.
func TestScanLibrary_SameFormatFilesStillClaimOnce(t *testing.T) {
	s, books, dir, book, ctx := dualFormatFixture(t)

	first := filepath.Join(dir, "Project Hail Mary.epub")
	writeEpubAt(t, first, "Project Hail Mary", "Andy Weir", "9780593135204")
	second := filepath.Join(dir, "Project Hail Mary (retail).epub")
	writeEpubAt(t, second, "Project Hail Mary", "Andy Weir", "9780593135204")

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ebooks int
	for _, f := range files {
		if f.Format == models.MediaTypeEbook {
			ebooks++
		}
	}
	if ebooks != 1 {
		t.Fatalf("expected exactly one ebook file to claim the book, got %d (%v)", ebooks, files)
	}
}

// TestScanLibrary_EbookNextToTrackedAudiobook covers the other half of #1957:
// the parent directory of a tracked audiobook is marked so its sibling TRACKS
// aren't reported unmatched, but that marking used to hide an epub dropped into
// the same folder — it was counted "already tracked" and never reconciled, on
// every scan, forever.
func TestScanLibrary_EbookNextToTrackedAudiobook(t *testing.T) {
	s, books, dir, book, ctx := dualFormatFixture(t)

	m4b := filepath.Join(dir, "Project Hail Mary.m4b")
	if err := os.WriteFile(m4b, buildID3v23("Project Hail Mary", "Andy Weir", ""), 0o644); err != nil {
		t.Fatal(err)
	}
	// The audiobook is already attached, as it would be after an earlier scan.
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, m4b); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(dir, "Project Hail Mary.epub")
	writeEpubAt(t, epub, "Project Hail Mary", "Andy Weir", "9780593135204")

	s.ScanLibrary(ctx)

	got := bookFileFormats(t, books, ctx, book.ID)
	if got[models.MediaTypeEbook] != filepath.Clean(epub) {
		t.Fatalf("epub beside a tracked audiobook was not reconciled: %v", got)
	}
}

// TestScanLibrary_AudiobookTracksStayTracked pins the behaviour the directory
// marking exists for: sibling MP3 tracks of an attached audiobook are counted
// as already tracked, not reported unmatched (#1436). Narrowing the marking to
// audio files must not weaken this.
func TestScanLibrary_AudiobookTracksStayTracked(t *testing.T) {
	s, books, dir, book, ctx := dualFormatFixture(t)

	first := filepath.Join(dir, "01.mp3")
	if err := os.WriteFile(first, buildID3v23("Project Hail Mary", "Andy Weir", ""), 0o644); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "02.mp3")
	if err := os.WriteFile(second, buildID3v23("Project Hail Mary", "Andy Weir", ""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, first); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got := bookFileFormats(t, books, ctx, book.ID)
	if got[models.MediaTypeAudiobook] != filepath.Clean(first) {
		t.Fatalf("tracked audiobook file changed: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("a sibling track attached as a second file: %v", got)
	}
}
