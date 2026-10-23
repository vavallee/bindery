package importer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// CD rips often name each disc folder after its album tag, "<title> Disc 1",
// so a disc folder may carry the book's own title in front of the label
// (#3327). Only the parent's title counts, compared the way titles are.
func TestIsTitledDisc(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		want         bool
	}{
		{"Fjellvinden Disc 1", "Fjellvinden (2002)", true},
		{"Fjellvinden CD2", "Fjellvinden", true},
		{"Fjellvinden (Disc 03)", "Fjellvinden (2002)", true},
		{"Fjellvinden [CD 4]", "Fjellvinden", true},
		{"Fjellvinden - Disk 5", "Fjellvinden", true},
		{"fjellvinden disc 1", "Fjellvinden", true},
		{"Fje\u0301llvinden Disc 1", "Fj\u00e9llvinden", true}, // macOS hands names back decomposed
		{"Fjellvinden (2002) Disc 1", "Fjellvinden (2002)", true},
		{"Havgapet Disc 1", "Fjellvinden (2002)", false}, // another book's title
		{"Fjellvinden Part 2", "Fjellvinden", false},     // only cd/disc after a title
		{"Fjellvinden Disc", "Fjellvinden", false},       // no number
		{"Fjellvinden Disc 1 Extra", "Fjellvinden", false},
		{"Disc 1", "Fjellvinden", false}, // a bare label is discSetNameRe's
		{"Fjellvinden Disc 1", "", false},
		{"(2002) Disc 1", "(2002)", false}, // both titles clean to nothing: no match
	} {
		if got := isTitledDisc(tc.name, tc.parent); got != tc.want {
			t.Errorf("isTitledDisc(%q, %q) = %v, want %v", tc.name, tc.parent, got, tc.want)
		}
	}
	if !isDiscSetName("Disc 2", "Fjellvinden") || !isDiscSetName("Fjellvinden Disc 2", "Fjellvinden") || isDiscSetName("Book 2", "Fjellvinden") {
		t.Error("isDiscSetName must take a bare label or the book's own titled label, never Book N")
	}
}

// seedTitledDiscs writes one track into each "<title> Disc N" folder under
// <root>/<author>/<bookFolder>/ and returns the book folder.
func seedTitledDiscs(t *testing.T, root, author, bookFolder, title string, discs int) string {
	t.Helper()
	bookDir := filepath.Join(root, author, bookFolder)
	for d := 1; d <= discs; d++ {
		writeFileAt(t, filepath.Join(bookDir, fmt.Sprintf("%s Disc %d", title, d), "01.mp3"))
	}
	return bookDir
}

// The Import page groups a titled disc set as one book, not one per disc.
func TestGroupUnmatched_TitledDiscSet(t *testing.T) {
	root := t.TempDir()
	bookDir := seedTitledDiscs(t, root, "Kari Nordmann", "Fjellvinden (2002)", "Fjellvinden", 3)
	var files []unmatchedScanFile
	for d := 1; d <= 3; d++ {
		files = append(files, audioFile(filepath.Join(bookDir, fmt.Sprintf("Fjellvinden Disc %d", d), "01.mp3"), "Spor 1"))
	}
	groups, _ := groupUnmatched(files, []string{root})
	if len(groups) != 1 || groups[0].unit.UnitPath != bookDir || groups[0].unit.FileCount != 3 {
		t.Fatalf("got %d units %+v, want one for %s with 3 files", len(groups), groups, bookDir)
	}
}

// Discs whose title is not the book folder's stay separate units, and a disc
// folder straight under the author never makes the author folder a book.
func TestGroupUnmatched_TitledDiscsKeptApart(t *testing.T) {
	t.Run("another prefix", func(t *testing.T) {
		root := t.TempDir()
		bookDir := filepath.Join(root, "Kari Nordmann", "Fjellvinden (2002)")
		a := filepath.Join(bookDir, "Havgapet Disc 1", "01.mp3")
		b := filepath.Join(bookDir, "Fjellvinden Disc 2", "01.mp3")
		writeFileAt(t, a)
		writeFileAt(t, b)
		groups, _ := groupUnmatched([]unmatchedScanFile{audioFile(a, "Spor 1"), audioFile(b, "Spor 1")}, []string{root})
		if len(groups) != 2 {
			t.Fatalf("got %d units, want 2 kept apart: %+v", len(groups), groups)
		}
	})
	t.Run("disc folder under the author", func(t *testing.T) {
		root := t.TempDir()
		authorDir := filepath.Join(root, "Kari Nordmann")
		a := filepath.Join(authorDir, "Kari Nordmann Disc 1", "01.mp3")
		b := filepath.Join(authorDir, "Kari Nordmann Disc 2", "01.mp3")
		writeFileAt(t, a)
		writeFileAt(t, b)
		groups, _ := groupUnmatched([]unmatchedScanFile{audioFile(a, "Spor 1"), audioFile(b, "Spor 1")}, []string{root})
		for _, g := range groups {
			if g.unit.UnitPath == authorDir {
				t.Fatalf("the author folder became a book unit: %+v", groups)
			}
		}
	})
}

// The library scan's layout reading names the book folder above a titled disc
// folder for a file two levels down, but never the author folder.
func TestBookFolderFromLayout_TitledDisc(t *testing.T) {
	root := "/lib"
	for _, tc := range []struct {
		path, want string
	}{
		{"/lib/Kari Nordmann/Fjellvinden (2002)/Fjellvinden Disc 1/01.mp3", "/lib/Kari Nordmann/Fjellvinden (2002)"},
		{"/lib/Kari Nordmann/Fjellvinden (2002)/Fjellvinden (Disc 03)/01.mp3", "/lib/Kari Nordmann/Fjellvinden (2002)"},
		{"/lib/Kari Nordmann/Fjellvinden (2002)/Havgapet Disc 1/01.mp3", "/lib/Kari Nordmann/Fjellvinden (2002)/Havgapet Disc 1"},
		{"/lib/Kari Nordmann/Kari Nordmann Disc 1/01.mp3", "/lib/Kari Nordmann/Kari Nordmann Disc 1"},
	} {
		_, got, ok := bookFolderFromLayout(tc.path, root)
		if !ok || got != tc.want {
			t.Errorf("bookFolderFromLayout(%q) = %q, %v; want %q", tc.path, got, ok, tc.want)
		}
	}
}

// AllDiscFolders, which the folder import walk uses, accepts a titled set too.
func TestAllDiscFolders_Titled(t *testing.T) {
	root := t.TempDir()
	bookDir := seedTitledDiscs(t, root, "Kari Nordmann", "Fjellvinden (2002)", "Fjellvinden", 2)
	discs := []string{filepath.Join(bookDir, "Fjellvinden Disc 1"), filepath.Join(bookDir, "Fjellvinden Disc 2")}
	if !AllDiscFolders(discs, true) {
		t.Error("a titled disc set was not taken as one audiobook")
	}
	if AllDiscFolders(discs, false) {
		t.Error("a titled disc set was taken without the caller allowing it")
	}
	writeFileAt(t, filepath.Join(bookDir, "Havgapet Disc 3", "01.mp3"))
	if AllDiscFolders(append(discs, filepath.Join(bookDir, "Havgapet Disc 3")), true) {
		t.Error("a disc named after another book was taken into the set")
	}
}

// End to end through the library scan: a titled disc split audiobook
// reconciles as the one book its folder names, as #2723 does for "CD1".
func TestScanLibrary_TitledDiscSplitReconcilesOneBook(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "OL-kn-disc", Name: "Kari Nordmann", SortName: "Nordmann, Kari"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-fj-disc", AuthorID: author.ID, Title: "Fjellvinden", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	bookDir := seedTitledDiscs(t, libraryDir, "Kari Nordmann", "Fjellvinden (2002)", "Fjellvinden", 3)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Clean(files[0].Path) != filepath.Clean(bookDir) {
		t.Fatalf("want one row for %s, got %+v", bookDir, files)
	}
}
