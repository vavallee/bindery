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
