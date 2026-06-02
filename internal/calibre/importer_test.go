package calibre

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// newImporterFixture wires an Importer against an in-memory Bindery DB plus
// a configurable fakeReader. Tests set fakeReader.books directly so they
// can exercise matcher logic without rebuilding a SQLite fixture each run.
func newImporterFixture(t *testing.T) (*Importer, *fakeReader, *db.AuthorRepo, *db.BookRepo, *db.EditionRepo, *db.AuthorAliasRepo, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	fr := &fakeReader{}
	imp := NewImporter(authorRepo, aliasRepo, bookRepo, editionRepo, settingsRepo)
	imp.openReader = func(string) (readerIface, error) { return fr, nil }

	return imp, fr, authorRepo, bookRepo, editionRepo, aliasRepo, settingsRepo
}

// fakeReader lets tests hand the importer a canned []CalibreBook without
// touching disk. It satisfies readerIface.
type fakeReader struct {
	books []CalibreBook
	err   error
}

func (f *fakeReader) Count(_ context.Context) (int, error) { return len(f.books), nil }
func (f *fakeReader) Close() error                         { return nil }
func (f *fakeReader) Books(_ context.Context, fn func(CalibreBook) error) error {
	if f.err != nil {
		return f.err
	}
	for _, b := range f.books {
		if err := fn(b); err != nil {
			return err
		}
	}
	return nil
}

func sampleCalibreBook(id int64, title, authorName string) CalibreBook {
	return CalibreBook{
		CalibreID: id,
		Title:     title,
		SortTitle: title,
		Authors:   []CalibreAuthor{{CalibreID: id, Name: authorName, Sort: authorName}},
		Formats: []CalibreFormat{
			{Format: "EPUB", FileName: "book", AbsolutePath: filepath.Join("/lib", title+".epub")},
		},
	}
}

func TestImporter_HappyPath_CreatesAuthorsBooksEditions(t *testing.T) {
	imp, fr, _, bookRepo, editionRepo, _, settingsRepo := newImporterFixture(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(1, "Book One", "Alice Author"),
		sampleCalibreBook(2, "Book Two", "Alice Author"),
	}

	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsAdded != 1 || stats.AuthorsLinked != 1 {
		t.Errorf("authors: added=%d linked=%d want 1/1", stats.AuthorsAdded, stats.AuthorsLinked)
	}
	if stats.BooksAdded != 2 || stats.BooksUpdated != 0 {
		t.Errorf("books: added=%d updated=%d want 2/0", stats.BooksAdded, stats.BooksUpdated)
	}
	if stats.EditionsAdded != 2 {
		t.Errorf("editions added = %d, want 2", stats.EditionsAdded)
	}

	// calibre_id must land on both book rows — Path B + OPDS cross-reference
	// depends on it being non-null.
	b1, err := bookRepo.GetByCalibreID(context.Background(), 1)
	if err != nil || b1 == nil {
		t.Fatalf("book 1 by calibre_id: %v / %v", err, b1)
	}
	if b1.CalibreID == nil || *b1.CalibreID != 1 {
		t.Errorf("book 1 calibre_id = %v, want 1", b1.CalibreID)
	}

	// one edition per book
	eds, _ := editionRepo.ListByBook(context.Background(), b1.ID)
	if len(eds) != 1 || eds[0].Format != "EPUB" {
		t.Errorf("book 1 editions = %+v", eds)
	}

	// last_import_at stamped
	s, _ := settingsRepo.Get(context.Background(), "calibre.last_import_at")
	if s == nil || s.Value == "" {
		t.Error("last_import_at should be stamped after a successful run")
	} else if _, err := time.Parse(time.RFC3339, s.Value); err != nil {
		t.Errorf("last_import_at not RFC3339: %v", err)
	}
}

// TestImporter_Idempotent — running twice must not duplicate rows. This
// is the primary acceptance criterion ("running import twice diffs-only").
func TestImporter_Idempotent(t *testing.T) {
	imp, fr, authorRepo, bookRepo, editionRepo, _, _ := newImporterFixture(t)
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}

	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	// Second run sees the existing rows and should mark them updated, not
	// added. Duplicate counts would mean we failed the calibre_id lookup.
	if stats.BooksAdded != 0 || stats.BooksUpdated != 1 {
		t.Errorf("second run books: added=%d updated=%d want 0/1", stats.BooksAdded, stats.BooksUpdated)
	}
	if stats.EditionsAdded != 0 {
		t.Errorf("second run should not add editions, got %d", stats.EditionsAdded)
	}

	authors, _ := authorRepo.List(context.Background())
	if len(authors) != 1 {
		t.Errorf("want 1 author after re-import, got %d", len(authors))
	}
	books, _ := bookRepo.List(context.Background())
	if len(books) != 1 {
		t.Errorf("want 1 book after re-import, got %d", len(books))
	}
	eds, _ := editionRepo.ListByBook(context.Background(), books[0].ID)
	if len(eds) != 1 {
		t.Errorf("want 1 edition after re-import, got %d", len(eds))
	}
}

// TestImporter_ReusesExistingAuthor — when a Bindery author already
// exists with the same name, the importer must link (not duplicate).
func TestImporter_ReusesExistingAuthor(t *testing.T) {
	imp, fr, authorRepo, _, _, _, _ := newImporterFixture(t)

	existing := &models.Author{
		ForeignID: "ol:A1", Name: "Alice Author", SortName: "Author, Alice",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(context.Background(), existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}

	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}

	authors, _ := authorRepo.List(context.Background())
	if len(authors) != 1 {
		t.Errorf("want 1 author (re-used), got %d", len(authors))
	}
	if authors[0].ForeignID != "ol:A1" {
		t.Errorf("expected to link to existing OL author, got foreign_id=%q", authors[0].ForeignID)
	}
}

// TestImporter_AliasResolvesToCanonical — if Calibre's author name matches
// an existing alias, the importer must route books under the alias' target
// rather than creating a new author row.
func TestImporter_AliasResolvesToCanonical(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, aliasRepo, _ := newImporterFixture(t)

	canonical := &models.Author{
		ForeignID: "ol:RRH", Name: "R.R. Haywood", SortName: "Haywood, R.R.",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(context.Background(), canonical); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := aliasRepo.Create(context.Background(), &models.AuthorAlias{
		AuthorID: canonical.ID, Name: "RR Haywood",
	}); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	fr.books = []CalibreBook{sampleCalibreBook(1, "The Undead", "RR Haywood")}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}
	authors, _ := authorRepo.List(context.Background())
	if len(authors) != 1 {
		t.Errorf("alias resolution should not create a new author, got %d total", len(authors))
	}
	books, _ := bookRepo.ListByAuthor(context.Background(), canonical.ID)
	if len(books) != 1 {
		t.Errorf("book should be filed under canonical, got %d", len(books))
	}
}

// TestImporter_MergesByTitle — if a Bindery book with the same author +
// title exists but has no calibre_id, the importer must link it in place
// and bump DuplicatesMerged rather than creating a parallel row.
func TestImporter_MergesByTitle(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)

	author := &models.Author{
		ForeignID: "ol:A1", Name: "Alice Author", SortName: "Author, Alice",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(context.Background(), author); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	prior := &models.Book{
		ForeignID: "ol:B1", AuthorID: author.ID, Title: "Book One", SortTitle: "Book One",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(context.Background(), prior); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	fr.books = []CalibreBook{sampleCalibreBook(42, "Book One", "Alice Author")}
	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.DuplicatesMerged != 1 {
		t.Errorf("DuplicatesMerged = %d, want 1", stats.DuplicatesMerged)
	}
	books, _ := bookRepo.List(context.Background())
	if len(books) != 1 {
		t.Fatalf("want 1 book after merge, got %d", len(books))
	}
	if books[0].CalibreID == nil || *books[0].CalibreID != 42 {
		t.Errorf("merged book calibre_id = %v, want 42", books[0].CalibreID)
	}
}

// TestImporter_SkipsBooksWithoutAuthors — a Calibre book with no author
// rows is a data error (Calibre requires at least one). We log + skip
// rather than crashing, and bump Skipped so the UI surfaces it.
func TestImporter_SkipsBooksWithoutAuthors(t *testing.T) {
	imp, fr, _, _, _, _, _ := newImporterFixture(t)
	fr.books = []CalibreBook{{CalibreID: 1, Title: "Orphan"}}
	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
	if stats.BooksAdded != 0 {
		t.Error("no book should be added when author is missing")
	}
}

// TestImporter_SecondaryAuthorsBecomeAliases — Calibre books with
// multiple authors are stored as (canonical, aliases) in Bindery. The
// alias rows let future imports that present the co-author as primary
// find the same row.
func TestImporter_SecondaryAuthorsBecomeAliases(t *testing.T) {
	imp, fr, _, _, _, aliasRepo, _ := newImporterFixture(t)
	fr.books = []CalibreBook{{
		CalibreID: 1, Title: "Collab", SortTitle: "Collab",
		Authors: []CalibreAuthor{
			{CalibreID: 1, Name: "Alice Author"},
			{CalibreID: 2, Name: "Carol Coauthor"},
		},
		Formats: []CalibreFormat{{Format: "EPUB", FileName: "c", AbsolutePath: "/x.epub"}},
	}}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Look up the alias by name; it should point at the first author.
	id, err := aliasRepo.LookupByName(context.Background(), "Carol Coauthor")
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("secondary author should be recorded as alias")
	}
}

// TestImporter_AlreadyRunningRejected locks in the 409 contract — two
// simultaneous clicks on the Import button should not race each other.
func TestImporter_AlreadyRunningRejected(t *testing.T) {
	imp, fr, _, _, _, _, _ := newImporterFixture(t)
	block := make(chan struct{})
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice")}
	// Replace Books with a version that blocks until we unblock it, so a
	// second Start arrives while the first is still mid-run.
	orig := fr.Books
	var blocking readerFn = func(ctx context.Context, fn func(CalibreBook) error) error {
		<-block
		return orig(ctx, fn)
	}
	imp.openReader = func(string) (readerIface, error) {
		return &blockingReader{fakeReader: fr, booksFn: blocking}, nil
	}

	if err := imp.Start(context.Background(), "/lib"); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := imp.Start(context.Background(), "/lib"); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second start err = %v, want ErrAlreadyRunning", err)
	}
	close(block)
	// Drain the running goroutine before the test ends.
	for i := 0; i < 200 && imp.Running(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if imp.Running() {
		t.Fatal("import did not complete")
	}
}

type readerFn func(ctx context.Context, fn func(CalibreBook) error) error

type blockingReader struct {
	*fakeReader
	booksFn readerFn
}

func (b *blockingReader) Books(ctx context.Context, fn func(CalibreBook) error) error {
	return b.booksFn(ctx, fn)
}

// TestImporter_ReaderOpenFailureSurfacesInProgress — a bad library_path
// must surface via the polling endpoint rather than leaving the UI stuck
// on "running".
func TestImporter_ReaderOpenFailureSurfacesInProgress(t *testing.T) {
	imp, _, _, _, _, _, _ := newImporterFixture(t)
	imp.openReader = func(string) (readerIface, error) { return nil, errors.New("boom") }

	if _, err := imp.Run(context.Background(), "/lib"); err == nil {
		t.Fatal("expected error")
	}
	p := imp.Progress()
	if p.Error == "" {
		t.Error("progress.Error should capture failure")
	}
	if p.Running {
		t.Error("progress.Running should be false after failure")
	}
}

// TestImporter_PersistsSeries is the #905 regression guard. The Calibre
// reader extracts series memberships into CalibreBook.Series, but until
// this fix the importer ignored the field. The acceptance criterion is
// that a Calibre book with a series + position lands a series row, a
// series_books link, and a parseable position string.
func TestImporter_PersistsSeries(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newImporterFixture(t)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	// Rebuild the fixture so series repo is wired against the same DB.
	authorRepo := db.NewAuthorRepo(database)
	bookRepo = db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	imp = NewImporter(authorRepo, aliasRepo, bookRepo, editionRepo, settingsRepo).
		WithSeries(seriesRepo)
	imp.openReader = func(string) (readerIface, error) { return fr, nil }

	cb := sampleCalibreBook(1, "Weapons and Wielders 1", "Andrew Rowe")
	cb.Series = &CalibreSeries{Name: "Weapons and Wielders", Position: 1.0}
	fr.books = []CalibreBook{cb}

	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesLinked != 1 || stats.SeriesFailures != 0 {
		t.Errorf("series stats: linked=%d failures=%d, want 1/0", stats.SeriesLinked, stats.SeriesFailures)
	}

	all, err := seriesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("seriesRepo.List: %v", err)
	}
	if len(all) != 1 || all[0].Title != "Weapons and Wielders" {
		t.Fatalf("expected one series 'Weapons and Wielders', got %+v", all)
	}
	if all[0].ForeignID != "calibre:series:weapons and wielders" {
		t.Errorf("foreign id = %q, want calibre:series:weapons and wielders", all[0].ForeignID)
	}

	// series_books link present with position "1"
	book, _ := bookRepo.GetByCalibreID(context.Background(), 1)
	if book == nil {
		t.Fatal("book not found post-import")
	}
	books, err := seriesRepo.ListBooksInSeries(context.Background(), all[0].ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries: %v", err)
	}
	if len(books) != 1 || books[0].ID != book.ID {
		t.Errorf("expected the book in the series, got %+v", books)
	}
}

// TestImporter_SkipsSeriesWhenRepoUnset confirms the back-compat: an
// importer constructed without WithSeries (legacy test fixtures, embedders)
// keeps working and silently skips series creation.
func TestImporter_SkipsSeriesWhenRepoUnset(t *testing.T) {
	imp, fr, _, _, _, _, _ := newImporterFixture(t)
	cb := sampleCalibreBook(1, "Solo Title", "Solo Author")
	cb.Series = &CalibreSeries{Name: "Phantom Series", Position: 2.0}
	fr.books = []CalibreBook{cb}

	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesLinked != 0 || stats.SeriesFailures != 0 {
		t.Errorf("series stats with no repo: linked=%d failures=%d, want 0/0", stats.SeriesLinked, stats.SeriesFailures)
	}
}
