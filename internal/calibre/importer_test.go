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
	identifier, err := authorRepo.GetAuthorIdentifier(context.Background(), "calibre:author:1")
	if err != nil {
		t.Fatal(err)
	}
	if identifier == nil || identifier.AuthorID != existing.ID {
		t.Fatalf("calibre identifier = %+v, want linked to existing author", identifier)
	}
}

func TestImporter_ReusesRelinkedAuthorByCalibreIdentifier(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL-A1",
		Name:             "Alice Author",
		SortName:         "Author, Alice",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, existing.ID, "calibre:author:7"); err != nil {
		t.Fatal(err)
	}

	fr.books = []CalibreBook{{
		CalibreID: 42,
		Title:     "Book One",
		SortTitle: "Book One",
		Authors:   []CalibreAuthor{{CalibreID: 7, Name: "A. Author", Sort: "Author, A."}},
		Formats: []CalibreFormat{
			{Format: "EPUB", FileName: "book", AbsolutePath: filepath.Join("/lib", "Book One.epub")},
		},
	}}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	if authors[0].ID != existing.ID || authors[0].ForeignID != "OL-A1" {
		t.Fatalf("author = %+v, want existing relinked author", authors[0])
	}
	books, err := bookRepo.ListByAuthor(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want imported book under existing author", len(books))
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

// collabBook is a Calibre book credited to two people, the shape at the
// centre of #1684.
func collabBook(id int64, title string, authors ...CalibreAuthor) CalibreBook {
	return CalibreBook{
		CalibreID: id, Title: title, SortTitle: title,
		Authors: authors,
		Formats: []CalibreFormat{{Format: "EPUB", FileName: "c", AbsolutePath: "/lib/" + title + ".epub"}},
	}
}

// TestImporter_SecondaryAuthorsAreNotAliases: a co-author is a different
// person who happened to share a cover, not another name for the primary
// author. Recording them as aliases (which this importer used to do) made
// the alias table claim names it had no business claiming (#1684).
//
// This test previously asserted the opposite; the behaviour it locked in
// was the bug.
func TestImporter_SecondaryAuthorsAreNotAliases(t *testing.T) {
	imp, fr, _, _, _, aliasRepo, _ := newImporterFixture(t)
	fr.books = []CalibreBook{collabBook(1, "Collab",
		CalibreAuthor{CalibreID: 1, Name: "Alice Author"},
		CalibreAuthor{CalibreID: 2, Name: "Carol Coauthor"},
	)}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}
	id, err := aliasRepo.LookupByName(context.Background(), "Carol Coauthor")
	if err != nil {
		t.Fatal(err)
	}
	if id != nil {
		t.Fatalf("co-author recorded as alias of author %d; co-authors must not become aliases", *id)
	}
}

// TestImporter_CoAuthorLaterCreditedAloneGetsOwnAuthor is the user-visible
// regression from #1684. Both reporters lost whole back-catalogues this way:
// a collaboration is imported first, its co-author becomes an alias of the
// primary, and every later book by that co-author resolves through the alias
// and is filed under the wrong person, who is never created at all.
//
// Which author survives depended on Calibre book id order, which is why the
// symptom looked random.
func TestImporter_CoAuthorLaterCreditedAloneGetsOwnAuthor(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)
	fr.books = []CalibreBook{
		// Lower calibre id, so this one is processed first.
		collabBook(1, "Kem Antilles Collab",
			CalibreAuthor{CalibreID: 1, Name: "Kem Antilles"},
			CalibreAuthor{CalibreID: 2, Name: "Kevin J. Anderson"},
		),
		collabBook(2, "Solo Novel", CalibreAuthor{CalibreID: 2, Name: "Kevin J. Anderson"}),
	}
	ctx := context.Background()
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]int64, len(authors))
	for _, a := range authors {
		byName[a.Name] = a.ID
	}
	andersonID, ok := byName["Kevin J. Anderson"]
	if !ok {
		t.Fatalf("co-author never got an author row; authors = %v", byName)
	}
	if _, ok := byName["Kem Antilles"]; !ok {
		t.Fatalf("primary author missing; authors = %v", byName)
	}

	solo, err := bookRepo.GetByCalibreID(ctx, 2)
	if err != nil || solo == nil {
		t.Fatalf("solo book: %v / %v", err, solo)
	}
	if solo.AuthorID != andersonID {
		t.Errorf("solo book filed under author %d, want Kevin J. Anderson (%d)", solo.AuthorID, andersonID)
	}
}

// TestImporter_IgnoresUntrustedPreExistingAlias covers the installs that are
// already polluted. New imports no longer mint co-author aliases, but the
// rows minted by older versions are still in the table and would keep
// swallowing authors forever. An unattributed alias whose name looks nothing
// like the author it points at is not evidence of identity, so it no longer
// decides who an incoming Calibre credit is. Crucially the row is left alone
// rather than deleted, because nothing in the schema distinguishes it from a
// legitimate one.
func TestImporter_IgnoresUntrustedPreExistingAlias(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, aliasRepo, _ := newImporterFixture(t)
	ctx := context.Background()

	pseudonym := &models.Author{ForeignID: "calibre:author:99", Name: "Kem Antilles", SortName: "Antilles, Kem"}
	if err := authorRepo.Create(ctx, pseudonym); err != nil {
		t.Fatal(err)
	}
	// The shape an older import left behind: no source id, name unrelated.
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: pseudonym.ID, Name: "Kevin J. Anderson"}); err != nil {
		t.Fatal(err)
	}

	fr.books = []CalibreBook{collabBook(5, "Solo Novel", CalibreAuthor{CalibreID: 2, Name: "Kevin J. Anderson"})}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}

	book, err := bookRepo.GetByCalibreID(ctx, 5)
	if err != nil || book == nil {
		t.Fatalf("book: %v / %v", err, book)
	}
	if book.AuthorID == pseudonym.ID {
		t.Fatal("book was filed under the pseudonym via an untrusted alias")
	}
	author, err := authorRepo.GetByID(ctx, book.AuthorID)
	if err != nil || author == nil {
		t.Fatalf("author: %v / %v", err, author)
	}
	if author.Name != "Kevin J. Anderson" {
		t.Errorf("book filed under %q, want a fresh %q row", author.Name, "Kevin J. Anderson")
	}

	// No data loss: the alias row survives, so indexer search keeps expanding
	// on it and the user can remove it from the author page if they want to.
	aliases, err := aliasRepo.ListByAuthor(ctx, pseudonym.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "Kevin J. Anderson" {
		t.Errorf("alias rows = %+v, want the pre-existing row left untouched", aliases)
	}
}

// TestImporter_TrustedAliasesStillResolve guards the other side of the trust
// rule: aliases that genuinely assert identity must keep working, or fixing
// #1684 would just trade one duplicate-author bug for another.
func TestImporter_TrustedAliasesStillResolve(t *testing.T) {
	cases := []struct {
		name       string
		authorName string
		sortName   string
		alias      models.AuthorAlias
		credit     string
	}{
		{
			// Merged-away author / provider record: something asserted these
			// are the same human, so the source id is the assertion.
			name: "provenanced alias", authorName: "Samuel Clemens", sortName: "Clemens, Samuel",
			alias: models.AuthorAlias{Name: "Mark Twain", SourceOLID: "OL18319A"}, credit: "Mark Twain",
		},
		{
			// A punctuation variant of the same name is what an alias table
			// is for; no provenance needed.
			name: "name variant", authorName: "R.R. Haywood", sortName: "Haywood, R.R.",
			alias: models.AuthorAlias{Name: "RR Haywood"}, credit: "RR Haywood",
		},
		{
			// saveAlternateNames' reason to exist: a latin-script name for a
			// non-latin author, which no matcher can connect by spelling.
			name: "latin alias of non-latin author", authorName: "村上春樹", sortName: "村上春樹",
			alias: models.AuthorAlias{Name: "Haruki Murakami"}, credit: "Haruki Murakami",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp, fr, authorRepo, bookRepo, _, aliasRepo, _ := newImporterFixture(t)
			ctx := context.Background()

			author := &models.Author{ForeignID: "ol:" + tc.authorName, Name: tc.authorName, SortName: tc.sortName}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			alias := tc.alias
			alias.AuthorID = author.ID
			if err := aliasRepo.Create(ctx, &alias); err != nil {
				t.Fatal(err)
			}

			fr.books = []CalibreBook{collabBook(3, "Some Book", CalibreAuthor{CalibreID: 3, Name: tc.credit})}
			if _, err := imp.Run(ctx, "/lib"); err != nil {
				t.Fatalf("run: %v", err)
			}

			book, err := bookRepo.GetByCalibreID(ctx, 3)
			if err != nil || book == nil {
				t.Fatalf("book: %v / %v", err, book)
			}
			if book.AuthorID != author.ID {
				t.Errorf("book filed under author %d, want %q (%d) via its alias", book.AuthorID, tc.authorName, author.ID)
			}
		})
	}
}

// TestAliasBindsAuthor_SharedLatinRule pins aliasBindsAuthor to the shared
// textutil.LatinAliasBinds rule (#2419). The unattributed-alias branch used to
// test for 7-bit ASCII, which made every accented latin name count as "another
// script": an unattributed alias sitting beside "Jo Nesbø" bound to him, so a
// Calibre credit for a completely different author was filed under Nesbø.
func TestAliasBindsAuthor_SharedLatinRule(t *testing.T) {
	cases := []struct {
		name      string
		canonical string
		alias     models.AuthorAlias
		want      bool
	}{
		{
			// An accented latin name is latin script. An unattributed alias
			// beside it says nothing; before #2419 this returned true.
			name:      "accented latin canonical does not bind unrelated alias",
			canonical: "Jo Nesbø", alias: models.AuthorAlias{Name: "Karin Fossum"}, want: false,
		},
		{
			// A mixed-script name is non-latin on the full-name rule.
			name:      "mixed-script canonical binds latin alias",
			canonical: "村上 Haruki", alias: models.AuthorAlias{Name: "Haruki Murakami"}, want: true,
		},
		{
			// An accented latin romanisation is still a latin alias; before
			// #2419 the "ø" made it non-ASCII and it was refused.
			name:      "accented latin alias binds non-latin canonical",
			canonical: "村上春樹", alias: models.AuthorAlias{Name: "Haruki Murakamø"}, want: true,
		},
		{
			name:      "plain latin canonical does not bind unrelated alias",
			canonical: "Kem Antilles", alias: models.AuthorAlias{Name: "Rebecca Moesta"}, want: false,
		},
		{
			// Provenance still wins regardless of script.
			name:      "provenanced alias binds a latin canonical",
			canonical: "Samuel Clemens", alias: models.AuthorAlias{Name: "Mark Twain", SourceOLID: "OL18319A"}, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical := &models.Author{Name: tc.canonical}
			if got := aliasBindsAuthor(tc.alias, canonical); got != tc.want {
				t.Errorf("aliasBindsAuthor(%q, %q) = %v, want %v", tc.alias.Name, tc.canonical, got, tc.want)
			}
		})
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
	// Hand-roll the fixture because newImporterFixture doesn't wire a
	// series repo, and #905 specifically exercises that path. Fresh DB
	// per-test so the series_books rows can be asserted in isolation.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	fr := &fakeReader{}
	imp := NewImporter(authorRepo, aliasRepo, bookRepo, editionRepo, settingsRepo).
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

// RunSync must not import when calibre.library_import_enabled is off, even with
// a library path set, and must import when it is on (#calibre-import-opt-in).
func TestRunSync_GatedOnLibraryImportEnabled(t *testing.T) {
	imp, fr, _, bookRepo, _, _, settingsRepo := newImporterFixture(t)
	ctx := context.Background()
	fr.books = []CalibreBook{sampleCalibreBook(1, "Gated Book", "Some Author")}
	if err := settingsRepo.Set(ctx, "calibre.library_path", "/lib"); err != nil {
		t.Fatal(err)
	}

	// Disabled (setting absent): no import.
	imp.RunSync(ctx)
	if books, _ := bookRepo.List(ctx); len(books) != 0 {
		t.Fatalf("RunSync imported %d books while library import disabled; want 0", len(books))
	}

	// Enabled: import proceeds.
	if err := settingsRepo.Set(ctx, "calibre.library_import_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	imp.RunSync(ctx)
	if books, _ := bookRepo.List(ctx); len(books) == 0 {
		t.Fatal("RunSync imported nothing after enabling library import; want the fake book")
	}
}

// TestImporter_TracksBookFiles is the second half of #1635. The import
// reconciled title, author and series from metadata.db but left book_files
// untouched, so a Calibre-managed book had its path recorded only in the
// legacy books.file_path column, or nowhere at all on the freshly-created
// path. Anything reading tracked files saw a book that had none.
func TestImporter_TracksBookFiles(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newImporterFixture(t)
	book := sampleCalibreBook(1, "Book One", "Alice Author")
	// A combined item: Calibre is the authority on what the book has, and an
	// audiobook beside the epub must be tracked as its own format rather than
	// lost behind the first entry.
	book.Formats = append(book.Formats, CalibreFormat{
		Format: "M4B", FileName: "book", AbsolutePath: filepath.Join("/lib", "Book One.m4b"),
	})
	fr.books = []CalibreBook{book}

	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	files, err := bookRepo.ListFiles(context.Background(), books[0].ID)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Format] = f.Path
	}
	want := map[string]string{
		models.MediaTypeEbook:     filepath.Join("/lib", "Book One.epub"),
		models.MediaTypeAudiobook: filepath.Join("/lib", "Book One.m4b"),
	}
	for format, wantPath := range want {
		if got[format] != wantPath {
			t.Errorf("tracked %s path = %q, want %q (all rows: %+v)", format, got[format], wantPath, files)
		}
	}
}

// A re-import must not accumulate duplicate rows for a file that has not moved.
func TestImporter_ReimportDoesNotDuplicateBookFiles(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newImporterFixture(t)
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}

	for i := 0; i < 3; i++ {
		if _, err := imp.Run(context.Background(), "/lib"); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	files, err := bookRepo.ListFiles(context.Background(), books[0].ID)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("tracked files after three imports = %d, want 1: %+v", len(files), files)
	}
}

func TestCalibreFormatMediaType(t *testing.T) {
	for format, want := range map[string]string{
		"EPUB": models.MediaTypeEbook,
		"MOBI": models.MediaTypeEbook,
		"PDF":  models.MediaTypeEbook,
		"AZW3": models.MediaTypeEbook,
		"M4B":  models.MediaTypeAudiobook,
		"mp3":  models.MediaTypeAudiobook,
		" M4A": models.MediaTypeAudiobook,
		"":     models.MediaTypeEbook,
	} {
		if got := calibreFormatMediaType(format); got != want {
			t.Errorf("calibreFormatMediaType(%q) = %q, want %q", format, got, want)
		}
	}
}

// A format Calibre reports with no resolved path is skipped rather than
// tracked as an empty string, and a book with no formats at all tracks
// nothing without failing the import.
func TestImporter_SkipsFormatsWithoutAPath(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newImporterFixture(t)

	withBlank := sampleCalibreBook(1, "Book One", "Alice Author")
	withBlank.Formats = append(withBlank.Formats, CalibreFormat{Format: "MOBI", FileName: "book"})
	noFormats := sampleCalibreBook(2, "Book Two", "Alice Author")
	noFormats.Formats = nil
	fr.books = []CalibreBook{withBlank, noFormats}

	stats, err := imp.Run(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BooksAdded != 2 {
		t.Fatalf("booksAdded = %d, want 2 (a pathless format must not fail the import)", stats.BooksAdded)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	byTitle := map[string]int64{}
	for _, b := range books {
		byTitle[b.Title] = b.ID
	}
	one, err := bookRepo.ListFiles(context.Background(), byTitle["Book One"])
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(one) != 1 || one[0].Path != filepath.Join("/lib", "Book One.epub") {
		t.Errorf("Book One tracked files = %+v, want only the epub", one)
	}
	two, err := bookRepo.ListFiles(context.Background(), byTitle["Book Two"])
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(two) != 0 {
		t.Errorf("Book Two tracked files = %+v, want none", two)
	}
}

// registerBookFiles is defensive about a book it cannot track against; the
// guard is cheap and the import must not panic on a partially built row.
func TestImporter_RegisterBookFilesIgnoresUnusableBooks(t *testing.T) {
	imp, _, _, _, _, _, _ := newImporterFixture(t)
	cb := sampleCalibreBook(1, "Book One", "Alice Author")

	imp.registerBookFiles(context.Background(), nil, cb)
	imp.registerBookFiles(context.Background(), &models.Book{ID: 0}, cb)

	noRepo := &Importer{}
	noRepo.registerBookFiles(context.Background(), &models.Book{ID: 1}, cb)
}
