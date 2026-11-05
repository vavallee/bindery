package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// secondBook adds a book for the same work as the fixture's, owned by alice,
// the way two author rows or two accounts each end up with their own copy.
func (f *regrabFixture) secondBook(t *testing.T) models.Book {
	t.Helper()
	other := models.Book{
		ForeignID: "OL-REGRAB-B2", AuthorID: f.book.AuthorID, Title: f.book.Title,
		SortTitle: f.book.SortTitle, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeEbook, OwnerUserID: f.alice,
	}
	if err := db.NewBookRepo(f.database).Create(context.Background(), &other); err != nil {
		t.Fatalf("second book create: %v", err)
	}
	return other
}

// TestSearchAndGrabFormat_TriesTheNextReleaseWhenAnotherBookHoldsTheBest is
// #3564 part 1. Another book's live download holds the best release. The
// GUID is unique in downloads, so this book cannot have that one, but the
// search used to stop there and leave the book wanted with a good second
// release in hand. It must pass the held release over and grab the next.
func TestSearchAndGrabFormat_TriesTheNextReleaseWhenAnotherBookHoldsTheBest(t *testing.T) {
	for _, status := range []models.DownloadState{models.StateDownloading, models.StateImported} {
		t.Run(string(status), func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
			logs := captureLogs(t)
			other := f.secondBook(t)

			first := f.s.searcher.(*fixedResultsSearcher).results[0]
			second := first
			second.GUID = "g-3564-second"
			second.Title = "Regrab Book.second.epub"
			f.s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{first, second}}

			held := &models.Download{
				GUID: first.GUID, BookID: &other.ID, OwnerUserID: f.alice, Title: first.Title,
				NZBURL: "http://old.example/old.nzb", Status: status, Protocol: "usenet",
			}
			if err := f.downloads.Create(ctx, held); err != nil {
				t.Fatal(err)
			}

			f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

			if n := f.adds.Load(); n != 1 {
				t.Fatalf("#3564: expected the next approved release sent to the client, got %d requests\n%s", n, logs.String())
			}
			got, err := f.downloads.GetByGUID(ctx, second.GUID)
			if err != nil || got == nil {
				t.Fatalf("expected a download for the second release, got %v, %v", got, err)
			}
			if got.BookID == nil || *got.BookID != f.book.ID || got.OwnerUserID != f.bob {
				t.Errorf("second release grabbed for book %v owner %d, want book %d owner %d", got.BookID, got.OwnerUserID, f.book.ID, f.bob)
			}
			kept, err := f.downloads.GetByID(ctx, held.ID)
			if err != nil || kept == nil {
				t.Fatalf("reload the other book's row: %v", err)
			}
			if kept.Status != status || kept.BookID == nil || *kept.BookID != other.ID || kept.OwnerUserID != f.alice {
				t.Errorf("the other book's row must be untouched, got %+v", kept)
			}
			if out := logs.String(); !strings.Contains(out, "skipping a release another book's download holds") {
				t.Errorf("the skip must be logged, got\n%s", out)
			}
		})
	}
}

// TestSearchAndGrabFormat_OwnLiveRowStillStopsTheSearch pins the half of the
// old rule that stays: this book's own live download of the best release means
// the book is already being fetched, so no second release is grabbed for it.
func TestSearchAndGrabFormat_OwnLiveRowStillStopsTheSearch(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	first := f.s.searcher.(*fixedResultsSearcher).results[0]
	second := first
	second.GUID = "g-3564-own-second"
	f.s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{first, second}}

	own := &models.Download{
		GUID: first.GUID, BookID: &f.book.ID, OwnerUserID: f.bob, Title: first.Title,
		NZBURL: "http://old.example/old.nzb", Status: models.StateDownloading, Protocol: "usenet",
	}
	if err := f.downloads.Create(ctx, own); err != nil {
		t.Fatal(err)
	}

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	if n := f.adds.Load(); n != 0 {
		t.Errorf("a book with its own live download must not grab a second release; the client got %d requests", n)
	}
	if got, err := f.downloads.GetByGUID(ctx, second.GUID); err != nil || got != nil {
		t.Errorf("no row for the second release expected, got %+v, %v", got, err)
	}
}

// TestSearchAndGrabFormat_EachBookKeepsItsOwnPendingRelease is #3564 part 2,
// end to end. A delay profile holds the same release back for two books that
// want it. Each book must list it as its own pending release; before, the
// second book's entry folded into the first's and it never saw the release.
func TestSearchAndGrabFormat_EachBookKeepsItsOwnPendingRelease(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	other := f.secondBook(t)

	res := f.s.searcher.(*fixedResultsSearcher).results[0]
	res.PubDate = time.Now().UTC().Format(time.RFC1123Z)
	f.s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{res}}

	delays := db.NewDelayProfileRepo(f.database)
	if err := delays.Create(ctx, &models.DelayProfile{
		UsenetDelay: 600, TorrentDelay: 600, PreferredProtocol: "usenet",
		EnableUsenet: true, EnableTorrent: true, Order: -1,
	}); err != nil {
		t.Fatal(err)
	}
	f.s.delayProfiles = delays
	pending := db.NewPendingReleaseRepo(f.database)
	f.s.pending = pending

	f.s.searchAndGrabFormat(ctx, other, models.MediaTypeEbook, nil)
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	if n := f.adds.Load(); n != 0 {
		t.Fatalf("a delayed release must not be grabbed; the client got %d requests", n)
	}
	for _, b := range []models.Book{other, f.book} {
		rows, err := pending.ListByBookAndMediaType(ctx, b.ID, models.MediaTypeEbook)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].GUID != res.GUID {
			t.Errorf("#3564: book %d pending rows = %+v, want its own row for %s", b.ID, rows, res.GUID)
		}
	}
	forBob, err := pending.ListForUser(ctx, f.bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(forBob) != 1 || forBob[0].BookID != f.book.ID {
		t.Errorf("bob's pending list = %+v, want his own book's row", forBob)
	}
}

// TestSearchAndGrabFormat_PendingRowHeldByAnotherBooksImportIsDropped: a
// pending row whose release another book has since imported can never be
// grabbed for this book, so the sweep removes it instead of leaving it in
// Pending for good. While the other book is still downloading it, the row
// stays: that download may fail and free the release.
func TestSearchAndGrabFormat_PendingRowHeldByAnotherBooksImportIsDropped(t *testing.T) {
	for _, tc := range []struct {
		status models.DownloadState
		kept   int
	}{
		{models.StateImported, 0},
		{models.StateDownloading, 1},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
			other := f.secondBook(t)
			res := f.s.searcher.(*fixedResultsSearcher).results[0]
			f.s.searcher = &fixedResultsSearcher{}
			pending := db.NewPendingReleaseRepo(f.database)
			f.s.pending = pending
			blob, _ := json.Marshal(res)
			if err := pending.Upsert(ctx, &models.PendingRelease{
				BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: res.Title, GUID: res.GUID,
				Protocol: "usenet", Reason: "usenet delay not met", ReleaseJSON: string(blob),
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.downloads.Create(ctx, &models.Download{
				GUID: res.GUID, BookID: &other.ID, OwnerUserID: f.alice, Title: res.Title,
				NZBURL: "http://old.example/old.nzb", Status: tc.status, Protocol: "usenet",
			}); err != nil {
				t.Fatal(err)
			}

			f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

			if n := f.adds.Load(); n != 0 {
				t.Fatalf("a release another book holds must not be grabbed; the client got %d requests", n)
			}
			rows, err := pending.ListByBookAndMediaType(ctx, f.book.ID, models.MediaTypeEbook)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.kept {
				t.Errorf("pending rows = %d, want %d", len(rows), tc.kept)
			}
		})
	}
}
