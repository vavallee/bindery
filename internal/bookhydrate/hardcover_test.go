package bookhydrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type fakeAudiobookEnricher struct {
	calls        int
	err          error
	beforeEnrich func()
}

func (f *fakeAudiobookEnricher) EnrichAudiobook(_ context.Context, book *models.Book) error {
	f.calls++
	if f.beforeEnrich != nil {
		f.beforeEnrich()
	}
	if f.err != nil {
		return f.err
	}
	book.Narrator = "Kate Reading"
	return nil
}

func newHydrateBook(t *testing.T, foreignID, provider, mediaType string) (*db.BookRepo, *db.EditionRepo, *models.Book, context.Context) {
	t.Helper()
	books, editions, book, ctx, _ := newHydrateBookWithDB(t, foreignID, provider, mediaType)
	return books, editions, book, ctx
}

func newHydrateBookWithDB(t *testing.T, foreignID, provider, mediaType string) (*db.BookRepo, *db.EditionRepo, *models.Book, context.Context, *sql.DB) {
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
	return books, editions, book, ctx, database
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

// TestHydrateHardcoverEditionsDerivesAudiobookMetadataBeforeAudnex covers
// issue #806: book-level audiobook fields (language, cover) are derived from
// the chosen Hardcover audio edition first, and Audnex is left to fill only
// the gap it owns (narrator).
func TestHydrateHardcoverEditionsDerivesAudiobookMetadataBeforeAudnex(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	audioASIN := "b222222222"
	editionLang := "ger"
	editionCover := "https://img.example/hc-audio.jpg"
	enricher := &fakeAudiobookEnricher{}

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{{
				ForeignID: "hc:audio",
				Title:     "Audio",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Language:  editionLang,
				ImageURL:  editionCover,
				Monitored: true,
			}}, nil
		},
		Enricher: enricher,
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if !result.MetadataDerived {
		t.Fatalf("expected MetadataDerived, got result=%+v", result)
	}
	if !result.ASINPromoted || !result.AudiobookEnriched || !result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	// Hardcover edition supplied language + cover...
	if book.Language != editionLang {
		t.Fatalf("language not derived from Hardcover edition: %q", book.Language)
	}
	if book.ImageURL != editionCover {
		t.Fatalf("cover not derived from Hardcover edition: %q", book.ImageURL)
	}
	// ...and Audnex filled the gap it owns.
	if book.Narrator != "Kate Reading" {
		t.Fatalf("Audnex did not fill narrator gap: %q", book.Narrator)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Language != editionLang || stored.ImageURL != editionCover || stored.Narrator != "Kate Reading" {
		t.Fatalf("derived audiobook metadata not persisted: %+v", stored)
	}
}

func TestHydrateHardcoverEditionsDerivesSelectedAudioDuration(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mediaType       string
		pinned          bool
		bookDuration    int
		bookASIN        string
		editionDuration int
		unknownFormat   bool
		asinOnOther     bool
		wantDuration    int
		wantASIN        string
	}{
		{name: "missing duration", mediaType: models.MediaTypeAudiobook, editionDuration: 36000, wantDuration: 36000},
		{name: "runtime without audio format", mediaType: models.MediaTypeAudiobook, editionDuration: 36000, unknownFormat: true, wantDuration: 36000},
		{name: "whitespace ASIN", mediaType: models.MediaTypeAudiobook, bookASIN: "  ", editionDuration: 36000, wantDuration: 36000, wantASIN: "B000AUDIO1"},
		{name: "known duration", mediaType: models.MediaTypeAudiobook, bookDuration: 42000, editionDuration: 36000, wantDuration: 42000},
		{name: "missing edition duration", mediaType: models.MediaTypeAudiobook},
		{name: "invalid edition duration", mediaType: models.MediaTypeAudiobook, editionDuration: -1},
		{name: "ASIN chooses matching edition", mediaType: models.MediaTypeAudiobook, editionDuration: 36000, asinOnOther: true, wantDuration: 72000},
		{name: "known ASIN chooses matching edition", mediaType: models.MediaTypeAudiobook, bookASIN: "B000AUDIO1", editionDuration: 36000, asinOnOther: true, wantDuration: 72000},
		{name: "unmatched known ASIN", mediaType: models.MediaTypeAudiobook, bookASIN: "B000UNKNOWN", editionDuration: 36000},
		{name: "pinned ebook", mediaType: models.MediaTypeEbook, pinned: true, editionDuration: 36000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:duration-book", "hardcover", tc.mediaType)
			book.DurationSeconds = tc.bookDuration
			book.ASIN = tc.bookASIN
			if tc.bookDuration > 0 || tc.bookASIN != "" {
				if err := books.Update(ctx, book); err != nil {
					t.Fatal(err)
				}
			}
			audioASIN := "B000AUDIO1"
			selectedASIN := &audioASIN
			var otherASIN *string
			if tc.asinOnOther {
				selectedASIN, otherASIN = nil, &audioASIN
			}
			audioFormat := "Audiobook"
			if tc.unknownFormat {
				audioFormat = "Unknown"
			}
			result := HydrateHardcoverEditions(ctx, Options{
				Book:            book,
				Provider:        "hardcover",
				Editions:        editions,
				Books:           books,
				MediaTypePinned: tc.pinned,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{
						{ForeignID: "hc:print", Format: "Hardcover"},
						{ForeignID: "hc:audio", Format: audioFormat, ASIN: selectedASIN, DurationSeconds: tc.editionDuration},
						{ForeignID: "hc:other-audio", Format: "Audiobook", ASIN: otherASIN, DurationSeconds: 72000},
					}, nil
				},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.DurationSeconds != tc.wantDuration {
				t.Errorf("stored DurationSeconds = %d, want %d; result=%+v", stored.DurationSeconds, tc.wantDuration, result)
			}
			if tc.wantASIN != "" && stored.ASIN != tc.wantASIN {
				t.Errorf("stored ASIN = %q, want %q", stored.ASIN, tc.wantASIN)
			}
		})
	}
}

func TestHydrateHardcoverEditionsRanksAudioEvidenceBeforeRuntime(t *testing.T) {
	for _, matchingASIN := range []bool{false, true} {
		for _, runtimeFormat := range []string{"Unknown", "Audiobook"} {
			for _, reverse := range []bool{false, true} {
				name := fmt.Sprintf("matchingASIN=%v/format=%s/reverse=%v", matchingASIN, runtimeFormat, reverse)
				t.Run(name, func(t *testing.T) {
					books, editions, book, ctx := newHydrateBook(t, "hc:duration-book", "hardcover", models.MediaTypeAudiobook)
					var asin *string
					if matchingASIN {
						book.ASIN = "B000AUDIO1"
						asin = &book.ASIN
						if err := books.Update(ctx, book); err != nil {
							t.Fatal(err)
						}
					}
					fetched := []models.Edition{
						{ForeignID: "hc:labeled", Format: "Audiobook", ASIN: asin, Language: "eng", ImageURL: "good"},
						{ForeignID: "hc:runtime", Format: runtimeFormat, ASIN: asin, Language: "ger", ImageURL: "other", DurationSeconds: 36000},
					}
					if reverse {
						fetched[0], fetched[1] = fetched[1], fetched[0]
					}
					result := HydrateHardcoverEditions(ctx, Options{
						Book: book, Provider: "hardcover", Editions: editions, Books: books,
						FetchEditions: func(context.Context, string) ([]models.Edition, error) { return fetched, nil },
					})
					if result.Err != nil {
						t.Fatal(result.Err)
					}
					stored, err := books.GetByID(ctx, book.ID)
					if err != nil {
						t.Fatal(err)
					}
					wantLanguage, wantCover, wantDuration := "eng", "good", 0
					if runtimeFormat == "Audiobook" {
						wantLanguage, wantCover, wantDuration = "ger", "other", 36000
					}
					if stored.Language != wantLanguage || stored.ImageURL != wantCover || stored.DurationSeconds != wantDuration || !result.BookUpdated {
						t.Fatalf("stored language=%q cover=%q duration=%d; want %q %q %d; result=%+v", stored.Language, stored.ImageURL, stored.DurationSeconds, wantLanguage, wantCover, wantDuration, result)
					}
				})
			}
		}
	}
}

func TestHydrateHardcoverEditionsRuntimeDoesNotOverrideKnownFormat(t *testing.T) {
	for _, tc := range []struct{ format, storedFormat string }{
		{format: "Hardcover"}, {format: "Paperback"}, {format: "Mass Market Paperback"},
		{format: "Kindle"}, {format: "Unknown"}, {format: ""}, {format: "Audiobook"},
		{format: "Hardcover", storedFormat: "Unknown"}, {format: "Paperback", storedFormat: "Unknown"},
	} {
		t.Run(tc.format+"/stored="+tc.storedFormat, func(t *testing.T) {
			format := tc.format
			books, editions, book, ctx := newHydrateBook(t, "hc:format-book", "hardcover", "")
			asin := "B00FORMAT1"
			if tc.storedFormat != "" {
				if ok, err := editions.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:edition", BookID: book.ID, Format: tc.storedFormat}, book.ForeignID, book.MetadataProvider); err != nil || !ok {
					t.Fatalf("seed edition: ok=%v err=%v", ok, err)
				}
			}
			isEbook := format == "Kindle"
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{{ForeignID: "hc:edition", Format: format, IsEbook: isEbook, ASIN: &asin, Language: "por", DurationSeconds: 36000}}, nil
				},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantAudio := format == "Unknown" || format == "" || format == "Audiobook"
			if wantAudio {
				if stored.MediaType != models.MediaTypeAudiobook || stored.ASIN != asin || stored.DurationSeconds != 36000 || !result.ASINPromoted {
					t.Fatalf("audio metadata not persisted: book=%+v result=%+v", stored, result)
				}
			} else if stored.MediaType != models.MediaTypeEbook || stored.ASIN != "" || stored.Language != "" || stored.DurationSeconds != 0 || result.BookUpdated || result.ASINPromoted {
				t.Fatalf("non-audio edition promoted: book=%+v result=%+v", stored, result)
			}
		})
	}
}

func TestHydrateHardcoverEditionsPersistsDurationWithMetadataAndLanguageLock(t *testing.T) {
	for _, tc := range []struct {
		name, language, cover string
		locked                bool
	}{
		{name: "language", language: "ger"},
		{name: "cover", cover: "https://example.com/cover.jpg"},
		{name: "locked language duration only", language: "ger", locked: true},
		{name: "locked language with cover", language: "ger", cover: "https://example.com/cover.jpg", locked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:metadata-duration", "hardcover", models.MediaTypeAudiobook)
			book.ASIN = "B000AUDIO1"
			if tc.locked {
				book.LockField(models.BookFieldLanguage)
			}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			asin := book.ASIN
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, Language: tc.language, ImageURL: tc.cover, DurationSeconds: 36000}}, nil
				},
			})
			if result.Err != nil || !result.BookUpdated || !result.MetadataDerived || result.ASINPromoted {
				t.Fatalf("unexpected hydration result: %+v", result)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantLanguage := tc.language
			if tc.locked {
				wantLanguage = ""
			}
			for _, got := range []*models.Book{book, stored} {
				if got.Language != wantLanguage || got.ImageURL != tc.cover || got.DurationSeconds != 36000 || got.IsFieldLocked(models.BookFieldLanguage) != tc.locked {
					t.Fatalf("duration/metadata/lock not preserved: %+v", got)
				}
			}
		})
	}
}

func TestHydrateHardcoverEditionsDurationOnlyPreservesConcurrentChanges(t *testing.T) {
	for _, replaceASIN := range []bool{false, true} {
		name := "unrelated changes"
		if replaceASIN {
			name = "changed ASIN"
		}
		t.Run(name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:duration-book", "hardcover", models.MediaTypeAudiobook)
			book.ASIN = "B000AUDIO1"
			book.SeriesRefs = []models.SeriesRef{{ForeignID: "hc:series", Title: "Provider series"}}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(ctx context.Context, _ string) ([]models.Edition, error) {
					current, err := books.GetByID(ctx, book.ID)
					if err != nil {
						return nil, err
					}
					current.Monitored = false
					current.Status = models.BookStatusSkipped
					current.Narrator = "Concurrent narrator"
					if replaceASIN {
						current.ASIN = "B000OTHER1"
						current.DurationSeconds = 42000
					}
					if err := books.Update(ctx, current); err != nil {
						return nil, err
					}
					asin := "B000AUDIO1"
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, DurationSeconds: 36000}}, nil
				},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if result.BookUpdated == replaceASIN {
				t.Fatalf("BookUpdated=%v with replaceASIN=%v", result.BookUpdated, replaceASIN)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantDuration := 36000
			wantASIN := "B000AUDIO1"
			if replaceASIN {
				wantDuration = 42000
				wantASIN = "B000OTHER1"
			}
			if book.DurationSeconds != wantDuration {
				t.Fatalf("in-memory duration = %d, want %d after persistence", book.DurationSeconds, wantDuration)
			}
			if book.Monitored || book.Status != models.BookStatusSkipped || book.Narrator != "Concurrent narrator" || book.ASIN != wantASIN {
				t.Fatalf("caller retains stale book state: %+v", book)
			}
			if len(book.SeriesRefs) != 1 || book.SeriesRefs[0].ForeignID != "hc:series" {
				t.Fatalf("provider series refs lost: %+v", book.SeriesRefs)
			}
			if stored.Monitored || stored.Status != models.BookStatusSkipped || stored.Narrator != "Concurrent narrator" ||
				stored.ASIN != wantASIN || stored.DurationSeconds != wantDuration {
				t.Fatalf("concurrent book changes were lost or stale duration persisted: %+v", stored)
			}
		})
	}
}

func TestHydrateHardcoverEditionsAdoptsConcurrentDuration(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:duration-book", "hardcover", models.MediaTypeAudiobook)
	book.ASIN = "B000AUDIO1"
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	asin := book.ASIN
	result := HydrateHardcoverEditions(ctx, Options{
		Book: book, Provider: "hardcover", Editions: editions, Books: books,
		FetchEditions: func(ctx context.Context, _ string) ([]models.Edition, error) {
			concurrentBook, err := books.GetByID(ctx, book.ID)
			if err != nil {
				return nil, err
			}
			concurrent := HydrateHardcoverEditions(ctx, Options{
				Book: concurrentBook, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, DurationSeconds: 42000}}, nil
				},
			})
			if concurrent.Err != nil || !concurrent.BookUpdated {
				t.Fatalf("concurrent hydration failed: %+v", concurrent)
			}
			return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, DurationSeconds: 36000}}, nil
		},
	})
	if result.Err != nil || result.BookUpdated {
		t.Fatalf("stale hydration should lose the guarded write: %+v", result)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if book.DurationSeconds != 42000 || stored.DurationSeconds != 42000 {
		t.Fatalf("in-memory duration=%d stored duration=%d, want concurrent runtime 42000", book.DurationSeconds, stored.DurationSeconds)
	}
}

// TestHydrateHardcoverEditionsDoesNotClobberKnownAudiobookFields guards the
// "unknown ⇒ don't overwrite known" invariant: Hardcover edition data must not
// replace language/cover the book already carries.
func TestHydrateHardcoverEditionsDoesNotClobberKnownAudiobookFields(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	book.Language = "eng"
	book.ImageURL = "https://existing.example/cover.jpg"
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	audioASIN := "b222222222"

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{{
				ForeignID: "hc:audio",
				Title:     "Audio",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Language:  "ger",
				ImageURL:  "https://img.example/hc-audio.jpg",
				Monitored: true,
			}}, nil
		},
		Enricher: &fakeAudiobookEnricher{},
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if book.Language != "eng" {
		t.Fatalf("known language was clobbered: %q", book.Language)
	}
	if book.ImageURL != "https://existing.example/cover.jpg" {
		t.Fatalf("known cover was clobbered: %q", book.ImageURL)
	}
}

// TestHydrateHardcoverEditionsDerivesMetadataWithoutASINPromotion covers the
// case where the book already has an ASIN (so no promotion happens) but the
// Hardcover edition still has language/cover to contribute — the derivation
// must run and persist on its own (#806).
func TestHydrateHardcoverEditionsDerivesMetadataWithoutASINPromotion(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	book.ASIN = "B000PREEXIST"
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	editionASIN := "b222222222"

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{{
				ForeignID: "hc:audio",
				Title:     "Audio",
				ASIN:      &editionASIN,
				Format:    "Audiobook",
				Language:  "ger",
				ImageURL:  "https://img.example/hc-audio.jpg",
				Monitored: true,
			}}, nil
		},
		Enricher: &fakeAudiobookEnricher{},
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if result.ASINPromoted {
		t.Fatalf("ASIN should not be promoted over an existing one: %+v", result)
	}
	if !result.MetadataDerived || !result.BookUpdated {
		t.Fatalf("derivation should run and persist without ASIN promotion: %+v", result)
	}
	if book.ASIN != "B000PREEXIST" {
		t.Fatalf("existing ASIN changed: %q", book.ASIN)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Language != "ger" || stored.ImageURL != "https://img.example/hc-audio.jpg" {
		t.Fatalf("derived metadata not persisted without ASIN promotion: %+v", stored)
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
	}, other.ForeignID, other.MetadataProvider); err != nil || !ok {
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

func TestHydrateHardcoverEditionsPromotesStoredEditionASIN(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	storedASIN := "B111STORED"
	if ok, err := editions.UpsertMetadata(ctx, &models.Edition{
		ForeignID: "hc:audio",
		BookID:    book.ID,
		Title:     "Stored Audio",
		ASIN:      &storedASIN,
		Format:    "Audiobook",
		Monitored: true,
	}, book.ForeignID, book.MetadataProvider); err != nil || !ok {
		t.Fatalf("seed edition ok=%v err=%v", ok, err)
	}
	replacementASIN := "B999FETCHD"

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return []models.Edition{{
				ForeignID:       "hc:audio",
				Title:           "Fetched Audio",
				ASIN:            &replacementASIN,
				Format:          "Audiobook",
				DurationSeconds: 36000,
				Monitored:       true,
			}}, nil
		},
		Enricher: &fakeAudiobookEnricher{},
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if !result.ASINPromoted || !result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if book.ASIN != storedASIN {
		t.Fatalf("promoted ASIN = %q, want stored %q", book.ASIN, storedASIN)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ASIN != storedASIN {
		t.Fatalf("stored book ASIN = %q, want %q", stored.ASIN, storedASIN)
	}
	if stored.DurationSeconds != 0 {
		t.Fatalf("stored book duration = %d from mismatched fetched ASIN, want 0", stored.DurationSeconds)
	}
	edition, err := editions.GetByForeignID(ctx, "hc:audio")
	if err != nil {
		t.Fatal(err)
	}
	if edition.ASIN == nil || *edition.ASIN != storedASIN {
		t.Fatalf("edition ASIN was overwritten: %+v", edition)
	}
}

func TestHydrateHardcoverEditionsUsesRuntimeMatchingRetainedASIN(t *testing.T) {
	for _, tc := range []struct {
		name         string
		storedASIN   string
		fetchedASIN  string
		siblingASIN  string
		legacySeed   bool
		wantASIN     string
		wantDuration int
	}{
		{name: "valid sibling after ASIN conflict", storedASIN: "B000RETAIN", fetchedASIN: "B000FETCHD", siblingASIN: "B000RETAIN", wantASIN: "B000RETAIN", wantDuration: 72000},
		{name: "legacy whitespace ASIN replaced by upsert", storedASIN: "  ", fetchedASIN: "B000FETCHD", legacySeed: true, wantASIN: "B000FETCHD", wantDuration: 36000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:duration-book", "hardcover", models.MediaTypeAudiobook)
			seed := &models.Edition{
				ForeignID: "hc:audio", BookID: book.ID, Title: "Audio", Format: "Audiobook", ASIN: &tc.storedASIN,
			}
			if tc.legacySeed {
				// Upsert stores the ASIN verbatim, leaving the whitespace-only row
				// that UpsertMetadata normalizes; hydration must not see it.
				if err := editions.Upsert(ctx, seed); err != nil {
					t.Fatalf("seed legacy edition: %v", err)
				}
				stored, err := editions.GetByForeignID(ctx, "hc:audio")
				if err != nil || stored == nil || stored.ASIN == nil || *stored.ASIN != tc.storedASIN {
					t.Fatalf("legacy seed ASIN = %+v err=%v, want %q", stored, err, tc.storedASIN)
				}
			} else if ok, err := editions.UpsertMetadata(ctx, seed, book.ForeignID, book.MetadataProvider); err != nil || !ok {
				t.Fatalf("seed edition ok=%v err=%v", ok, err)
			}
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					fetched := []models.Edition{{
						ForeignID: "hc:audio", Title: "Audio", Format: "Audiobook", ASIN: &tc.fetchedASIN, DurationSeconds: 36000,
					}}
					if tc.siblingASIN != "" {
						fetched = append(fetched, models.Edition{
							ForeignID: "hc:sibling", Title: "Sibling Audio", Format: "Audiobook", ASIN: &tc.siblingASIN, DurationSeconds: 72000,
						})
					}
					return fetched, nil
				},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ASIN != tc.wantASIN || stored.DurationSeconds != tc.wantDuration {
				t.Fatalf("stored ASIN=%q duration=%d, want ASIN=%q duration=%d; result=%+v", stored.ASIN, stored.DurationSeconds, tc.wantASIN, tc.wantDuration, result)
			}
		})
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

func TestHydrateHardcoverEditionsDoesNotMutateFetchedEditions(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:hydrated-book", "hardcover", models.MediaTypeAudiobook)
	audioASIN := "B111111111"
	fetched := []models.Edition{{
		ForeignID: "hc:audio",
		ASIN:      &audioASIN,
		Format:    "Audiobook",
		Monitored: true,
	}}

	result := HydrateHardcoverEditions(ctx, Options{
		Book:     book,
		Provider: "hardcover",
		Editions: editions,
		Books:    books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			return fetched, nil
		},
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if result.Upserted != 1 || !result.ASINPromoted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if fetched[0].BookID != 0 {
		t.Fatalf("fetched edition BookID mutated to %d", fetched[0].BookID)
	}
	if fetched[0].Title != "" {
		t.Fatalf("fetched edition Title mutated to %q", fetched[0].Title)
	}
}

func TestHydrateHardcoverEditionsUsesProviderForeignID(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "OL123W", "openlibrary", models.MediaTypeAudiobook)
	audioASIN := "B222222222"
	var gotForeignID string

	result := HydrateHardcoverEditions(ctx, Options{
		Book:              book,
		Provider:          "openlibrary",
		ProviderForeignID: "hc:matched-book",
		Editions:          editions,
		Books:             books,
		FetchEditions: func(_ context.Context, foreignID string) ([]models.Edition, error) {
			gotForeignID = foreignID
			return []models.Edition{{
				ForeignID: "hc:matched-audio",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}}, nil
		},
	})
	if result.Err != nil {
		t.Fatalf("hydrate err = %v", result.Err)
	}
	if gotForeignID != "hc:matched-book" {
		t.Fatalf("fetch foreign ID = %q, want hc:matched-book", gotForeignID)
	}
	if !result.ASINPromoted || !result.BookUpdated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if book.ForeignID != "OL123W" || book.ASIN != audioASIN {
		t.Fatalf("book identity or ASIN changed unexpectedly: %+v", book)
	}
	list, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ForeignID != "hc:matched-audio" {
		t.Fatalf("expected matched edition, got %+v", list)
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

// TestHydrateHardcoverEditionsRespectsMediaTypePin guards the #1732 boundary
// against the inventory-driven widening added to refreshBookStatus: that
// widening fires when a second format is already imported, this one fires on
// metadata alone ("the work has an audio edition somewhere", true of most
// popular titles). The two must stay independent — a pinned ebook book must
// not become 'both' just because Hardcover lists an audiobook edition.
func TestHydrateHardcoverEditionsRespectsMediaTypePin(t *testing.T) {
	for _, pinned := range []bool{true, false} {
		name := "unpinned widens"
		if pinned {
			name = "pinned stays ebook"
		}
		t.Run(name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:pinned-book", "hardcover", models.MediaTypeEbook)
			audioASIN := "b333333333"

			result := HydrateHardcoverEditions(ctx, Options{
				Book:     book,
				Provider: "hardcover",
				Editions: editions,
				Books:    books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{{
						ForeignID: "hc:audio-pin",
						Title:     "Audio",
						ASIN:      &audioASIN,
						Format:    "Audiobook",
						Monitored: true,
					}}, nil
				},
				MediaTypePinned: pinned,
			})
			if result.Err != nil {
				t.Fatalf("hydrate err = %v", result.Err)
			}

			want := models.MediaTypeBoth
			if pinned {
				want = models.MediaTypeEbook
			}
			if book.MediaType != want {
				t.Errorf("MediaType = %q, want %q (pinned=%v)", book.MediaType, want, pinned)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.MediaType != want {
				t.Errorf("persisted MediaType = %q, want %q (pinned=%v)", stored.MediaType, want, pinned)
			}
		})
	}
}

// A row last written as CURRENT_TIMESTAMP (migrations 082/084) or in the #914
// time.String shape must still accept hydration from an unmodified snapshot,
// e.g. the author-refresh title-dedup arm that hydrates a listed book (#2758).
func TestHydrateHardcoverEditionsPersistsOverLegacyUpdatedAt(t *testing.T) {
	for _, stored := range []string{"2026-09-01 10:00:00", "2026-09-01 10:00:00.123456789 +0000 UTC"} {
		t.Run(stored, func(t *testing.T) {
			books, editions, created, ctx, database := newHydrateBookWithDB(t, "hc:legacy-updated-at", "hardcover", models.MediaTypeAudiobook)
			if _, err := database.ExecContext(ctx, "UPDATE books SET updated_at=? WHERE id=?", stored, created.ID); err != nil {
				t.Fatal(err)
			}
			book, err := books.GetByID(ctx, created.ID)
			if err != nil || book == nil {
				t.Fatalf("load legacy book: %v", err)
			}
			asin := "B000LEGACY1"
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books, Enricher: &fakeAudiobookEnricher{},
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, Language: "ger", ImageURL: "https://img.example/legacy.jpg"}}, nil
				},
			})
			if result.Err != nil || !result.BookUpdated || !result.ASINPromoted || !result.MetadataDerived {
				t.Fatalf("hydration over legacy updated_at was discarded: %+v", result)
			}
			persisted, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.ASIN != asin || persisted.Language != "ger" || persisted.ImageURL != "https://img.example/legacy.jpg" || persisted.Narrator != "Kate Reading" {
				t.Fatalf("hydrated metadata not persisted: %+v", persisted)
			}
		})
	}
}

func TestHydrateHardcoverEditionsPreservesConcurrentMetadataEdits(t *testing.T) {
	for _, duringEnrichment := range []bool{false, true} {
		t.Run(fmt.Sprintf("duringEnrichment=%v", duringEnrichment), func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:concurrent-metadata", "hardcover", models.MediaTypeAudiobook)
			book.SeriesRefs = []models.SeriesRef{{ForeignID: "hc:series", Title: "Provider series"}}
			asin := "B000AUDIO1"
			if !duringEnrichment {
				book.ASIN = asin
			}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			editBook := func() {
				current, err := books.GetByID(ctx, book.ID)
				if err != nil {
					t.Fatal(err)
				}
				current.Language = ""
				current.LockField(models.BookFieldLanguage)
				current.Title = "User's title"
				current.Monitored = false
				current.Status = models.BookStatusSkipped
				current.ImageURL = "https://example.com/user-cover.jpg"
				current.DurationSeconds = 42000
				if err := books.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			enricher := &fakeAudiobookEnricher{}
			if duringEnrichment {
				enricher.beforeEnrich = editBook
			}
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books, Enricher: enricher,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					if !duringEnrichment {
						editBook()
					}
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, Language: "ger", ImageURL: "https://example.com/provider-cover.jpg", DurationSeconds: 36000}}, nil
				},
			})
			if result.Err != nil || result.BookUpdated || result.MetadataDerived || result.ASINPromoted || result.AudiobookEnriched {
				t.Fatalf("stale metadata should be discarded: %+v", result)
			}
			if duringEnrichment && enricher.calls != 1 {
				t.Fatalf("enricher calls=%d, want 1", enricher.calls)
			}
			if len(book.SeriesRefs) != 1 || book.SeriesRefs[0].ForeignID != "hc:series" {
				t.Fatalf("provider series refs lost after conflict: %+v", book.SeriesRefs)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantASIN := asin
			if duringEnrichment {
				wantASIN = ""
			}
			for _, got := range []*models.Book{book, stored} {
				if got.Language != "" || !got.IsFieldLocked(models.BookFieldLanguage) || got.Title != "User's title" || got.Monitored || got.Status != models.BookStatusSkipped || got.ImageURL != "https://example.com/user-cover.jpg" || got.DurationSeconds != 42000 || got.ASIN != wantASIN || got.Narrator != "" {
					t.Fatalf("concurrent edit was lost or caller retains discarded metadata: %+v", got)
				}
			}
		})
	}
}

func TestHydrateHardcoverEditionsPreservesConcurrentEmbeddedLanguage(t *testing.T) {
	books, editions, book, ctx := newHydrateBook(t, "hc:scanner-language", "hardcover", models.MediaTypeBoth)
	asin := "B000AUDIO1"
	book.ASIN = asin
	if err := books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	result := HydrateHardcoverEditions(ctx, Options{
		Book: book, Provider: "hardcover", Editions: editions, Books: books,
		FetchEditions: func(context.Context, string) ([]models.Edition, error) {
			if err := books.SetLanguage(ctx, book.ID, "spa"); err != nil {
				return nil, err
			}
			return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, Language: "ger", DurationSeconds: 36000}}, nil
		},
	})
	if result.Err != nil || result.BookUpdated || result.MetadataDerived {
		t.Fatalf("stale metadata should be discarded: %+v", result)
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Language != "spa" || book.Language != "spa" {
		t.Fatalf("embedded language overwritten: stored=%q caller=%q", stored.Language, book.Language)
	}
}

func TestHydrateHardcoverEditionsDiscardsProviderContextAfterRebind(t *testing.T) {
	for _, durationOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("durationOnly=%v", durationOnly), func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:original", "hardcover", models.MediaTypeAudiobook)
			asin := "B000AUDIO1"
			book.ASIN = asin
			book.SeriesRefs = []models.SeriesRef{{ForeignID: "hc:old-series", Title: "Old series"}}
			book.ProviderISBNs = []string{"9781234567890"}
			book.CreditedAuthorForeignIDs = []string{"hc:old-author"}
			book.HardcoverForeignID = book.ForeignID
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					current, err := books.GetByID(ctx, book.ID)
					if err != nil {
						return nil, err
					}
					current.ForeignID = "hc:rebound"
					if err := books.Update(ctx, current); err != nil {
						return nil, err
					}
					language := "ger"
					if durationOnly {
						language = ""
					}
					return []models.Edition{{ForeignID: "hc:audio", Format: "Audiobook", ASIN: &asin, Language: language, DurationSeconds: 36000}}, nil
				},
			})
			if result.Err != nil || result.BookUpdated || result.MetadataDerived {
				t.Fatalf("rebound metadata should be discarded: %+v", result)
			}
			attached, err := editions.ListByBook(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(attached) != 0 || result.Upserted != 0 {
				t.Fatalf("stale editions persisted after rebind: upserted=%d editions=%+v", result.Upserted, attached)
			}
			if book.ForeignID != "hc:rebound" || len(book.SeriesRefs) != 0 || len(book.ProviderISBNs) != 0 || len(book.CreditedAuthorForeignIDs) != 0 || book.HardcoverForeignID != "" {
				t.Fatalf("rebound book retains old provider context: %+v", book)
			}
		})
	}
}

func TestHydrateHardcoverEditionsRefreshesCallerWithoutDerivedChanges(t *testing.T) {
	for _, outcome := range []string{"empty", "unchanged editions", "fetch error"} {
		t.Run(outcome, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:original-noop", "hardcover", models.MediaTypeAudiobook)
			asin := "B000AUDIO1"
			book.ASIN, book.Language, book.ImageURL, book.DurationSeconds = asin, "eng", "https://example.com/cover.jpg", 36000
			book.SeriesRefs = []models.SeriesRef{{ForeignID: "hc:series", Title: "Provider series"}}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			fetchErr := errors.New("provider unavailable")
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					current, err := books.GetByID(ctx, book.ID)
					if err != nil {
						return nil, err
					}
					current.Monitored = false
					current.Status = models.BookStatusSkipped
					if outcome != "unchanged editions" {
						current.ForeignID = "hc:rebound-noop"
					}
					if err := books.Update(ctx, current); err != nil {
						return nil, err
					}
					switch outcome {
					case "empty":
						return nil, nil
					case "fetch error":
						return nil, fetchErr
					default:
						return []models.Edition{{ForeignID: "hc:audio-noop", Format: "Audiobook", ASIN: &asin, Language: "eng", ImageURL: "https://example.com/cover.jpg", DurationSeconds: 36000}}, nil
					}
				},
			})
			if outcome == "fetch error" {
				if !errors.Is(result.Err, fetchErr) {
					t.Fatalf("expected fetch error: %+v", result)
				}
			} else if result.Err != nil {
				t.Fatal(result.Err)
			}
			if result.BookUpdated || result.MetadataDerived || result.ASINPromoted || result.AudiobookEnriched {
				t.Fatalf("refresh must not report metadata changes: %+v", result)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if book.ForeignID != stored.ForeignID || book.Monitored != stored.Monitored || book.Status != stored.Status {
				t.Fatalf("caller stale: foreignID=%q monitored=%v status=%q; stored=%q/%v/%q", book.ForeignID, book.Monitored, book.Status, stored.ForeignID, stored.Monitored, stored.Status)
			}
			wantRefs := 0
			if outcome == "unchanged editions" {
				wantRefs = 1
			}
			if len(book.SeriesRefs) != wantRefs {
				t.Fatalf("provider refs=%+v, want %d for %s", book.SeriesRefs, wantRefs, outcome)
			}
		})
	}
}

func TestHydrateHardcoverEditionsNoMetadataReloadFailure(t *testing.T) {
	for _, fetchFailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fetchFailed=%v", fetchFailed), func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:deleted", "hardcover", models.MediaTypeAudiobook)
			fetchErr := errors.New("provider unavailable")
			result := HydrateHardcoverEditions(ctx, Options{
				Book: book, Provider: "hardcover", Editions: editions, Books: books,
				FetchEditions: func(context.Context, string) ([]models.Edition, error) {
					if err := books.Delete(ctx, book.ID); err != nil {
						t.Fatal(err)
					}
					if fetchFailed {
						return nil, fetchErr
					}
					return nil, nil
				},
			})
			if result.Err == nil || (fetchFailed && !errors.Is(result.Err, fetchErr)) {
				t.Fatalf("reload failure must surface without replacing fetch error: %+v", result)
			}
			if result.BookUpdated {
				t.Fatal("reload must not report a book write")
			}
		})
	}
}
