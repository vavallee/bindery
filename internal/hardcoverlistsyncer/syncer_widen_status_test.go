package hardcoverlistsyncer

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// A complementary audiobook list widening a book the user already owns as an
// ebook creates a genuine gap: the audiobook slot becomes monitored with
// nothing behind it. The row must leave 'imported', or the wanted page, the
// scheduled sweep and the author bulk search all skip it forever and the
// format this sync just asked for is never searched (#1634).
func TestSyncOne_WideningAnImportedBookReopensItAsWanted(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	ebookList := testImportList("Ebooks", "hardcover", true)
	ebookList.URL = "ebooks"
	ebookList.MediaType = models.MediaTypeEbook
	if err := repo.Create(ctx, &ebookList); err != nil {
		t.Fatalf("seed ebook list: %v", err)
	}
	audiobookList := testImportList("Audiobooks", "hardcover", true)
	audiobookList.URL = "audiobooks"
	audiobookList.MediaType = models.MediaTypeAudiobook
	if err := repo.Create(ctx, &audiobookList); err != nil {
		t.Fatalf("seed audiobook list: %v", err)
	}

	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{
				{ID: 1, Slug: ebookList.URL, Name: ebookList.Name},
				{ID: 2, Slug: audiobookList.URL, Name: audiobookList.Name},
			},
			books: []models.Book{{
				ForeignID:        "hc:owned-ebook",
				Title:            "Owned Ebook",
				MetadataProvider: "hardcover",
				MediaType:        models.MediaTypeBoth,
				Author: &models.Author{
					ForeignID:        "hc:owned-author",
					Name:             "Owned Author",
					MetadataProvider: "hardcover",
				},
			}},
		}
	})

	if err := s.SyncOne(ctx, ebookList.ID); err != nil {
		t.Fatalf("sync ebook list: %v", err)
	}
	tracked, err := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if err != nil || tracked == nil {
		t.Fatalf("book not created: %v", err)
	}

	// The user then imports the ebook. AddBookFile derives the status, so the
	// book is legitimately 'imported' for its declared single format.
	if err := s.books.AddBookFile(ctx, tracked.ID, models.MediaTypeEbook, "/library/owned.epub"); err != nil {
		t.Fatalf("import the ebook: %v", err)
	}
	before, _ := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if before.Status != models.BookStatusImported || before.MediaType != models.MediaTypeEbook {
		t.Fatalf("precondition: book = %q/%q, want imported ebook", before.Status, before.MediaType)
	}

	// The complementary audiobook list now widens it to 'both'.
	if err := s.SyncOne(ctx, audiobookList.ID); err != nil {
		t.Fatalf("sync audiobook list: %v", err)
	}

	got, err := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if err != nil || got == nil {
		t.Fatalf("widened book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("mediaType = %q, want both", got.MediaType)
	}
	if got.AudiobookFilePath != "" {
		t.Fatalf("audiobook path = %q, want empty: nothing imported it", got.AudiobookFilePath)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want wanted: the audiobook slot is monitored and empty", got.Status)
	}
}

// TestSyncOne_WideningKeepsLockedASIN pins that the widen path copies the
// audiobook list's ASIN onto a tracked book only when the user has not locked
// the field. The lock set is already on the loaded row; it was simply not
// consulted here.
func TestSyncOne_WideningKeepsLockedASIN(t *testing.T) {
	for _, locked := range []bool{true, false} {
		name := "unlocked takes the list asin"
		if locked {
			name = "locked keeps the user asin"
		}
		t.Run(name, func(t *testing.T) {
			s, repo := newTestSyncer(t)
			ctx := context.Background()

			ebookList := testImportList("Ebooks", "hardcover", true)
			ebookList.URL = "ebooks"
			ebookList.MediaType = models.MediaTypeEbook
			if err := repo.Create(ctx, &ebookList); err != nil {
				t.Fatalf("seed ebook list: %v", err)
			}
			audiobookList := testImportList("Audiobooks", "hardcover", true)
			audiobookList.URL = "audiobooks"
			audiobookList.MediaType = models.MediaTypeAudiobook
			if err := repo.Create(ctx, &audiobookList); err != nil {
				t.Fatalf("seed audiobook list: %v", err)
			}
			s.WithClientFactory(func(string) hardcoverClient {
				return &fakeHardcoverClient{
					lists: []hardcover.HCList{
						{ID: 1, Slug: ebookList.URL, Name: ebookList.Name},
						{ID: 2, Slug: audiobookList.URL, Name: audiobookList.Name},
					},
					books: []models.Book{{
						ForeignID: "hc:asin-lock", Title: "Locked ASIN", MetadataProvider: "hardcover",
						MediaType: models.MediaTypeBoth, ASIN: "B0LISTASIN",
						Author: &models.Author{ForeignID: "hc:asin-author", Name: "Lock Author", MetadataProvider: "hardcover"},
					}},
				}
			})

			if err := s.SyncOne(ctx, ebookList.ID); err != nil {
				t.Fatalf("sync ebook list: %v", err)
			}
			tracked, err := s.books.GetByForeignID(ctx, "hc:asin-lock")
			if err != nil || tracked == nil {
				t.Fatalf("book not created: %v", err)
			}
			tracked.ASIN = "B0USERASIN"
			if locked {
				tracked.LockField(models.BookFieldASIN)
			}
			if err := s.books.Update(ctx, tracked); err != nil {
				t.Fatal(err)
			}

			if err := s.SyncOne(ctx, audiobookList.ID); err != nil {
				t.Fatalf("sync audiobook list: %v", err)
			}
			got, err := s.books.GetByForeignID(ctx, "hc:asin-lock")
			if err != nil || got == nil {
				t.Fatalf("widened book not found: %v", err)
			}
			if got.MediaType != models.MediaTypeBoth {
				t.Fatalf("mediaType = %q, want both", got.MediaType)
			}
			want := "B0LISTASIN"
			if locked {
				want = "B0USERASIN"
			}
			if got.ASIN != want {
				t.Fatalf("asin = %q, want %q (locked=%v)", got.ASIN, want, locked)
			}
			if locked && !got.IsFieldLocked(models.BookFieldASIN) {
				t.Fatalf("widen dropped the asin lock: %v", got.LockedFields)
			}
		})
	}
}

// TestWidenTrackedBook_KeepsLockSetAfterRead pins the widen write against a
// user edit that lands between the sync's read and its write: the ASIN the
// user set and locked stays, and the widen itself still lands on retry.
func TestWidenTrackedBook_KeepsLockSetAfterRead(t *testing.T) {
	s, _ := newTestSyncer(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "hc:widen-race-author", Name: "A", SortName: "A", MetadataProvider: "hardcover", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "hc:widen-race", AuthorID: author.ID, Title: "Book", SortTitle: "Book", Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover", Genres: []string{}, Monitored: true}
	if err := s.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.books.GetByID(ctx, book.ID)

	fresh, _ := s.books.GetByID(ctx, book.ID)
	fresh.ASIN = "B0USERASIN"
	fresh.LockField(models.BookFieldASIN)
	if err := s.books.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	if err := s.widenTrackedBook(ctx, stale, models.Book{Title: "Book", ASIN: "B0LISTASIN"}, models.MediaTypeAudiobook); err != nil {
		t.Fatal(err)
	}
	got, _ := s.books.GetByID(ctx, book.ID)
	if got.ASIN != "B0USERASIN" || !got.IsFieldLocked(models.BookFieldASIN) {
		t.Fatalf("stale widen write undid the user's lock: asin=%q locked=%v", got.ASIN, got.LockedFields)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("widen did not land on retry: mediaType=%q", got.MediaType)
	}
}

// newWidenTestSyncer exposes the database so a test can make every guarded
// write miss.
func newWidenTestSyncer(t *testing.T) (*ListSyncer, *sql.DB) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return New(db.NewImportListRepo(database), db.NewAuthorRepo(database), db.NewBookRepo(database)), database
}

func seedWidenBook(t *testing.T, s *ListSyncer, foreignID string) *models.Book {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: foreignID + "-author", Name: "A", SortName: "A", MetadataProvider: "hardcover", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: foreignID, AuthorID: author.ID, Title: "Book", SortTitle: "Book", Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover", Genres: []string{}, Monitored: true}
	if err := s.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stored, err := s.books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	return stored
}

// A widen whose every guarded write misses gives up after three and returns
// an error, which syncOne counts as a failed book, instead of looping or
// writing over the row unguarded.
func TestWidenTrackedBook_GivesUpWhenTheBookKeepsChanging(t *testing.T) {
	s, database := newWidenTestSyncer(t)
	ctx := context.Background()
	stale := seedWidenBook(t, s, "hc:widen-gives-up")
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER keep_changing BEFORE UPDATE ON books BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}

	err := s.widenTrackedBook(ctx, stale, models.Book{Title: "Book"}, models.MediaTypeAudiobook)
	if !errors.Is(err, db.ErrBookKeptChanging) {
		t.Fatalf("expected ErrBookKeptChanging, got %v", err)
	}
	got, _ := s.books.GetByID(ctx, stale.ID)
	if got.MediaType != models.MediaTypeEbook {
		t.Fatalf("widen landed although every write was swallowed: mediaType=%q", got.MediaType)
	}
}

// A book with no updated_at to compare against, such as one created earlier
// in the same sync, is written directly.
func TestWidenTrackedBook_WritesDirectlyWithoutUpdatedAt(t *testing.T) {
	s, _ := newWidenTestSyncer(t)
	ctx := context.Background()
	book := seedWidenBook(t, s, "hc:widen-direct")
	book.UpdatedAtRaw = ""

	if err := s.widenTrackedBook(ctx, book, models.Book{Title: "Book"}, models.MediaTypeAudiobook); err != nil {
		t.Fatal(err)
	}
	got, _ := s.books.GetByID(ctx, book.ID)
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("direct write did not land: mediaType=%q", got.MediaType)
	}
}
