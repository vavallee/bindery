package abs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestReconcileFormatPath_RefusesSymlink: an ABS item path that is a symlink
// inside the library is not registered as the book's file, because a
// registered path is what the download routes serve. A regular file next to
// it still is.
func TestReconcileFormatPath_RefusesSymlink(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	lib := t.TempDir()
	importer.WithStoragePaths(lib, "", nil)

	author := &models.Author{ForeignID: "abs-link-a", Name: "Link Author", SortName: "Author, Link",
		MetadataProvider: "abs", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "abs-link-b", AuthorID: author.ID, Title: "Link Book",
		SortTitle: "link book", Status: "imported", Genres: []string{}, MetadataProvider: "abs", Monitored: true}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	secret := filepath.Join(t.TempDir(), "bindery.db")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(lib, "Link Author", "Link Book.epub")
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ok, _, msg, _ := importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, link)
	if ok || !strings.Contains(msg, "symlink") {
		t.Fatalf("symlinked ebook: ok=%v msg=%q, want refused as a symlink", ok, msg)
	}
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("symlink registered as a book file: %+v", files)
	}

	real := filepath.Join(lib, "Link Author", "Real Book.epub")
	if err := os.WriteFile(real, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _, msg, _ := importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, real); !ok {
		t.Fatalf("regular ebook refused: %q", msg)
	}
}
