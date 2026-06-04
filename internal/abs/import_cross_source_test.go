package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedCalibreStyleBook creates a book the way the Calibre importer (or any
// other create path) would: a plain Create, which now populates dedup_key from
// the title via indexer.CanonicalDedupKey. Returns the row.
func seedCalibreStyleBook(t *testing.T, bookRepo *db.BookRepo, authorID int64, fid, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID:        fid,
		AuthorID:         authorID,
		Title:            title,
		SortTitle:        title,
		Status:           models.BookStatusImported,
		Genres:           []string{},
		MetadataProvider: "calibre",
		MediaType:        models.MediaTypeEbook,
		Monitored:        true,
	}
	if err := bookRepo.Create(context.Background(), b); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	return b
}

func countBooksForAuthor(t *testing.T, bookRepo *db.BookRepo, authorID int64) int {
	t.Helper()
	books, err := bookRepo.ListByAuthorIncludingExcluded(context.Background(), authorID)
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	return len(books)
}

// TestImporter_CrossSourceBindsByDedupKey is the #940 importer-level
// regression. A book already filed by Calibre under one title form must be
// LINKED (not duplicated) when ABS imports the same work under a differing but
// canonically-equal title form — for subtitle, case, and umlaut variants.
func TestImporter_CrossSourceBindsByDedupKey(t *testing.T) {
	cases := []struct {
		name         string
		calibreTitle string
		absTitle     string
	}{
		{"abs drops subtitle calibre keeps it", "Mistborn: The Final Empire", "Mistborn"},
		{"abs adds bracket qualifier", "The Way of Kings", "The Way of Kings [Unabridged]"},
		{"case differs", "Elantris", "ELANTRIS"},
		{"umlaut form differs", "Die Straße", "Die Strasse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			ctx := context.Background()

			author := &models.Author{ForeignID: "OL-CS", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", Monitored: true, MetadataProvider: "openlibrary"}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			seedCalibreStyleBook(t, bookRepo, author.ID, "calibre:book:1", tc.calibreTitle)

			if got := countBooksForAuthor(t, bookRepo, author.ID); got != 1 {
				t.Fatalf("precondition: expected 1 seeded book, got %d", got)
			}

			item := sampleABSItem()
			item.ItemID = "li-cross-source"
			item.Title = tc.absTitle
			item.ASIN = ""
			item.Series = nil
			item.Authors = []NormalizedAuthor{{ID: "author-cs", Name: "Brandon Sanderson"}}
			runSingleABSImport(t, importer, item)

			if got := countBooksForAuthor(t, bookRepo, author.ID); got != 1 {
				t.Fatalf("ABS import duplicated the book: expected 1 row, got %d (calibre=%q abs=%q)", got, tc.calibreTitle, tc.absTitle)
			}
		})
	}
}

// TestImporter_ABSFirstThenReimportDoesNotDuplicate proves the opposite order
// and the resurrection case: ABS creates the row, and re-running the same ABS
// import binds to it instead of creating a second row (#940's "re-running
// resurrected deleted rows" symptom).
func TestImporter_ABSFirstThenReimportDoesNotDuplicate(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-RE", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-reimport"
	item.Title = "Artemis: A Novel"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-weir", Name: "Andy Weir"}}

	runSingleABSImport(t, importer, item)
	if got := countBooksForAuthor(t, bookRepo, author.ID); got != 1 {
		t.Fatalf("first import: expected 1 book, got %d", got)
	}

	// Re-run with the subtitle-less form a different scan might present.
	item.Title = "Artemis"
	runSingleABSImport(t, importer, item)
	if got := countBooksForAuthor(t, bookRepo, author.ID); got != 1 {
		t.Fatalf("re-import duplicated: expected 1 book, got %d", got)
	}
}
