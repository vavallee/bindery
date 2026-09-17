package abs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// isbnBindingStubProvider is bindingStubProvider with an ISBN lookup that can
// answer a record or fail, so the aggregator's ISBN fan out can be steered.
type isbnBindingStubProvider struct {
	bindingStubProvider
	byISBN  map[string]*models.Book
	isbnErr error
}

func (p *isbnBindingStubProvider) GetBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	if p.isbnErr != nil {
		return nil, p.isbnErr
	}
	return p.byISBN[isbn], nil
}

const bindingTestISBN = "9780593135204"

// runISBNBindingImport imports one ABS item twice: first with no metadata so
// the book row exists on its Audiobookshelf identity, then with meta, carrying
// a changed narrator so the second run's ABS field update is observable.
func runISBNBindingImport(t *testing.T, meta *metadata.Aggregator) (*Importer, *db.BookRepo, *ImportStats) {
	t.Helper()
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)

	item := sampleABSItem()
	item.ISBN = bindingTestISBN
	item.ASIN = ""
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}
	run := func(it NormalizedLibraryItem) *ImportStats {
		importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
			if err := fn(ctx, it); err != nil {
				return EnumerationStats{}, err
			}
			return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
		}
		stats, err := importer.Run(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return stats
	}

	run(item)
	importer.WithMetadata(meta)
	item.Narrators = []string{"A Different Narrator"}
	return importer, bookRepo, run(item)
}

func onlyBook(t *testing.T, bookRepo *db.BookRepo) models.Book {
	t.Helper()
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	return books[0]
}

// TestABSImportDoesNotRelinkBookToFallbackISBNRecordWhilePrimaryDown is #2642:
// OpenLibrary times out, DNB answers the ISBN, and the book used to be relinked
// to DNB's id and provider permanently.
func TestABSImportDoesNotRelinkBookToFallbackISBNRecordWhilePrimaryDown(t *testing.T) {
	t.Parallel()

	ol := &isbnBindingStubProvider{
		bindingStubProvider: bindingStubProvider{name: "openlibrary"},
		isbnErr:             errors.New("context deadline exceeded"),
	}
	dnb := &isbnBindingStubProvider{
		bindingStubProvider: bindingStubProvider{name: "dnb"},
		byISBN: map[string]*models.Book{
			bindingTestISBN: {ForeignID: "dnb:1234567890", Title: "Project Hail Mary", MetadataProvider: "dnb"},
		},
	}
	importer, bookRepo, stats := runISBNBindingImport(t, metadata.NewAggregator(ol, dnb))

	book := onlyBook(t, bookRepo)
	if !strings.HasPrefix(book.ForeignID, "abs:") {
		t.Errorf("book ForeignID = %q, want it left on its abs: identity", book.ForeignID)
	}
	if book.MetadataProvider != providerAudiobookshelf {
		t.Errorf("book MetadataProvider = %q, want %q", book.MetadataProvider, providerAudiobookshelf)
	}
	if stats.MetadataRelinked != 0 {
		t.Errorf("metadataRelinked = %d, want 0", stats.MetadataRelinked)
	}
	if book.Narrator != "A Different Narrator" {
		t.Errorf("narrator = %q, want the ABS update applied regardless", book.Narrator)
	}
	if stats.Failed != 0 {
		t.Errorf("failed = %d, want 0: a skipped relink is not an import failure", stats.Failed)
	}

	progress := importer.Progress()
	if len(progress.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(progress.Results))
	}
	msg := progress.Results[0].Message
	if !strings.Contains(msg, "book relink skipped") || !strings.Contains(msg, "openlibrary did not answer") {
		t.Errorf("message = %q, want a book relink skipped message naming the primary", msg)
	}
}

// TestABSImportRelinksBookToFallbackISBNRecordWhenPrimaryMisses is the #2237
// half: the primary answered and has no such ISBN, so the fallback's record is
// a fact rather than a walkover and the relink goes ahead.
func TestABSImportRelinksBookToFallbackISBNRecordWhenPrimaryMisses(t *testing.T) {
	t.Parallel()

	ol := &isbnBindingStubProvider{bindingStubProvider: bindingStubProvider{name: "openlibrary"}}
	dnb := &isbnBindingStubProvider{
		bindingStubProvider: bindingStubProvider{name: "dnb"},
		byISBN: map[string]*models.Book{
			bindingTestISBN: {ForeignID: "dnb:1234567890", Title: "Project Hail Mary", MetadataProvider: "dnb"},
		},
	}
	_, bookRepo, stats := runISBNBindingImport(t, metadata.NewAggregator(ol, dnb))

	book := onlyBook(t, bookRepo)
	if book.ForeignID != "dnb:1234567890" || book.MetadataProvider != "dnb" {
		t.Errorf("book = %q/%q, want relinked to dnb:1234567890/dnb", book.ForeignID, book.MetadataProvider)
	}
	if stats.MetadataRelinked == 0 {
		t.Error("metadataRelinked = 0, want the relink counted")
	}
	if book.Narrator != "A Different Narrator" {
		t.Errorf("narrator = %q, want the ABS update applied", book.Narrator)
	}
}
