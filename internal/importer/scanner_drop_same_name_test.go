package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// sameNameBook adds a second "Title T" row under a second "Author A" row, so it
// renders to the same drop name as the fixture's book (#3562).
func sameNameBook(t *testing.T, s *Scanner, books *db.BookRepo, ctx context.Context) *models.Book {
	t.Helper()
	author := &models.Author{ForeignID: "OLA2", Name: "Author A", SortName: "A, Author", Monitored: true, MetadataProvider: "hardcover"}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB2", AuthorID: author.ID, Title: "Title T", SortTitle: "t, title",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return book
}

// placeDropEbook hands one ebook off for book and fails the test if it can't.
func placeDropEbook(t *testing.T, s *Scanner, book *models.Book, dropDir, layout, marker string) {
	t.Helper()
	ctx := t.Context()
	author, err := s.authors.GetByID(ctx, book.AuthorID)
	if err != nil {
		t.Fatal(err)
	}
	downloadDir := t.TempDir()
	writeEbook(t, filepath.Join(downloadDir, "release.epub"), marker)
	files := discoverBookFiles(ctx, downloadDir, nil)
	if _, _, err := s.placeDroppedFormat(ctx, book, author, downloadDir, files, nil, models.MediaTypeEbook, dropDir, layout, "copy"); err != nil {
		t.Fatalf("place %s: %v", marker, err)
	}
}

func readDropFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected a file at %s: %v", path, err)
	}
	return string(data)
}

func writtenEbook(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ebook.epub")
	writeEbook(t, path, marker)
	return readDropFile(t, path)
}

func countDropFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// TestDrop_SameNameFromAnotherBookKeepsBoth is the #3562 regression, end to end
// through the import: the second book's handoff used to delete the first book's
// file while it was still waiting for the external tool.
func TestDrop_SameNameFromAnotherBookKeepsBoth(t *testing.T) {
	s, settings, downloads, books, _, dropDir, bookA, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})
	bookB := sameNameBook(t, s, books, ctx)

	for _, tc := range []struct {
		book   *models.Book
		marker string
	}{{bookA, "book A"}, {bookB, "book B"}} {
		downloadDir := t.TempDir()
		writeEbook(t, filepath.Join(downloadDir, "release.epub"), tc.marker)
		dl := newDropDownload(t, downloads, tc.book, ctx)
		s.ImportFromPath(ctx, dl, downloadDir, "")
		assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
	}

	if got := readDropFile(t, filepath.Join(dropDir, "Title T - Author A.epub")); got != writtenEbook(t, "book A") {
		t.Error("the first book's file was replaced by the second book's handoff")
	}
	if got := readDropFile(t, filepath.Join(dropDir, "Title T - Author A (2).epub")); got != writtenEbook(t, "book B") {
		t.Error("the second book's file should take a name of its own")
	}
}

// TestDrop_SameNameTemplatedLayoutKeepsBoth: the templated layout places files
// the same way, so it has the same collision.
func TestDrop_SameNameTemplatedLayoutKeepsBoth(t *testing.T) {
	s, _, _, books, _, dropDir, bookA, ctx := dropFixture(t, models.MediaTypeEbook)
	bookB := sameNameBook(t, s, books, ctx)

	placeDropEbook(t, s, bookA, dropDir, "templated", "book A")
	placeDropEbook(t, s, bookB, dropDir, "templated", "book B")

	var found []string
	if err := filepath.WalkDir(dropDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("expected a file for each book, found %v", found)
	}
}

// TestDrop_EarlierDropOfTheSameBookIsReplaced: a re-drop must not pile up copies
// of one book for the external tool, including under a suffixed name, and must
// not touch the other book's file while doing it.
func TestDrop_EarlierDropOfTheSameBookIsReplaced(t *testing.T) {
	s, _, _, books, _, dropDir, bookA, ctx := dropFixture(t, models.MediaTypeEbook)
	bookB := sameNameBook(t, s, books, ctx)
	plain := filepath.Join(dropDir, "Title T - Author A.epub")
	suffixed := filepath.Join(dropDir, "Title T - Author A (2).epub")

	placeDropEbook(t, s, bookA, dropDir, "flat", "A first")
	placeDropEbook(t, s, bookB, dropDir, "flat", "B first")
	placeDropEbook(t, s, bookA, dropDir, "flat", "A second")
	placeDropEbook(t, s, bookB, dropDir, "flat", "B second")

	if n := countDropFiles(t, dropDir); n != 2 {
		t.Errorf("drop folder holds %d files, want one per book", n)
	}
	if got := readDropFile(t, plain); got != writtenEbook(t, "A second") {
		t.Error("book A's earlier drop should have been replaced by its newer one")
	}
	if got := readDropFile(t, suffixed); got != writtenEbook(t, "B second") {
		t.Error("book B's earlier drop should have been replaced by its newer one")
	}
}

// TestDrop_NameLastPlacedByAnotherBookIsNotTakenBack: once the external tool has
// consumed a book's file, another book can legitimately take that name. The first
// book's history at that path must not let it replace what is there now.
func TestDrop_NameLastPlacedByAnotherBookIsNotTakenBack(t *testing.T) {
	s, _, _, books, _, dropDir, bookA, ctx := dropFixture(t, models.MediaTypeEbook)
	bookB := sameNameBook(t, s, books, ctx)
	plain := filepath.Join(dropDir, "Title T - Author A.epub")

	placeDropEbook(t, s, bookA, dropDir, "flat", "A first")
	if err := os.Remove(plain); err != nil { // the external tool ingests it
		t.Fatal(err)
	}
	placeDropEbook(t, s, bookB, dropDir, "flat", "B first")
	placeDropEbook(t, s, bookA, dropDir, "flat", "A second")

	if got := readDropFile(t, plain); got != writtenEbook(t, "B first") {
		t.Error("book B's waiting file was replaced")
	}
	if got := readDropFile(t, filepath.Join(dropDir, "Title T - Author A (2).epub")); got != writtenEbook(t, "A second") {
		t.Error("book A's new file should take a name of its own")
	}
}

// TestDrop_FileBinderyDidNotPlaceIsKept: a file with no placement on record could
// be anything, so it is never replaced.
func TestDrop_FileBinderyDidNotPlaceIsKept(t *testing.T) {
	s, _, _, _, _, dropDir, book, _ := dropFixture(t, models.MediaTypeEbook)
	plain := filepath.Join(dropDir, "Title T - Author A.epub")
	if err := os.WriteFile(plain, []byte("somebody else's"), 0o644); err != nil {
		t.Fatal(err)
	}

	placeDropEbook(t, s, book, dropDir, "flat", "book")

	if got := readDropFile(t, plain); got != "somebody else's" {
		t.Errorf("a file Bindery did not place was replaced with %d bytes", len(got))
	}
	if got := readDropFile(t, filepath.Join(dropDir, "Title T - Author A (2).epub")); got != writtenEbook(t, "book") {
		t.Error("the book should take a name of its own")
	}
}

// TestDrop_RacingHandoffsOfOneNameKeepEveryFile: handoffs of same-named books
// running at once must each end up with a file of their own. Checking the name
// first and writing after let two of them see it free, and the later write
// removed the earlier book's file.
func TestDrop_RacingHandoffsOfOneNameKeepEveryFile(t *testing.T) {
	for _, mode := range []string{"copy", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			s := &Scanner{books: db.NewBookRepo(database)}
			folder := t.TempDir()
			dest := filepath.Join(folder, "Title T - Author A.epub")

			const books = 8
			srcs := make([]string, books)
			for i := range srcs {
				srcs[i] = filepath.Join(t.TempDir(), "release.epub")
				if err := os.WriteFile(srcs[i], []byte(fmt.Sprintf("book %d", i+1)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			errs := make([]error, books)
			for i := range srcs {
				wg.Go(func() {
					_, errs[i] = s.dropPlaceEbook(t.Context(), int64(i+1), folder, dest, srcs[i], mode)
				})
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("book %d: %v", i+1, err)
				}
			}

			entries, err := os.ReadDir(folder)
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string]bool, len(entries))
			for _, e := range entries {
				got[readDropFile(t, filepath.Join(folder, e.Name()))] = true
			}
			for i := range books {
				if want := fmt.Sprintf("book %d", i+1); !got[want] {
					t.Errorf("%s's file is missing from the drop folder (%d files)", want, len(entries))
				}
			}
		})
	}
}

// TestDropCreateFile_NeverReplaces: claiming a name must fail on anything
// already there, in both modes, and leave it as it was.
func TestDropCreateFile_NeverReplaces(t *testing.T) {
	for _, mode := range []string{"copy", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "release.epub")
			if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(t.TempDir(), "Title.epub")
			if err := os.WriteFile(dst, []byte("waiting"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := dropCreateFile(t.Context(), src, dst, mode); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("err = %v, want one matching fs.ErrExist", err)
			}
			if got := readDropFile(t, dst); got != "waiting" {
				t.Errorf("the file at the name was changed to %q", got)
			}
		})
	}
}

// TestDropPlaceEbook_LookupFailureFailsThePlacement: when the book that placed
// a file can't be read, nothing is overwritten on a guess.
func TestDropPlaceEbook_LookupFailureFailsThePlacement(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	s := &Scanner{books: db.NewBookRepo(database)}
	folder := t.TempDir()
	dest := filepath.Join(folder, "Title.epub")
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "release.epub")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	database.Close()

	if _, err := s.dropPlaceEbook(context.Background(), 1, folder, dest, src, "copy"); err == nil {
		t.Fatal("want an error when the placement lookup fails")
	}
	if got := readDropFile(t, dest); got != "x" {
		t.Errorf("the file at the name was changed to %q", got)
	}
	if n := countDropFiles(t, folder); n != 1 {
		t.Errorf("drop folder holds %d files, want only the one that was there", n)
	}
}

// TestDropPlaceEbook_RefusesANameOutsideTheFolder: a rendered name that leaves
// the drop folder is an error, not a place to write.
func TestDropPlaceEbook_RefusesANameOutsideTheFolder(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "drop")
	src := filepath.Join(t.TempDir(), "release.epub")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{}
	for _, dest := range []string{filepath.Join(folder, "..", "Title.epub"), filepath.Join(filepath.Dir(folder), "drop2", "Title.epub")} {
		if got, err := s.dropPlaceEbook(context.Background(), 1, folder, dest, src, "copy"); err == nil {
			t.Errorf("dest %q: got %q, want an error", dest, got)
		}
		if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("dest %q: something was written there (%v)", dest, err)
		}
	}
}
