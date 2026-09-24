package bookhydrate

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// audnexStub stands in for api.audible.com's audnex mirror.
type audnexStub struct {
	book  *audnex.Book
	calls int
}

func (s *audnexStub) GetBook(_ context.Context, _ string) (*audnex.Book, error) {
	s.calls++
	return s.book, nil
}

// The headline case of #2767, and the reason it is worth an integration test
// rather than a unit one: the write lives in metadata.Aggregator.EnrichAudiobook
// but the damage is done here, because HydrateHardcoverEditions hands it the
// stored book and then persists it through Books.Update. #2760 fixed the same
// bug on Language in this function; description reached the database by the
// very same call path, one field over.
//
// Deliberately wired to the real aggregator rather than a fake enricher: a fake
// that consulted the lock itself would be testing the fake.
func TestHydrateHardcoverEditionsRespectsDescriptionLock(t *testing.T) {
	tests := []struct {
		name        string
		description string
		locked      bool
		want        string
	}{
		{name: "locked empty survives", locked: true, want: ""},
		{name: "locked non-empty survives", description: "mine", locked: true, want: "mine"},
		{name: "unlocked empty is filled", want: "Audnex summary"},
		{name: "unlocked non-empty is kept", description: "theirs", want: "theirs"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			books, editions, book, ctx := newHydrateBook(t, "hc:locked-description", "hardcover", models.MediaTypeAudiobook)
			audioASIN := "b222222222"
			book.Description = tc.description
			if tc.locked {
				book.LockField(models.BookFieldDescription)
			}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}

			stub := &audnexStub{book: &audnex.Book{
				ASIN:      "B222222222",
				Summary:   "Audnex summary",
				Narrators: []audnex.Person{{Name: "Kate Reading"}},
			}}
			enricher := metadata.NewAggregator(nil).WithAudnexClient(stub)

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
						Monitored: true,
					}}, nil
				},
				Enricher: enricher,
			})
			if result.Err != nil {
				t.Fatalf("hydrate err = %v", result.Err)
			}
			// Audnex really answered and the book really was written, so a pass
			// cannot come from hydration stopping short of the enrichment.
			if stub.calls != 1 || !result.AudiobookEnriched || !result.BookUpdated {
				t.Fatalf("enrichment did not run: audnexCalls=%d result=%+v", stub.calls, result)
			}
			if book.Narrator != "Kate Reading" {
				t.Fatalf("narrator = %q, want the enrichment to have run", book.Narrator)
			}
			if book.Description != tc.want {
				t.Errorf("description after hydration = %q, want %q", book.Description, tc.want)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Description != tc.want {
				t.Errorf("persisted description = %q, want %q", stored.Description, tc.want)
			}
		})
	}
}
