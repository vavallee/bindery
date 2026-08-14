package importer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// daizeArtistTag is the exact Artist value from the #1956 report: the author,
// the translator with a role suffix, then the narrator — the standard
// Audible-style contributor list. Every fixture below uses it verbatim.
const daizeArtistTag = "Álvaro Enrigue, Natasha Wimmer - translator, Gabriel Porras"

func TestContributorCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"daize contributor list", daizeArtistTag, []string{"Álvaro Enrigue", "Gabriel Porras"}},
		{"role suffix on the first segment", "Gabriel Porras - narrator, Álvaro Enrigue", []string{"Álvaro Enrigue"}},
		{"narrator credit segment", "Read by Nigel Planer, Terry Pratchett", []string{"Terry Pratchett"}},
		// Both co-authors are returned: the list says nothing about which of
		// them the book is catalogued under, so the caller unions them rather
		// than betting on the first.
		{"plain co-authors keep both", "Neil Gaiman, Terry Pratchett", []string{"Neil Gaiman", "Terry Pratchett"}},
		// Audible also emits an explicit " - author" credit. The suffix is
		// stripped and the name kept; returning the literal matched nobody.
		{"explicit author role", "Terry Pratchett - author, Nigel Planer - narrator", []string{"Terry Pratchett"}},
		// A librarian sort-form name also contains a comma. Returning "Enrigue"
		// here would let a bare surname match any author containing it, so the
		// single-token segment must abort the whole extraction.
		{"sort-form name is not a contributor list", "Enrigue, Álvaro", nil},
		{"sort-form with initial", "Martin, George R. R.", nil},
		// A multi-word surname clears a two-token test on the FIRST segment, so
		// the lone given name in the second segment is what has to abort it —
		// otherwise "García Márquez" is returned whole and authorMatch's subset
		// rule hands the file to Gabriel García Márquez.
		{"sort-form with a multi-word surname", "García Márquez, Rodrigo", nil},
		{"sort-form with a particle surname", "Le Guin, Theodore", nil},
		{"no comma at all", "Álvaro Enrigue", nil},
		{"empty", "", nil},
		// Every segment is a credit: nothing usable, so don't guess.
		{"only credits", "Read by A. B., Someone Else - translator", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contributorCandidates(c.in)
			if !slices.Equal(got, c.want) {
				t.Errorf("contributorCandidates(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestAuthorMatch_ContributorListNeverMatches pins the premise of the #1956
// fix: the contributor list itself cannot be made to match the catalogue author
// by authorMatch, so the fallback path is the only thing that can save the file.
// If someone ever loosens authorMatch to accept it, this test tells them the
// fallback's justification changed.
func TestAuthorMatch_ContributorListNeverMatches(t *testing.T) {
	if authorMatch("Álvaro Enrigue", daizeArtistTag) {
		t.Fatal("expected the contributor list NOT to match the catalogue author directly")
	}
	if !authorMatch("Álvaro Enrigue", "Álvaro Enrigue") {
		t.Fatal("expected the extracted author to match the catalogue author")
	}
}

// contributorFixture creates the "You Dreamed of Empires" author + wanted
// audiobook and returns the scanner, book repo, library dir and context.
func contributorFixture(t *testing.T) (*Scanner, *db.BookRepo, string, context.Context) {
	t.Helper()
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "ol:enrigue", Name: "Álvaro Enrigue", SortName: "Enrigue, Álvaro", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:empires", AuthorID: author.ID, Title: "You Dreamed of Empires", SortTitle: "you dreamed of empires",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return s, books, libraryDir, ctx
}

// assertReconciled fails unless want is among the book_file paths on record.
func assertReconciled(t *testing.T, books *db.BookRepo, ctx context.Context, want string) {
	t.Helper()
	paths, err := books.ListAllBookFilePaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if filepath.Clean(p) == filepath.Clean(want) {
			return
		}
	}
	t.Fatalf("%s was not reconciled; book_file paths = %v", want, paths)
}

// TestScanLibrary_ContributorListTagReconciles is the #1956 regression: an m4b
// laid out under {Author}/{Title}/ whose Artist tag is an author + translator +
// narrator contributor list must still reconcile via the folder-derived author.
// Before the fix the tag overwrote the (correct) folder author, authorMatch
// rejected every catalogue author because "wimmer"/"translator"/"porras" appear
// in no author's name, and the title tier was handed zero candidates — the file
// stayed unmatched on every scan, forever.
func TestScanLibrary_ContributorListTagReconciles(t *testing.T) {
	s, books, libraryDir, ctx := contributorFixture(t)

	dir := filepath.Join(libraryDir, "Álvaro Enrigue", "You Dreamed of Empires")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m4b := filepath.Join(dir, "You Dreamed of Empires.m4b")
	if err := os.WriteFile(m4b, buildID3v23("You Dreamed of Empires", daizeArtistTag, ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	assertReconciled(t, books, ctx, m4b)
}

// TestScanLibrary_ContributorListTagNoAuthorFolder covers the second fallback:
// with the file at the library root there is no folder author to restore, so
// the primary contributor of the list has to carry the match.
func TestScanLibrary_ContributorListTagNoAuthorFolder(t *testing.T) {
	s, books, libraryDir, ctx := contributorFixture(t)

	m4b := filepath.Join(libraryDir, "You Dreamed of Empires.m4b")
	if err := os.WriteFile(m4b, buildID3v23("You Dreamed of Empires", daizeArtistTag, ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	assertReconciled(t, books, ctx, m4b)
}

// TestScanLibrary_CoAuthorTagUsesEveryCreditedName covers the hole the
// first-contributor-wins fallback left: a plain co-author list carries no
// evidence about which name the book is catalogued under. "Bill Clinton, James
// Patterson" is catalogued under Patterson, but taking the first credit
// searched only Clinton's books — the right book was never a candidate, and any
// Clinton title inside the fuzzy gate could have taken the file instead. Every
// credited name that matches a catalogue author is unioned, so title similarity
// decides.
func TestScanLibrary_CoAuthorTagUsesEveryCreditedName(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)

	clinton := &models.Author{ForeignID: "ol:clinton", Name: "Bill Clinton", SortName: "Clinton, Bill", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, clinton); err != nil {
		t.Fatal(err)
	}
	// Clinton is in the library with a book of his own, so the old fallback
	// matched him and stopped there.
	if err := books.Create(ctx, &models.Book{
		ForeignID: "ol:mylife", AuthorID: clinton.ID, Title: "My Life", SortTitle: "my life",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}); err != nil {
		t.Fatal(err)
	}
	patterson := &models.Author{ForeignID: "ol:patterson", Name: "James Patterson", SortName: "Patterson, James", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, patterson); err != nil {
		t.Fatal(err)
	}
	if err := books.Create(ctx, &models.Book{
		ForeignID: "ol:tpim", AuthorID: patterson.ID, Title: "The President Is Missing", SortTitle: "the president is missing",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}); err != nil {
		t.Fatal(err)
	}

	// No author folder, so the folder fallback cannot rescue this one.
	m4b := filepath.Join(libraryDir, "The President Is Missing.m4b")
	if err := os.WriteFile(m4b, buildID3v23("The President Is Missing", "Bill Clinton, James Patterson", ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	assertReconciled(t, books, ctx, m4b)
}

// TestScanLibrary_NarratorOnlyArtistTagReconciles proves the folder-author
// fallback carries its own weight, independent of contributor-list parsing: a
// bare narrator name in Artist (no comma, no "Read by" prefix — nothing any
// string heuristic can recognise) matches no catalogue author, and only the
// folder author the tag overwrote can rescue the file.
func TestScanLibrary_NarratorOnlyArtistTagReconciles(t *testing.T) {
	s, books, libraryDir, ctx := contributorFixture(t)

	dir := filepath.Join(libraryDir, "Álvaro Enrigue", "You Dreamed of Empires")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m4b := filepath.Join(dir, "You Dreamed of Empires.m4b")
	if err := os.WriteFile(m4b, buildID3v23("You Dreamed of Empires", "Gabriel Porras", ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	assertReconciled(t, books, ctx, m4b)
}

// TestScanLibrary_WrongAuthorTagStillRejected proves the fallback did not turn
// the author tier into a rubber stamp: a file whose tag AND folder name both
// name a different author must stay unmatched, even though its title is an
// exact match for the catalogue book.
func TestScanLibrary_WrongAuthorTagStillRejected(t *testing.T) {
	s, books, libraryDir, ctx := contributorFixture(t)

	dir := filepath.Join(libraryDir, "Hernán Cortés", "You Dreamed of Empires")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m4b := filepath.Join(dir, "You Dreamed of Empires.m4b")
	if err := os.WriteFile(m4b, buildID3v23("You Dreamed of Empires", "Hernán Cortés, Someone Else - translator", ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	paths, err := books.ListAllBookFilePaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected no reconciliation for a foreign author, got %v", paths)
	}
}
