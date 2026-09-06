package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Audiobookshelf hands back whatever casing the item's metadata was written
// with, and its own ASIN field is not normalized. Every metadata provider in
// Bindery upper-cases an ASIN; the ABS import used to only trim one, so the
// same book could hold "b0b2rjrf1k" from a shelf scan and "B0B2RJRF1K" from
// Audible, and importer.Lookup's `books[i].ASIN == parsed.ASIN` tier — whose
// other side comes from a filename regexp that is upper case by construction —
// could never match the first.
//
// The tests below cover every ABS site that writes an ASIN somewhere it is
// read back from.
const (
	lowerASIN = "  b0b2rjrf1k "
	upperASIN = "B0B2RJRF1K"
)

func asinTestAuthor(t *testing.T, importer *Importer) *models.Author {
	t.Helper()
	author := &models.Author{
		ForeignID:        "OL23919A",
		Name:             "Andy Weir",
		SortName:         "Weir, Andy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := importer.authors.Create(context.Background(), author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	return author
}

func asinTestConfig() ImportConfig {
	return ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	}
}

// upsertBook's create branch — the ordinary "new book from a shelf scan" path.
func TestImporter_UpsertBookStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	author := asinTestAuthor(t, importer)

	item := sampleABSItem()
	item.ItemID = "li-asin-create"
	item.Title = "Artemis"
	item.ASIN = lowerASIN

	result, created, _, _, err := importer.upsertBook(context.Background(), asinTestConfig(), 0, author, item, true)
	if err != nil {
		t.Fatalf("upsertBook: %v", err)
	}
	if !created || result == nil {
		t.Fatalf("upsertBook created=%v result=%v, want a created book", created, result)
	}
	if result.row.ASIN != upperASIN {
		t.Errorf("created book ASIN = %q, want %q", result.row.ASIN, upperASIN)
	}
	stored, err := bookRepo.GetByID(context.Background(), result.row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.ASIN != upperASIN {
		t.Errorf("stored book ASIN = %v, want %q", stored, upperASIN)
	}
}

// upsertBook's dry-run branch builds the book it *would* create and shows it in
// the preview, so it has to agree with the real create above.
func TestImporter_UpsertBookDryRunPreviewsUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	author := asinTestAuthor(t, importer)

	item := sampleABSItem()
	item.ItemID = "li-asin-dry-run"
	item.Title = "Artemis"
	item.ASIN = lowerASIN

	cfg := asinTestConfig()
	cfg.DryRun = true
	result, created, _, _, err := importer.upsertBook(context.Background(), cfg, 0, author, item, true)
	if err != nil {
		t.Fatalf("upsertBook: %v", err)
	}
	if !created || result == nil {
		t.Fatalf("upsertBook created=%v result=%v, want a planned book", created, result)
	}
	if result.row.ASIN != upperASIN {
		t.Errorf("dry-run book ASIN = %q, want %q", result.row.ASIN, upperASIN)
	}
}

// upsertManualBook's create branch — the book a user resolved by hand in the
// review queue, which is written with MetadataProvider "openlibrary".
func TestImporter_UpsertManualBookStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	author := asinTestAuthor(t, importer)

	item := sampleABSItem()
	item.ItemID = "li-asin-manual"
	item.ASIN = lowerASIN
	item.ResolvedBookForeignID = "OL123W"
	item.ResolvedBookTitle = "Artemis"

	result, created, _, _, err := importer.upsertManualBook(context.Background(), asinTestConfig(), 0, author, item)
	if err != nil {
		t.Fatalf("upsertManualBook: %v", err)
	}
	if !created || result == nil {
		t.Fatalf("upsertManualBook created=%v result=%v, want a created book", created, result)
	}
	if result.row.ASIN != upperASIN {
		t.Errorf("manual book ASIN = %q, want %q", result.row.ASIN, upperASIN)
	}
	stored, err := bookRepo.GetByID(context.Background(), result.row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.ASIN != upperASIN {
		t.Errorf("stored manual book ASIN = %v, want %q", stored, upperASIN)
	}
}

// applyBookFields — the update path taken when the item is already linked to a
// local book by provenance.
func TestImporter_ApplyBookFieldsStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	author := asinTestAuthor(t, importer)

	book := &models.Book{
		ForeignID:        "abs:book:existing",
		AuthorID:         author.ID,
		Title:            "Artemis",
		SortTitle:        "Artemis",
		Status:           models.BookStatusWanted,
		MetadataProvider: providerAudiobookshelf,
	}
	if err := bookRepo.Create(context.Background(), book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-asin-apply"
	item.Title = "Artemis"
	item.ASIN = lowerASIN

	if err := importer.applyBookFields(context.Background(), book, author.ID, item); err != nil {
		t.Fatalf("applyBookFields: %v", err)
	}
	if book.ASIN != upperASIN {
		t.Errorf("updated book ASIN = %q, want %q", book.ASIN, upperASIN)
	}
	stored, err := bookRepo.GetByID(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.ASIN != upperASIN {
		t.Errorf("stored updated book ASIN = %v, want %q", stored, upperASIN)
	}
}

// applyABSFormatFields — the narrower field merge used when a manually
// resolved item re-links to a book that already exists.
func TestImporter_ApplyABSFormatFieldsStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)

	item := sampleABSItem()
	item.ASIN = lowerASIN

	book := &models.Book{Title: "Artemis"}
	importer.applyABSFormatFields(book, item)
	if book.ASIN != upperASIN {
		t.Errorf("merged book ASIN = %q, want %q", book.ASIN, upperASIN)
	}
}

// upsertEditions — the edition row, whose ASIN the Hardcover hydration path
// reads back and upper-cases on its own side before comparing.
func TestImporter_UpsertEditionsStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, editionRepo, _, _, _, _, _ := newABSImporterFixture(t)
	author := asinTestAuthor(t, importer)

	book := &models.Book{
		ForeignID:        "abs:book:editions",
		AuthorID:         author.ID,
		Title:            "Artemis",
		SortTitle:        "Artemis",
		Status:           models.BookStatusWanted,
		MetadataProvider: providerAudiobookshelf,
	}
	if err := bookRepo.Create(context.Background(), book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-asin-editions"
	item.ASIN = lowerASIN

	if _, err := importer.upsertEditions(context.Background(), asinTestConfig(), 0, book.ID, item); err != nil {
		t.Fatalf("upsertEditions: %v", err)
	}
	editions, err := editionRepo.ListByBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) == 0 {
		t.Fatal("no editions written")
	}
	for _, e := range editions {
		if e.ASIN == nil {
			t.Errorf("edition %q has no ASIN, want %q", e.Format, upperASIN)
			continue
		}
		if *e.ASIN != upperASIN {
			t.Errorf("edition %q ASIN = %q, want %q", e.Format, *e.ASIN, upperASIN)
		}
	}
}

// queueReviewItem — the review queue row. It is what the review screen shows
// and what ImportReview reads back, so a lowercase ASIN there is a lookup that
// silently finds nothing.
func TestImporter_QueueReviewItemStoresUpperCaseASIN(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, reviewRepo, _ := newABSImporterFixture(t)

	item := sampleABSItem()
	item.ItemID = "li-asin-review"
	item.ASIN = lowerASIN

	if err := importer.queueReviewItem(context.Background(), 0, asinTestConfig(), item, reviewReasonAmbiguousBook); err != nil {
		t.Fatalf("queueReviewItem: %v", err)
	}
	reviews, err := reviewRepo.ListByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(reviews))
	}
	if reviews[0].ASIN != upperASIN {
		t.Errorf("review item ASIN = %q, want %q", reviews[0].ASIN, upperASIN)
	}
}

// TestABSISBNKeepsTheXCheckDigit is the other identifier the ABS import used to
// mangle: it kept only the digits, so an ISBN-10 ending in 'X' lost its check
// digit, became nine characters, matched neither the 13 nor the 10 branch, and
// was dropped from the edition and from the upstream lookup entirely.
func TestABSISBNKeepsTheXCheckDigit(t *testing.T) {
	t.Parallel()

	if got := absLookupISBN("0-8044-2957-X"); got != "080442957X" {
		t.Errorf("absLookupISBN = %q, want %q", got, "080442957X")
	}
	if got := isbn10Ptr("0-8044-2957-X"); got == nil || *got != "080442957X" {
		t.Errorf("isbn10Ptr = %v, want %q", got, "080442957X")
	}
	if got := isbn13Ptr("978-0-441-17271-9"); got == nil || *got != "9780441172719" {
		t.Errorf("isbn13Ptr = %v, want %q", got, "9780441172719")
	}
}
