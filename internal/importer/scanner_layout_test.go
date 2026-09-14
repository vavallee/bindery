package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorTitleFromLayout unit-tests the folder-hierarchy resolver: author is
// the first directory under the root, title is the file's immediate parent
// directory with bracket/paren annotations stripped (#754).
func TestAuthorTitleFromLayout(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name         string
		path         string
		wantA, wantT string
		wantOK       bool
	}{
		{
			name:  "author/book/file",
			path:  filepath.Join(root, "Cal Newport", "Deep Work", "Cal Newport - Deep Work.epub"),
			wantA: "Cal Newport", wantT: "Deep Work", wantOK: true,
		},
		{
			name:  "calibre id stripped from book folder",
			path:  filepath.Join(root, "Andy Weir", "Project Hail Mary (4242)", "x.epub"),
			wantA: "Andy Weir", wantT: "Project Hail Mary", wantOK: true,
		},
		{
			name:  "author/series/book/file uses first and immediate-parent dirs",
			path:  filepath.Join(root, "Brandon Sanderson", "Mistborn", "The Final Empire", "x.epub"),
			wantA: "Brandon Sanderson", wantT: "The Final Empire", wantOK: true,
		},
		{
			// issue #1234: Readarr "{Series} #{N} - {Title}" book folder. The
			// title must be the real book title, not the whole folder string with
			// the series tag glued on.
			name:  "readarr series-number book folder strips the series prefix",
			path:  filepath.Join(root, "Terry Pratchett", "Discworld", "Discworld #8 - Guards! Guards!", "Terry Pratchett - Guards! Guards!.epub"),
			wantA: "Terry Pratchett", wantT: "Guards! Guards!", wantOK: true,
		},
		{
			// issue #1234 regression guard: a flat non-series book folder that
			// merely contains a hyphen must NOT be mistaken for a series prefix.
			name:  "non-series book folder with hyphen is left intact",
			path:  filepath.Join(root, "Ursula K. Le Guin", "The Dispossessed - An Ambiguous Utopia", "x.epub"),
			wantA: "Ursula K. Le Guin", wantT: "The Dispossessed - An Ambiguous Utopia", wantOK: true,
		},
		{
			name:  "author folder only",
			path:  filepath.Join(root, "Isaac Asimov", "foundation.epub"),
			wantA: "Isaac Asimov", wantT: "", wantOK: true,
		},
		{
			name:  "file directly in root has no hierarchy",
			path:  filepath.Join(root, "loose.epub"),
			wantA: "", wantT: "", wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, tl, ok := authorTitleFromLayout(c.path, root)
			if a != c.wantA || tl != c.wantT || ok != c.wantOK {
				t.Errorf("authorTitleFromLayout(%q) = (%q, %q, %v); want (%q, %q, %v)",
					c.path, a, tl, ok, c.wantA, c.wantT, c.wantOK)
			}
		})
	}
}

// TestScanLibrary_ReadarrFolderLayoutFixesSwappedFilename is the #754
// regression test: a Readarr-style "{Author} - {Title}.epub" file in an
// <Author>/<Book>/ hierarchy. ParseFilename alone reads the filename as
// "Title - Author" and transposes the two; the scan must instead take author
// and title from the unambiguous folder names and reconcile correctly.
func TestScanLibrary_ReadarrFolderLayoutFixesSwappedFilename(t *testing.T) {
	libDir := t.TempDir()
	bookDir := filepath.Join(libDir, "Cal Newport", "Deep Work")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Readarr's default "{Author Name} - {Book Title}" filename.
	epub := filepath.Join(bookDir, "Cal Newport - Deep Work.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL-cn", Name: "Cal Newport", SortName: "Newport, Cal"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-dw", AuthorID: author.ID, Title: "Deep Work", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != epub {
		t.Errorf("Readarr-layout file must reconcile via the folder names, want FilePath=%q, got %q", epub, got.FilePath)
	}
}

// TestScanLibrary_ReadarrSeriesFolderReconcilesNonOpener is the #1234
// regression test: a Readarr library laid out as
// {Author}/{Series}/{Series} #{N} - {Title}/{Author} - {Title}.{ext}. Before
// the fix the book-folder name ("Discworld #8 - Guards! Guards!") leaked through
// as the parsed title, so only series openers (where book title == series
// title) reconciled and other books collided on the same title. The scan must
// strip the "{Series} #{N} - " prefix and reconcile each book to its real title.
func TestScanLibrary_ReadarrSeriesFolderReconcilesNonOpener(t *testing.T) {
	libDir := t.TempDir()

	type fixtureBook struct {
		seriesFolder string // e.g. "Discworld #8 - Guards! Guards!"
		title        string // stored book title, e.g. "Guards! Guards!"
	}
	books := []fixtureBook{
		{"Discworld #8 - Guards! Guards!", "Guards! Guards!"},
		{"Discworld #1 - The Colour of Magic", "The Colour of Magic"},
	}

	paths := make([]string, len(books))
	for i, b := range books {
		bookDir := filepath.Join(libDir, "Terry Pratchett", "Discworld", b.seriesFolder)
		if err := os.MkdirAll(bookDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Readarr's default "{Author} - {Title}.{ext}" filename.
		epub := filepath.Join(bookDir, "Terry Pratchett - "+b.title+".epub")
		if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[i] = epub
	}

	s, bookRepo, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL-tp", Name: "Terry Pratchett", SortName: "Pratchett, Terry"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(books))
	for i, b := range books {
		book := &models.Book{ForeignID: "OL-" + b.title, AuthorID: author.ID, Title: b.title, Status: models.BookStatusWanted}
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		ids[i] = book.ID
	}

	s.ScanLibrary(ctx)

	for i, b := range books {
		got, err := bookRepo.GetByID(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if got.FilePath != paths[i] {
			t.Errorf("book %q must reconcile to its own file via the stripped title, want FilePath=%q, got %q",
				b.title, paths[i], got.FilePath)
		}
	}
}

// TestScanLibrary_AuthorFolderFixesSwappedFilenameWithoutBookFolder extends
// #754 to a library with author folders but no book folders
// (<root>/<Author>/<Author> - <Title>.epub). The folder supplied the author,
// but with no book folder the title came from the filename's left side, which
// is the author again, so the file never reconciled (#2331).
func TestScanLibrary_AuthorFolderFixesSwappedFilenameWithoutBookFolder(t *testing.T) {
	libDir := t.TempDir()
	authorDir := filepath.Join(libDir, "Cal Newport")
	if err := os.MkdirAll(authorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(authorDir, "Cal Newport - Deep Work.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL-cn", Name: "Cal Newport", SortName: "Newport, Cal"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-dw", AuthorID: author.ID, Title: "Deep Work", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != epub {
		t.Errorf("want FilePath=%q, got %q", epub, got.FilePath)
	}
}

// TestApplyLayout pins the precedence the library scan and both Manual Import
// lookups share (#754, #2331). authoritative is the scan's library root, where
// the folders name author and title outright; the lookups pass false because
// their root can be any folder.
func TestApplyLayout(t *testing.T) {
	cases := []struct {
		name                     string
		title, author            string
		layoutAuthor, layoutBook string
		authoritative            bool
		wantTitle, wantAuthor    string
	}{
		{"flat layout keeps the filename", "Evil Thirst", "Christopher Pike", "", "", false, "Evil Thirst", "Christopher Pike"},
		{"backwards filename under its author folder", "Christopher Pike", "Evil Thirst", "Christopher Pike", "", false, "Evil Thirst", "Christopher Pike"},
		{"inverted backwards filename", "Pike, Christopher", "Evil Thirst", "Christopher Pike", "", false, "Evil Thirst", "Christopher Pike"},
		{"filename already agrees", "Evil Thirst", "Christopher Pike", "Christopher Pike", "", false, "Evil Thirst", "Christopher Pike"},
		{"unconfirmed folder does not beat the filename author", "Evil Thirst", "Christopher Pike", "Horror", "", false, "Evil Thirst", "Christopher Pike"},
		{"folder fills a missing author", "Evil Thirst", "", "Christopher Pike", "", false, "Evil Thirst", "Christopher Pike"},
		{"book folder fills a missing title", "", "", "Christopher Pike", "Evil Thirst", false, "Evil Thirst", "Christopher Pike"},
		{"lookup keeps the filename title over a book folder", "Evil Thirst", "", "Christopher Pike", "Books", false, "Evil Thirst", "Christopher Pike"},
		{"a title that is not the folder author is not swapped", "Christine", "Stephen King", "Christopher Pike", "", false, "Christine", "Stephen King"},
		{"scan folder beats a disagreeing filename author", "Evil Thirst", "Someone Else", "Christopher Pike", "", true, "Evil Thirst", "Christopher Pike"},
		{"scan book folder beats the filename title", "Wrong", "Whoever", "Christopher Pike", "Evil Thirst", true, "Evil Thirst", "Christopher Pike"},
		{"scan backwards filename with no book folder", "Cal Newport", "Deep Work", "Cal Newport", "", true, "Deep Work", "Cal Newport"},
		{"scan flat layout keeps the filename", "Deep Work", "Cal Newport", "", "", true, "Deep Work", "Cal Newport"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyLayout(ParsedFile{Title: c.title, Author: c.author}, c.layoutAuthor, c.layoutBook, c.authoritative)
			if got.Title != c.wantTitle || got.Author != c.wantAuthor {
				t.Errorf("applyLayout = %q / %q, want %q / %q", got.Title, got.Author, c.wantTitle, c.wantAuthor)
			}
		})
	}
}
