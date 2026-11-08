package importer

// Regression tests for #1357: the library scan ignored a second ebook format
// sitting next to the file a book already tracks. "Dune.azw3" beside a tracked
// "Dune.epub" belongs to the same book, but the book was no longer a scan
// candidate (its ebook resolved), so the azw3 went to Unmatched and had to be
// adopted by hand into the book it obviously belongs to.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func ebookPathsOf(t *testing.T, files []models.BookFile) []string {
	t.Helper()
	var out []string
	for _, f := range files {
		if f.Format == models.MediaTypeEbook {
			out = append(out, filepath.Clean(f.Path))
		}
	}
	sort.Strings(out)
	return out
}

// TestScanLibrary_RegistersOtherFormatBesideTrackedEbook: a book that already
// tracks Title.epub gains Title.azw3 and Title.pdf from the same folder. A
// second epub with a different name ("Title (2).epub") is a same-format copy,
// not another format, and stays out of the book.
func TestScanLibrary_RegistersOtherFormatBesideTrackedEbook(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Alpha Adventure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	epub := write("Alpha Adventure.epub")
	azw3 := write("Alpha Adventure.azw3")
	pdf := write("Alpha Adventure.pdf")
	copyEpub := write("Alpha Adventure (2).epub")

	book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeEbook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := ebookPathsOf(t, files)
	want := []string{filepath.Clean(azw3), filepath.Clean(epub), filepath.Clean(pdf)}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("book ebook files = %v, want %v (other formats beside the tracked file must be registered)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("book ebook files = %v, want %v", got, want)
		}
	}
	for _, p := range got {
		if p == filepath.Clean(copyEpub) {
			t.Fatalf("a same-format copy with another name must not be attached: %v", got)
		}
	}

	p := readScanResult(t, ctx, settings)
	if p.Reconciled != 2 {
		t.Errorf("Reconciled = %d, want 2 (the azw3 and the pdf)", p.Reconciled)
	}
	if got := p.Reconciled + p.Unmatched + p.AlreadyTrack; got != p.FilesFound {
		t.Errorf("counters don't add up: reconciled+unmatched+already_tracked = %d, files_found = %d", got, p.FilesFound)
	}
}

// TestScanLibrary_RegistersBothFormatsOfNewBookInOnePass: a wanted book whose
// folder holds Title.epub and Title.azw3, neither tracked yet. The first file
// the scan matches claims the book; the other format must follow it in the
// same pass rather than be sent to Unmatched until a second scan.
func TestScanLibrary_RegistersBothFormatsOfNewBookInOnePass(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Beta Business")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, name := range []string{"Beta Business.epub", "Beta Business.azw3"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Clean(p))
	}
	book := createScanBook(t, ctx, books, author.ID, "ol:beta", "Beta Business", models.BookStatusWanted, models.MediaTypeEbook)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := ebookPathsOf(t, files)
	sort.Strings(paths)
	if len(got) != 2 || got[0] != paths[0] || got[1] != paths[1] {
		t.Fatalf("book ebook files = %v, want both %v in one pass", got, paths)
	}
	p := readScanResult(t, ctx, settings)
	if p.Unmatched != 0 {
		t.Errorf("Unmatched = %d, want 0", p.Unmatched)
	}
}

// TestScanLibrary_OtherFormatOfAnotherBookIsNotTakenOver: a sibling that
// another book already tracks is counted as tracked and stays with its owner.
func TestScanLibrary_OtherFormatOfAnotherBookIsNotTakenOver(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Gamma Games")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(dir, "Gamma Games.epub")
	mobi := filepath.Join(dir, "Gamma Games.mobi")
	for _, p := range []string{epub, mobi} {
		if err := os.WriteFile(p, bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := createScanBook(t, ctx, books, author.ID, "ol:gamma", "Gamma Games", models.BookStatusImported, models.MediaTypeEbook)
	other := createScanBook(t, ctx, books, author.ID, "ol:gamma2", "Gamma Games Again", models.BookStatusImported, models.MediaTypeEbook)
	if err := books.AddBookFile(ctx, first.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, other.ID, models.MediaTypeEbook, mobi); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	owner, err := books.GetByTrackedPath(ctx, filepath.Clean(mobi))
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.ID != other.ID {
		t.Fatalf("mobi owner = %+v, want book %d", owner, other.ID)
	}
}
