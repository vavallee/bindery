package hardcoverlistsyncer

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// recordingSearcher captures the books a sync pass starts an immediate search
// for, so these tests can assert the wanted-transition rule without a real
// indexer behind it.
type recordingSearcher struct {
	ch chan models.Book
}

func newRecordingSearcher() *recordingSearcher {
	return &recordingSearcher{ch: make(chan models.Book, 16)}
}

func (r *recordingSearcher) SearchAndGrabBook(_ context.Context, book models.Book) {
	r.ch <- book
}

func (r *recordingSearcher) waitForCall(t *testing.T, timeout time.Duration) models.Book {
	t.Helper()
	select {
	case b := <-r.ch:
		return b
	case <-time.After(timeout):
		t.Fatal("timeout: the list sync did not start a search")
		return models.Book{}
	}
}

func (r *recordingSearcher) assertNoCall(t *testing.T, window time.Duration) {
	t.Helper()
	select {
	case b := <-r.ch:
		t.Errorf("unexpected search started for %q", b.Title)
	case <-time.After(window):
	}
}

// newSearchingSyncer wires the in-memory syncer fixture with a recording
// searcher so a pass's searches are observable.
func newSearchingSyncer(t *testing.T) (*ListSyncer, *db.ImportListRepo, *recordingSearcher) {
	t.Helper()
	s, repo := newTestSyncer(t)
	searcher := newRecordingSearcher()
	s.WithSearcher(searcher)
	return s, repo, searcher
}

// A Hardcover list whose MonitorNew is on creates its books wanted and
// monitored, which is exactly the state that earns one immediate search. The
// syncer had no searcher at all, so the book waited up to search.interval for
// the sweep (#2722).
func TestSyncOne_MonitoredNewBookStartsSearch(t *testing.T) {
	s, repo, searcher := newSearchingSyncer(t)
	ctx := context.Background()

	il := testImportList("Favourites", "hardcover", true)
	il.MonitorNew = true
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}},
			books: []models.Book{{
				ForeignID: "hc:new-monitored", Title: "Newly Wanted", MetadataProvider: "hardcover",
				Author: &models.Author{ForeignID: "hc:list-author", Name: "List Author", MetadataProvider: "hardcover"},
			}},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	created, err := s.books.GetByForeignID(ctx, "hc:new-monitored")
	if err != nil || created == nil {
		t.Fatalf("book not created: %v", err)
	}
	if call := searcher.waitForCall(t, time.Second); call.ID != created.ID {
		t.Errorf("search started for book %d, want %d", call.ID, created.ID)
	}
}

// MonitorNew off means "catalogue it, don't fetch it". The book is still
// created wanted, but the sync must not start a search for it (#2124, #2722).
func TestSyncOne_UnmonitoredNewBookDoesNotStartSearch(t *testing.T) {
	s, repo, searcher := newSearchingSyncer(t)
	ctx := context.Background()

	il := testImportList("Wishlist", "hardcover", true)
	il.MonitorNew = false
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 8, Slug: il.URL, Name: il.Name}},
			books: []models.Book{{
				ForeignID: "hc:wishlisted", Title: "Just Wishlisted", MetadataProvider: "hardcover",
				Author: &models.Author{ForeignID: "hc:wish-author", Name: "Wish Author", MetadataProvider: "hardcover"},
			}},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	created, err := s.books.GetByForeignID(ctx, "hc:wishlisted")
	if err != nil || created == nil {
		t.Fatalf("book not created: %v", err)
	}
	if created.Monitored {
		t.Fatalf("precondition: book should be unmonitored, got %+v", created)
	}
	searcher.assertNoCall(t, 200*time.Millisecond)
}

// The complementary-list path from #1634 reopens an owned book as wanted. That
// is a wanted transition too, so it must start the same immediate search.
func TestSyncOne_WideningAnImportedBookStartsSearch(t *testing.T) {
	s, repo, searcher := newSearchingSyncer(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "hc:widen-author", Name: "Widen Author", MetadataProvider: "hardcover", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	existing := &models.Book{
		ForeignID: "hc:widen", AuthorID: author.ID, Title: "Owned Ebook", SortTitle: "owned ebook",
		Status: models.BookStatusImported, MediaType: models.MediaTypeEbook,
		Monitored: true, Genres: []string{}, MetadataProvider: "hardcover",
	}
	if err := s.books.Create(ctx, existing); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	if err := s.books.AddBookFile(ctx, existing.ID, models.MediaTypeEbook, "/library/widen.epub"); err != nil {
		t.Fatalf("import the ebook: %v", err)
	}

	il := testImportList("Audiobooks", "hardcover", true)
	il.URL = "audiobooks"
	il.MediaType = models.MediaTypeAudiobook
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 2, Slug: il.URL, Name: il.Name}},
			books: []models.Book{{
				ForeignID: "hc:widen", Title: "Owned Ebook", MetadataProvider: "hardcover",
				MediaType: models.MediaTypeBoth,
				Author:    &models.Author{ForeignID: "hc:widen-author", Name: "Widen Author", MetadataProvider: "hardcover"},
			}},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	got, err := s.books.GetByForeignID(ctx, "hc:widen")
	if err != nil || got == nil {
		t.Fatalf("widened book not found: %v", err)
	}
	if got.Status != models.BookStatusWanted || got.MediaType != models.MediaTypeBoth {
		t.Fatalf("book = %q/%q, want wanted/both", got.Status, got.MediaType)
	}
	if call := searcher.waitForCall(t, time.Second); call.ID != existing.ID {
		t.Errorf("search started for book %d, want %d", call.ID, existing.ID)
	}
}

// The other half of the rule: a book that was already wanted and monitored
// does not get another search just because a later sync pass widens it. The
// wanted sweep owns repeat searches (#2722).
func TestSyncOne_WideningAnAlreadyWantedBookDoesNotStartSearch(t *testing.T) {
	s, repo, searcher := newSearchingSyncer(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "hc:wanted-author", Name: "Wanted Author", MetadataProvider: "hardcover", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	existing := &models.Book{
		ForeignID: "hc:wanted", AuthorID: author.ID, Title: "Wanted Ebook", SortTitle: "wanted ebook",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Monitored: true, Genres: []string{}, MetadataProvider: "hardcover",
	}
	if err := s.books.Create(ctx, existing); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	il := testImportList("Audiobooks", "hardcover", true)
	il.URL = "audiobooks"
	il.MediaType = models.MediaTypeAudiobook
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 2, Slug: il.URL, Name: il.Name}},
			books: []models.Book{{
				ForeignID: "hc:wanted", Title: "Wanted Ebook", MetadataProvider: "hardcover",
				MediaType: models.MediaTypeBoth,
				Author:    &models.Author{ForeignID: "hc:wanted-author", Name: "Wanted Author", MetadataProvider: "hardcover"},
			}},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	got, err := s.books.GetByForeignID(ctx, "hc:wanted")
	if err != nil || got == nil {
		t.Fatalf("widened book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("mediaType = %q, want both", got.MediaType)
	}
	searcher.assertNoCall(t, 200*time.Millisecond)
}
