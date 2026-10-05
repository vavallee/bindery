package abs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestReconcileFormatPath_PathOwnedByOtherBook (#2937): an ABS item whose
// file another book already tracks used to come back ok and "changed" while
// the OR IGNORE insert recorded nothing. It now reports the owner and leaves
// both books as they were.
func TestReconcileFormatPath_PathOwnedByOtherBook(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	lib := t.TempDir()
	importer.WithStoragePaths(lib, "", nil)

	author := &models.Author{ForeignID: "abs-own-a", Name: "Own Author", SortName: "Author, Own",
		MetadataProvider: "abs", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mk := func(foreign, title string) *models.Book {
		b := &models.Book{ForeignID: foreign, AuthorID: author.ID, Title: title,
			SortTitle: strings.ToLower(title), Status: "wanted", Genres: []string{}, MetadataProvider: "abs", Monitored: true}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	owner := mk("abs-own-17", "Volume 17")
	book := mk("abs-own-3", "Volume 3")

	path := filepath.Join(lib, "Own Author", "Volume 3.epub")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, owner.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	ok, changed, msg, _ := importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, path)
	if ok || changed {
		t.Fatalf("ok=%v changed=%v for a path another book tracks, want both false", ok, changed)
	}
	if !strings.Contains(msg, owner.Title) {
		t.Errorf("message %q does not name the owning book", msg)
	}
	if files, _ := bookRepo.ListFiles(ctx, book.ID); len(files) != 0 {
		t.Errorf("book gained %+v", files)
	}
	if files, _ := bookRepo.ListFiles(ctx, owner.ID); len(files) != 1 {
		t.Errorf("owner's row changed: %+v", files)
	}
}
