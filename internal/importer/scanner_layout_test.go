package importer

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorTitleFromLayout unit-tests the folder-hierarchy resolver: author is
// the first directory under the root, title is the file's immediate parent
// directory with bracket/paren annotations stripped (#754). A disc subfolder is
// skipped, so the folder above it names the audiobook (#2723).
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
			// issue #2723: the disc subfolder of a split audiobook names a part,
			// not the book, so the title comes from the folder above it.
			name:  "disc subfolder uses the book folder above it",
			path:  filepath.Join(root, "Amy Tan", "The Kitchen God's Wife", "CD1", "The Kitchen God's Wife - 01.mp3"),
			wantA: "Amy Tan", wantT: "The Kitchen God's Wife", wantOK: true,
		},
		{
			name:  "disc subfolder worded as Disc uses the book folder above it",
			path:  filepath.Join(root, "Amy Tan", "The Kitchen God's Wife", "Disc 2", "track.mp3"),
			wantA: "Amy Tan", wantT: "The Kitchen God's Wife", wantOK: true,
		},
		{
			// issue #2672: "Book 1", "Vol 1" and bare numbers name separate
			// books of a series as often as they name discs, so the narrower
			// cd/disc rule leaves them as the book folder rather than folding
			// every volume into its series.
			name:  "numbered book folder keeps its own name",
			path:  filepath.Join(root, "Brandon Sanderson", "Mistborn", "Book 1", "x.epub"),
			wantA: "Brandon Sanderson", wantT: "Book 1", wantOK: true,
		},
		{
			name:  "volume book folder keeps its own name",
			path:  filepath.Join(root, "Brandon Sanderson", "Mistborn", "Vol 1", "x.epub"),
			wantA: "Brandon Sanderson", wantT: "Vol 1", wantOK: true,
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

// TestFlipByLayout pins when a filename may be read the other way round
// (#2331). ok only offers the flip: "It - Stephen King.epub" in It/ offers one
// too, and it is the catalogue check in every caller that refuses it. An
// ambiguous author match never flips, since the flip is automatic.
func TestFlipByLayout(t *testing.T) {
	cases := []struct {
		name                  string
		title, author, folder string
		wantOK                bool
		wantTitle, wantAuthor string
	}{
		{"flat layout", "Evil Thirst", "Christopher Pike", "", false, "", ""},
		{"backwards filename under its author folder", "Christopher Pike", "Evil Thirst", "Christopher Pike", true, "Evil Thirst", "Christopher Pike"},
		{"inverted backwards filename", "Pike, Christopher", "Evil Thirst", "Christopher Pike", true, "Evil Thirst", "Christopher Pike"},
		{"surname folder", "Pike", "Evil Thirst", "Pike", true, "Evil Thirst", "Pike"},
		{"filename already agrees", "Evil Thirst", "Christopher Pike", "Christopher Pike", false, "", ""},
		{"no filename author", "Evil Thirst", "", "Christopher Pike", false, "", ""},
		{"title is not the folder author", "Christine", "Stephen King", "Christopher Pike", false, "", ""},
		{"book folder offers a flip the catalogue must refuse", "It", "Stephen King", "It", true, "Stephen King", "It"},
		{"ambiguous name match never flips", "Stanley Paul", "Face the Music", "Paul Stanley", false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := flipByLayout(ParsedFile{Title: c.title, Author: c.author}, c.folder)
			if ok != c.wantOK {
				t.Fatalf("flipByLayout ok = %v, want %v", ok, c.wantOK)
			}
			if ok && (got.Title != c.wantTitle || got.Author != c.wantAuthor) {
				t.Errorf("flipByLayout = %q / %q, want %q / %q", got.Title, got.Author, c.wantTitle, c.wantAuthor)
			}
		})
	}
}

// TestScanLibrary_FilenameNamingItsBookFolderIsNotFlipped is the review
// finding on #2331: in a library of flat book folders, "It - Stephen King.epub"
// in It/ has a title side that names the first folder, exactly like a
// backwards "Author - Title" name in an author folder. The parse as it is
// reconciles, so the flip must never be taken.
func TestScanLibrary_FilenameNamingItsBookFolderIsNotFlipped(t *testing.T) {
	libDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libDir)
	book := seedLayoutBook(t, books, authors, ctx, "Stephen King", "It")

	p := filepath.Join(libDir, "It", "It - Stephen King.epub")
	writeFileAt(t, p)

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != p {
		t.Errorf("want FilePath=%q, got %q", p, got.FilePath)
	}
}

// fileOwners returns, sorted, the titles of the seeded books the scan
// attached p to, in any format. A book that tracks the folder holding p owns
// it too: an audiobook is recorded as its book folder (#2716).
func fileOwners(t *testing.T, books *db.BookRepo, ctx context.Context, seeded map[string]*models.Book, p string) []string {
	t.Helper()
	cleanP := filepath.Clean(p)
	parent := filepath.Clean(filepath.Dir(cleanP))
	audio := IsAudioFile(cleanP)
	var owners []string
	for title, b := range seeded {
		files, err := books.ListFiles(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			cleanF := filepath.Clean(f.Path)
			if cleanF == cleanP || (audio && cleanF == parent) {
				owners = append(owners, title)
				break
			}
		}
	}
	slices.Sort(owners)
	return owners
}

// writeFileWith writes body to p, creating its folders.
func writeFileWith(t *testing.T, p string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// layoutM4B is a minimal MP4 container whose ilst holds only an artist
// (©ART), which is all ReadAudioTags needs to report a tag author.
func layoutM4B(artist string) []byte {
	atom := func(name string, payload ...[]byte) []byte {
		var body []byte
		for _, p := range payload {
			body = append(body, p...)
		}
		out := make([]byte, 8, 8+len(body))
		binary.BigEndian.PutUint32(out, uint32(8+len(body)))
		copy(out[4:], name)
		return append(out, body...)
	}
	item := atom("\xa9ART", atom("data", []byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte(artist)))
	meta := atom("meta", []byte{0, 0, 0, 0}, atom("ilst", item))
	ftyp := atom("ftyp", []byte("M4B "), []byte{0, 0, 0, 0}, []byte("M4B isom"))
	return append(ftyp, atom("moov", atom("udta", meta))...)
}

// layoutID3 is an ID3v2.3 tag holding the given {frame ID, text} frames.
func layoutID3(frames ...[2]string) []byte {
	var body []byte
	for _, f := range frames {
		text := append([]byte{0x03}, f[1]...) // $03 = UTF-8
		body = append(body, frameHeaderV23(f[0], len(text))...)
		body = append(body, text...)
	}
	tag := append([]byte("ID3"), 0x03, 0x00, 0x00) // v2.3, flags 0
	tag = append(tag, synchsafe(uint32(len(body)))...)
	return append(tag, body...)
}

// TestScanLibrary_BackwardsFilenameBeatsTitleNamingItsAuthor is the scan half
// of the second review on #2331. Read as it is, "Tom Clancy - Red Storm
// Rising.epub" in Tom Clancy/ has the author's own name for a title, and the
// fuzzy title tier handed the file to a book named after him, on main too. The
// flip is now tried first. It is offered only in an author folder with no book
// folder below it, and taken only for a folder author the catalogue knows, so
// TestScanLibrary_FilenameNamingItsBookFolderIsNotFlipped still holds.
func TestScanLibrary_BackwardsFilenameBeatsTitleNamingItsAuthor(t *testing.T) {
	cases := []struct {
		name, author string
		titles       []string
		file, want   string
	}{
		{"one title carries his name", "Tom Clancy",
			[]string{"Tom Clancy Enemy Contact", "Red Storm Rising"},
			"Tom Clancy - Red Storm Rising.epub", "Red Storm Rising"},
		{"two titles carry his name", "Tom Clancy",
			[]string{"Tom Clancy Enemy Contact", "Tom Clancy Code of Honor", "Red Storm Rising"},
			"Tom Clancy - Red Storm Rising.epub", "Red Storm Rising"},
		{"autobiography", "Agatha Christie",
			[]string{"Agatha Christie: An Autobiography", "Murder on the Orient Express"},
			"Agatha Christie - Murder on the Orient Express.epub", "Murder on the Orient Express"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			s, books, authors, ctx := scannerFixture(t, libDir)
			seeded := seedAuthorBooks(t, books, authors, ctx, c.author, c.titles...)
			p := filepath.Join(libDir, c.author, c.file)
			writeFileAt(t, p)

			s.ScanLibrary(ctx)

			if got := fileOwners(t, books, ctx, seeded, p); !slices.Equal(got, []string{c.want}) {
				t.Errorf("file went to %q, want [%q]", got, c.want)
			}
		})
	}
}

// TestScanLibrary_TagAuthorOfTheFolderKeepsTheFlip is the second review's
// scan finding on #2331: any tag author cancelled the flip, so an m4b with
// only its artist set, or an mp3 with album and album artist, stayed
// unmatched in the scan while bulk and single file import matched it. A tag
// author that is the folder's says nothing about which side of the filename
// is the title.
func TestScanLibrary_TagAuthorOfTheFolderKeepsTheFlip(t *testing.T) {
	const pike, thirst = "Christopher Pike", "Evil Thirst"
	cases := []struct {
		name, file string
		body       []byte
	}{
		{"m4b with only the artist", pike + " - " + thirst + ".m4b", layoutM4B(pike)},
		{"mp3 with album and album artist", pike + " - " + thirst + ".mp3",
			layoutID3([2]string{"TALB", thirst}, [2]string{"TPE2", pike})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			s, books, authors, ctx := scannerFixture(t, libDir)
			seeded := seedAuthorBooks(t, books, authors, ctx, pike, thirst)
			p := filepath.Join(libDir, pike, c.file)
			writeFileWith(t, p, c.body)

			s.ScanLibrary(ctx)

			if got := fileOwners(t, books, ctx, seeded, p); !slices.Equal(got, []string{thirst}) {
				t.Errorf("file went to %q, want [%q]", got, thirst)
			}
		})
	}
}

// TestScanLibrary_TagAuthorNotTheFolderCancelsTheFlip keeps #303: a tag author
// that is not the folder's says the file is someone else's, so the filename is
// not read the other way round for the folder author.
func TestScanLibrary_TagAuthorNotTheFolderCancelsTheFlip(t *testing.T) {
	const pike, thirst, king = "Christopher Pike", "Evil Thirst", "Stephen King"
	cases := []struct {
		name, file string
		body       []byte
	}{
		{"m4b artist", pike + " - " + thirst + ".m4b", layoutM4B(king)},
		{"mp3 artist", pike + " - " + thirst + ".mp3", layoutID3([2]string{"TPE1", king})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			s, books, authors, ctx := scannerFixture(t, libDir)
			seeded := seedAuthorBooks(t, books, authors, ctx, pike, thirst)
			for title, b := range seedAuthorBooks(t, books, authors, ctx, king, "It") {
				seeded[title] = b
			}
			p := filepath.Join(libDir, pike, c.file)
			writeFileWith(t, p, c.body)

			s.ScanLibrary(ctx)

			if got := fileOwners(t, books, ctx, seeded, p); len(got) != 0 {
				t.Errorf("file went to %q, want no book", got)
			}
		})
	}
}
