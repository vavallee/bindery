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
// tracks Title.epub gains Title.azw3 and Title.mobi from the same folder. A
// second epub with a different name ("Title (2).epub") is a same-format copy,
// not another format, and stays out of the book. A same name PDF is a
// supplement-class file (#2188) and is left to the ordinary rules, which send
// it to Unmatched rather than attach it.
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
	mobi := write("Alpha Adventure.mobi")
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
	want := []string{filepath.Clean(azw3), filepath.Clean(epub), filepath.Clean(mobi)}
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
		if p == filepath.Clean(copyEpub) || p == filepath.Clean(pdf) {
			t.Fatalf("a same-format copy or a supplement-class file must not be attached: %v", got)
		}
	}

	p := readScanResult(t, ctx, settings)
	if p.Reconciled != 2 {
		t.Errorf("Reconciled = %d, want 2 (the azw3 and the mobi)", p.Reconciled)
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

// hasEbookFile reports whether the book tracks any ebook file.
func hasEbookFile(t *testing.T, files []models.BookFile) bool {
	t.Helper()
	return len(ebookPathsOf(t, files)) > 0
}

// writeBookSized writes a book-sized file at path, creating its folder.
func writeBookSized(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bookSized("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScanLibrary_AudiobookFolderIsNotAnEbookSibling: an audiobook is tracked
// as its folder, and a folder has no ebook extension. A loose epub named like
// the folder ("Alpha Adventure/" and "Alpha Adventure.epub") is not another
// format of that audiobook, and neither is "Mr.epub" beside the folder
// "Mr. Mercedes/", whose name filepath.Ext would cut at the dot.
func TestScanLibrary_AudiobookFolderIsNotAnEbookSibling(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	authorDir := filepath.Join(libDir, "Jane Doe")
	alphaDir := filepath.Join(authorDir, "Alpha Adventure")
	mercedesDir := filepath.Join(authorDir, "Mr. Mercedes")
	writeBookSized(t, filepath.Join(alphaDir, "01 - track.mp3"))
	writeBookSized(t, filepath.Join(mercedesDir, "01 - track.mp3"))
	writeBookSized(t, filepath.Join(authorDir, "Alpha Adventure.epub"))
	writeBookSized(t, filepath.Join(authorDir, "Mr.epub"))

	alpha := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeAudiobook)
	if err := books.AddBookFile(ctx, alpha.ID, models.MediaTypeAudiobook, alphaDir); err != nil {
		t.Fatal(err)
	}
	mercedes := createScanBook(t, ctx, books, author.ID, "ol:mercedes", "Mr. Mercedes", models.BookStatusImported, models.MediaTypeAudiobook)
	if err := books.AddBookFile(ctx, mercedes.ID, models.MediaTypeAudiobook, mercedesDir); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	for _, b := range []*models.Book{alpha, mercedes} {
		files, err := books.ListFiles(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if hasEbookFile(t, files) {
			t.Errorf("audiobook %q gained an ebook from a loose file named like its folder: %v", b.Title, ebookPathsOf(t, files))
		}
		got, err := books.GetByID(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.MediaType != models.MediaTypeAudiobook {
			t.Errorf("audiobook %q media type = %q, want %q", b.Title, got.MediaType, models.MediaTypeAudiobook)
		}
	}
}

// TestScanLibrary_CaseVariantOfAKnownFormatIsNotAnotherFormat: with
// Title.epub tracked, Title.azw3 is another format but Title.ePub is the same
// format in other letter case. Attaching the azw3 must not make the ePub look
// new.
func TestScanLibrary_CaseVariantOfAKnownFormatIsNotAnotherFormat(t *testing.T) {
	s, books, authors, _, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Alpha Adventure")
	epub := writeBookSized(t, filepath.Join(dir, "Alpha Adventure.epub"))
	azw3 := writeBookSized(t, filepath.Join(dir, "Alpha Adventure.azw3"))
	writeBookSized(t, filepath.Join(dir, "Alpha Adventure.ePub"))

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
	want := []string{filepath.Clean(azw3), filepath.Clean(epub)}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("book ebook files = %v, want %v (the ePub variant is not another format)", got, want)
	}
}

// TestScanLibrary_SupplementBesideNewEbookStaysASidecar keeps #2188: a same
// name .pdf, .txt or .rtf beside an epub the same pass attaches is that
// epub's sidecar, counted as tracked, not a second format of the book.
func TestScanLibrary_SupplementBesideNewEbookStaysASidecar(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Beta Business")
	epub := writeBookSized(t, filepath.Join(dir, "Beta Business.epub"))
	for _, ext := range []string{".pdf", ".txt", ".rtf"} {
		writeBookSized(t, filepath.Join(dir, "Beta Business"+ext))
	}
	book := createScanBook(t, ctx, books, author.ID, "ol:beta", "Beta Business", models.BookStatusWanted, models.MediaTypeEbook)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := ebookPathsOf(t, files)
	if len(got) != 1 || got[0] != filepath.Clean(epub) {
		t.Fatalf("book ebook files = %v, want only the epub (supplements stay sidecars, #2188)", got)
	}
	p := readScanResult(t, ctx, settings)
	if p.Reconciled != 1 || p.Unmatched != 0 || p.AlreadyTrack != 3 {
		t.Errorf("reconciled/unmatched/already_tracked = %d/%d/%d, want 1/0/3", p.Reconciled, p.Unmatched, p.AlreadyTrack)
	}
}
