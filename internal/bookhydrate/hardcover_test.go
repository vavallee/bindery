package bookhydrate

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type fakeAudiobookEnricher struct {
	calls int
	err   error
}

func (f *fakeAudiobookEnricher) EnrichAudiobook(_ context.Context, book *models.Book) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	book.Narrator = "Kate Reading"
	return nil
}

func newHydrateBook(t *testing.T, foreignID, provider, mediaType string) (*db.BookRepo, *db.EditionRepo, *models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)
	author := &models.Author{ForeignID: "OL-HYDRATE-A", Name: "Author", SortName: "Author", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         author.ID,
		Title:            "Hydrated Book",
		SortTitle:        "Hydrated Book",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: provider,
		MediaType:        mediaType,
		Monitored:        true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return books, editions, book, ctx
}

func TestHydrateHardcoverEditionsAssignsBookAndPromotesAudioASIN(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	kindleASIN := "B111111111"
	audioASIN := "b222222222"
	enricher := &fakeAudiobookEnricher{}

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{
				{ForeignID: "hc:kindle", Title: "Kindle", ASIN: &kindleASIN, Format: "Kindle", IsEbook: true},
				{ForeignID: "hc:audio", Title: "Audio", ASIN: &audioASIN, Format: "Audiobook"},
			}, nil
		},
		Enricher: enricher,
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if result.Fetched != 2 || result.Upserted != 2 || !result.ASINPromoted || !result.AudiobookEnriched || !result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if book.ASIN != "B222222222" {
		t.Fatalf("promoted ASIN = %q", book.ASIN)
	}
	if enricher.calls != 1 {
		t.Fatalf("enricher calls = %d", enricher.calls)
	}

	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ASIN != "B222222222" || stored.Narrator != "Kate Reading" {
		t.Fatalf("book update not persisted: %+v", stored)
	}
	list, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("editions persisted = %d", len(list))
	}
	for _, edition := range list {
		if edition.BookID != book.ID {
			t.Fatalf("edition book id = %d, want %d", edition.BookID, book.ID)
		}
	}
}

func TestHydrateHardcoverEditionsDoesNotPromoteSkippedEditionASIN(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	other := &models.Book{
		ForeignID:        "hc:other-book",
		AuthorID:         book.AuthorID,
		Title:            "Other Book",
		SortTitle:        "Other Book",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "hardcover",
		MediaType:        models.MediaTypeAudiobook,
		Monitored:        true,
	}
	if err := books.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	skippedASIN := "B000SKIPP0"
	if ok, err := editions.UpsertMetadata(ctx, &models.Edition{
		ForeignID: "hc:shared-audio",
		BookID:    other.ID,
		Title:     "Other Audio",
		ASIN:      &skippedASIN,
		Format:    "Audiobook",
		Monitored: true,
	}); err != nil || !ok {
		t.Fatalf("seed edition ok=%v err=%v", ok, err)
	}
	enricher := &fakeAudiobookEnricher{}

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{{
				ForeignID: "hc:shared-audio",
				Title:     "Skipped Audio",
				ASIN:      &skippedASIN,
				Format:    "Audiobook",
				Monitored: true,
			}}, nil
		},
		Enricher: enricher,
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if result.Fetched != 1 || result.Upserted != 0 || result.ASINPromoted || result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if book.ASIN != "" {
		t.Fatalf("skipped edition ASIN was promoted: %q", book.ASIN)
	}
	if enricher.calls != 0 {
		t.Fatalf("enricher calls = %d, want 0", enricher.calls)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ASIN != "" {
		t.Fatalf("stored ASIN = %q, want empty", stored.ASIN)
	}
	list, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("skipped edition was linked to target book: %+v", list)
	}
}

func TestHydrateHardcoverEditionsDoesNotPromoteNonAudioASIN(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	kindleASIN := "B111111111"
	hardcoverASIN := "B333333333"
	enricher := &fakeAudiobookEnricher{}

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{
				{ForeignID: "hc:kindle", Title: "Kindle", ASIN: &kindleASIN, Format: "Kindle", IsEbook: true},
				{ForeignID: "hc:hardcover", Title: "Hardcover", ASIN: &hardcoverASIN, Format: "Hardcover", EditionInfo: "First edition"},
			}, nil
		},
		Enricher: enricher,
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if result.Fetched != 2 || result.Upserted != 2 || result.ASINPromoted || result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if book.ASIN != "" {
		t.Fatalf("non-audio ASIN was promoted: %q", book.ASIN)
	}
	if enricher.calls != 0 {
		t.Fatalf("enricher calls = %d, want 0", enricher.calls)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ASIN != "" {
		t.Fatalf("stored ASIN = %q, want empty", stored.ASIN)
	}
	list, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("editions persisted = %d, want 2", len(list))
	}
}

func TestHydrateHardcoverEditionsSkipsNonHardcoverBook(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "OL123W", "openlibrary", models.MediaTypeAudiobook)
	calls := 0
	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			calls++
			return nil, nil
		},
	})
	if result.Fetched != 0 || calls != 0 {
		t.Fatalf("non-Hardcover hydration should be no-op, result=%+v calls=%d", result, calls)
	}
}

func TestHydrateHardcoverEditionsReturnsFetchErrorAsBestEffort(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	fetchErr := errors.New("hardcover unavailable")
	result := HydrateHardcoverEditions(ctx, Options{
		Book:          book,
		Provider:      "hardcover",
		Editions:      editions,
		Books:         books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) { return nil, fetchErr },
	})
	if !errors.Is(result.Err, fetchErr) {
		t.Fatalf("result err = %v, want %v", result.Err, fetchErr)
	}
	if result.Upserted != 0 || book.ASIN != "" {
		t.Fatalf("unexpected mutation after fetch failure: result=%+v book=%+v", result, book)
	}
}
