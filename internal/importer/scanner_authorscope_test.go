package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_HyphenatedAuthorReconciles guards the author-scoping word-run
// index: a hyphenated author name must still resolve. "Mary-Kate" splits into
// the runs "mary" and "kate", and the parsed token "mary-kate" indexes on its
// first run "mary" — the index must still be a super-set of authorMatch.
func TestScanLibrary_HyphenatedAuthorReconciles(t *testing.T) {
	libDir := t.TempDir()
	bookDir := filepath.Join(libDir, "Mary-Kate Olsen", "Influence")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(bookDir, "Influence.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL-hy", Name: "Mary-Kate Olsen", SortName: "Olsen, Mary-Kate"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-hy-b", AuthorID: author.ID, Title: "Influence", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != epub {
		t.Errorf("hyphenated author: want FilePath=%q, got %q", epub, got.FilePath)
	}
}

// TestScanLibrary_SharedFirstNameScopedToRightAuthor — two authors share the
// first name "John" and own an identically titled book. The file under
// "John Smith/" must reconcile Smith's book and leave Carter's untouched: the
// word-run index may bucket both authors under "john", but the exact
// authorMatch verification must reject the cross-author candidate.
func TestScanLibrary_SharedFirstNameScopedToRightAuthor(t *testing.T) {
	libDir := t.TempDir()
	bookDir := filepath.Join(libDir, "John Smith", "Quantum Gardens")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(bookDir, "Quantum Gardens.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	smith := &models.Author{ForeignID: "OL-js", Name: "John Smith", SortName: "Smith, John"}
	carter := &models.Author{ForeignID: "OL-jc", Name: "John Carter", SortName: "Carter, John"}
	for _, a := range []*models.Author{smith, carter} {
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	smithBook := &models.Book{ForeignID: "OL-js-b", AuthorID: smith.ID, Title: "Quantum Gardens", Status: models.BookStatusWanted}
	carterBook := &models.Book{ForeignID: "OL-jc-b", AuthorID: carter.ID, Title: "Quantum Gardens", Status: models.BookStatusWanted}
	for _, bk := range []*models.Book{smithBook, carterBook} {
		if err := books.Create(ctx, bk); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	gotSmith, _ := books.GetByID(ctx, smithBook.ID)
	if gotSmith.FilePath != epub {
		t.Errorf("Smith book: want FilePath=%q, got %q", epub, gotSmith.FilePath)
	}
	gotCarter, _ := books.GetByID(ctx, carterBook.ID)
	if gotCarter.FilePath != "" {
		t.Errorf("Carter book must stay unreconciled, got FilePath=%q", gotCarter.FilePath)
	}
}
