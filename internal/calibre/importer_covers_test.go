package calibre

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// jpegBytes is enough of a JPEG for http.DetectContentType to call it one.
var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}

// writeLibraryCover lays out <lib>/<Author>/<Title (id)>/cover.jpg the way
// Calibre does and returns the cover path the reader would record.
func writeLibraryCover(t *testing.T, lib string, cb CalibreBook, body []byte) string {
	t.Helper()
	dir := filepath.Join(lib, cb.Authors[0].Name, cb.Title)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertNoHostPaths(t *testing.T, imp *Importer) {
	t.Helper()
	ctx := context.Background()
	if eds, err := imp.editions.ListWithLocalImagePath(ctx); err != nil || len(eds) != 0 {
		t.Errorf("editions still holding a host path: %+v (err %v)", eds, err)
	}
	if bs, err := imp.books.ListWithLocalImagePath(ctx); err != nil || len(bs) != 0 {
		t.Errorf("books still holding a host path: %+v (err %v)", bs, err)
	}
}

// TestImporter_StoresCoverAsServableReference is the #2564 acceptance test:
// a Calibre book with a cover.jpg ends up with the same bindery-cover:
// reference on the book and on every edition, the bytes are in the store the
// image proxy serves from, and the library's absolute path is stored nowhere.
func TestImporter_StoresCoverAsServableReference(t *testing.T) {
	imp, fr, _, bookRepo, editionRepo, _, _ := newImporterFixture(t)
	store := covers.NewStore(filepath.Join(t.TempDir(), "covers"))
	imp.WithCoverStore(store)

	lib := t.TempDir()
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.Formats = append(cb.Formats, CalibreFormat{Format: "MOBI", FileName: "book", AbsolutePath: filepath.Join(lib, "book.mobi")})
	cb.CoverPath = writeLibraryCover(t, lib, cb, jpegBytes)
	fr.books = []CalibreBook{cb}

	if _, err := imp.Run(context.Background(), lib); err != nil {
		t.Fatalf("Run: %v", err)
	}

	book, err := bookRepo.GetByCalibreID(context.Background(), 1)
	if err != nil || book == nil {
		t.Fatalf("book: %v / %v", err, book)
	}
	if !covers.IsRef(book.ImageURL) {
		t.Fatalf("book image_url = %q, want a %s reference", book.ImageURL, covers.Scheme)
	}
	path, ct, ok := store.Resolve(book.ImageURL)
	if !ok || ct != "image/jpeg" {
		t.Fatalf("book cover %q does not resolve in the store (ok=%v ct=%q)", book.ImageURL, ok, ct)
	}
	if got, _ := os.ReadFile(path); string(got) != string(jpegBytes) {
		t.Errorf("stored cover bytes differ from the library's cover.jpg")
	}
	if strings.HasPrefix(path, lib) {
		t.Errorf("stored cover %q lives inside the library; it must be a copy in the data dir", path)
	}

	eds, _ := editionRepo.ListByBook(context.Background(), book.ID)
	if len(eds) != 2 {
		t.Fatalf("editions = %d, want 2", len(eds))
	}
	for _, e := range eds {
		if e.ImageURL != book.ImageURL {
			t.Errorf("edition %s image_url = %q, want the book's %q", e.Format, e.ImageURL, book.ImageURL)
		}
	}
	assertNoHostPaths(t, imp)
	if entries, _ := os.ReadDir(store.Dir()); len(entries) != 1 {
		t.Errorf("store holds %d files for one cover shared by two formats, want 1", len(entries))
	}

	// A re-import of the unchanged library keeps the same reference.
	if _, err := imp.Run(context.Background(), lib); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	again, _ := bookRepo.GetByCalibreID(context.Background(), 1)
	if again.ImageURL != book.ImageURL {
		t.Errorf("re-import changed book image_url to %q", again.ImageURL)
	}
}

// TestImporter_CoverDoesNotReplaceProviderCover: a book that already has a
// metadata provider cover keeps it. The Calibre cover is only the fallback.
func TestImporter_CoverDoesNotReplaceProviderCover(t *testing.T) {
	imp, fr, authorRepo, bookRepo, editionRepo, _, _ := newImporterFixture(t)
	imp.WithCoverStore(covers.NewStore(filepath.Join(t.TempDir(), "covers")))
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL1A", Name: "Alice Author", SortName: "Author, Alice", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	const provider = "https://assets.hardcover.app/editions/1/cover.jpg"
	b := &models.Book{
		ForeignID: "OL1W", AuthorID: a.ID, Title: "Book One", SortTitle: "Book One", ImageURL: provider,
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	lib := t.TempDir()
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.CoverPath = writeLibraryCover(t, lib, cb, jpegBytes)
	fr.books = []CalibreBook{cb}
	if _, err := imp.Run(ctx, lib); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, _ := bookRepo.GetByID(ctx, b.ID)
	if got.ImageURL != provider {
		t.Errorf("book image_url = %q, want the provider cover kept", got.ImageURL)
	}
	eds, _ := editionRepo.ListByBook(ctx, b.ID)
	if len(eds) != 1 || !covers.IsRef(eds[0].ImageURL) {
		t.Errorf("edition should still carry the stored Calibre cover: %+v", eds)
	}
}

// TestImporter_NoStoreNeverWritesHostPath: without a store wired (the
// pre-#2564 behaviour would have written the path) nothing is stored, and a
// row an older importer left with a host path is cleared on re-import.
func TestImporter_NoStoreNeverWritesHostPath(t *testing.T) {
	imp, fr, _, bookRepo, editionRepo, _, _ := newImporterFixture(t)
	ctx := context.Background()
	lib := t.TempDir()
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.CoverPath = writeLibraryCover(t, lib, cb, jpegBytes)
	fr.books = []CalibreBook{cb}

	if _, err := imp.Run(ctx, lib); err != nil {
		t.Fatalf("Run: %v", err)
	}
	book, _ := bookRepo.GetByCalibreID(ctx, 1)
	if book.ImageURL != "" {
		t.Errorf("book image_url = %q, want empty with no store", book.ImageURL)
	}
	eds, _ := editionRepo.ListByBook(ctx, book.ID)
	if len(eds) != 1 || eds[0].ImageURL != "" {
		t.Errorf("edition image_url = %+v, want empty with no store", eds)
	}

	// Simulate the pre-fix row and re-import: the host path must not survive.
	if err := editionRepo.SetImageURL(ctx, eds[0].ID, cb.CoverPath); err != nil {
		t.Fatal(err)
	}
	if _, err := imp.Run(ctx, lib); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	assertNoHostPaths(t, imp)
}

// TestImporter_RepairLocalCovers rewrites the rows an older importer left
// behind: editions holding the library's cover path get a stored reference,
// their book gets the same one when it had no cover, a book holding a host
// path of its own is rewritten too, and rows whose file is gone are left for
// the next pass rather than blanked.
func TestImporter_RepairLocalCovers(t *testing.T) {
	imp, _, authorRepo, bookRepo, editionRepo, _, _ := newImporterFixture(t)
	store := covers.NewStore(filepath.Join(t.TempDir(), "covers"))
	imp.WithCoverStore(store)
	ctx := context.Background()
	lib := t.TempDir()

	a := &models.Author{ForeignID: "calibre:author:1", Name: "Alice Author", SortName: "Author, Alice", MetadataProvider: "calibre", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	mkBook := func(fid, image string) *models.Book {
		b := &models.Book{
			ForeignID: fid, AuthorID: a.ID, Title: fid, SortTitle: fid, ImageURL: image,
			Status: "imported", Genres: []string{}, MetadataProvider: "calibre", Monitored: true,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	mkEdition := func(b *models.Book, format, image string) *models.Edition {
		e := &models.Edition{
			ForeignID: "calibre:edition:" + b.ForeignID + ":" + format, BookID: b.ID, Title: b.Title,
			Format: format, Language: "eng", ImageURL: image, IsEbook: true, Monitored: true,
		}
		if err := editionRepo.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}

	// Book 1: two editions sharing one readable cover, book has no cover.
	cover1 := writeLibraryCover(t, lib, CalibreBook{Title: "One", Authors: []CalibreAuthor{{Name: "Alice Author"}}}, jpegBytes)
	b1 := mkBook("one", "")
	e1a := mkEdition(b1, "EPUB", cover1)
	e1b := mkEdition(b1, "MOBI", cover1)

	// Book 2: edition path points at a file that no longer exists.
	b2 := mkBook("two", "")
	e2 := mkEdition(b2, "EPUB", filepath.Join(lib, "gone", "cover.jpg"))

	// Book 3: has a provider cover already; the edition is repaired but the
	// book keeps its own.
	const provider = "https://assets.hardcover.app/editions/3/cover.jpg"
	b3 := mkBook("three", provider)
	e3 := mkEdition(b3, "EPUB", cover1)

	// Book 4: the book row itself holds a host path.
	cover4 := writeLibraryCover(t, lib, CalibreBook{Title: "Four", Authors: []CalibreAuthor{{Name: "Alice Author"}}}, append([]byte{}, jpegBytes...))
	b4 := mkBook("four", cover4)

	stats, err := imp.RepairLocalCovers(ctx)
	if err != nil {
		t.Fatalf("RepairLocalCovers: %v", err)
	}
	if stats.EditionsRewritten != 3 || stats.BooksRewritten != 2 || stats.Unreadable != 1 {
		t.Errorf("stats = %+v, want editions=3 books=2 unreadable=1", stats)
	}

	for _, e := range []*models.Edition{e1a, e1b, e3} {
		got, _ := editionRepo.GetByID(ctx, e.ID)
		if !covers.IsRef(got.ImageURL) {
			t.Errorf("edition %d image_url = %q, want a stored reference", e.ID, got.ImageURL)
		}
	}
	gotB1, _ := bookRepo.GetByID(ctx, b1.ID)
	gotE1a, _ := editionRepo.GetByID(ctx, e1a.ID)
	if gotB1.ImageURL == "" || gotB1.ImageURL != gotE1a.ImageURL {
		t.Errorf("book 1 image_url = %q, want its edition's %q", gotB1.ImageURL, gotE1a.ImageURL)
	}
	gotE2, _ := editionRepo.GetByID(ctx, e2.ID)
	if gotE2.ImageURL != e2.ImageURL {
		t.Errorf("unreadable edition was changed to %q; it should be left for the next pass", gotE2.ImageURL)
	}
	gotB2, _ := bookRepo.GetByID(ctx, b2.ID)
	if gotB2.ImageURL != "" {
		t.Errorf("book 2 image_url = %q, want empty (its only cover was unreadable)", gotB2.ImageURL)
	}
	gotB3, _ := bookRepo.GetByID(ctx, b3.ID)
	if gotB3.ImageURL != provider {
		t.Errorf("book 3 image_url = %q, want the provider cover kept", gotB3.ImageURL)
	}
	gotB4, _ := bookRepo.GetByID(ctx, b4.ID)
	if !covers.IsRef(gotB4.ImageURL) {
		t.Errorf("book 4 image_url = %q, want a stored reference", gotB4.ImageURL)
	}

	// Second pass: only the unreadable row is looked at again, nothing rewritten.
	stats, err = imp.RepairLocalCovers(ctx)
	if err != nil {
		t.Fatalf("second RepairLocalCovers: %v", err)
	}
	if stats.EditionsRewritten != 0 || stats.BooksRewritten != 0 || stats.Unreadable != 1 {
		t.Errorf("second pass stats = %+v, want nothing rewritten and 1 unreadable", stats)
	}

	// No store: a no-op, not an error.
	imp.covers = nil
	if stats, err := imp.RepairLocalCovers(ctx); err != nil || stats != (CoverRepairStats{}) {
		t.Errorf("no store: stats=%+v err=%v", stats, err)
	}
}

// TestImporter_RollbackRestoresBookCover: rolling back an import that gave a
// pre-existing book its Calibre cover puts the previous (empty) value back.
func TestImporter_RollbackRestoresBookCover(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, runsRepo, _, _ := newRollbackFixture(t)
	imp.WithCoverStore(covers.NewStore(filepath.Join(t.TempDir(), "covers")))
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL9A", Name: "Alice Author", SortName: "Author, Alice", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL9W", AuthorID: a.ID, Title: "Book One", SortTitle: "Book One",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	lib := t.TempDir()
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.CoverPath = writeLibraryCover(t, lib, cb, jpegBytes)
	fr.books = []CalibreBook{cb}
	if _, err := imp.Run(ctx, lib); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := bookRepo.GetByID(ctx, b.ID); !covers.IsRef(got.ImageURL) {
		t.Fatalf("book image_url after import = %q, want a stored reference", got.ImageURL)
	}

	runs, _ := runsRepo.ListRecent(ctx, 1)
	if _, err := imp.Rollback(ctx, runs[0].ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	got, _ := bookRepo.GetByID(ctx, b.ID)
	if got == nil {
		t.Fatal("pre-existing book deleted by rollback")
	}
	if got.ImageURL != "" {
		t.Errorf("book image_url after rollback = %q, want empty", got.ImageURL)
	}
}
