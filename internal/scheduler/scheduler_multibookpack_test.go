package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// resultSearcher returns a fixed result set so the decision loop can be
// exercised without a network. stubSearcher returns nil and so never reaches
// it.
type resultSearcher struct{ results []newznab.SearchResult }

func (r *resultSearcher) SearchBook(_ context.Context, _ []models.Indexer, _ indexer.MatchCriteria) []newznab.SearchResult {
	return r.results
}

// packGrabFixture builds a Scheduler with real repositories and one enabled
// torrent client, so searchAndGrabFormat runs all the way to creating a
// download row. SendDownload then fails (there is no client listening), which
// is after the row exists and is exactly what these tests read.
func packGrabFixture(t *testing.T, results []newznab.SearchResult) (*Scheduler, *db.DownloadRepo, models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	indexers := db.NewIndexerRepo(database)

	client := &models.DownloadClient{
		Name: "qbit", Type: "qbittorrent", Host: "127.0.0.1", Port: 1,
		Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true,
	}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{Name: "stub", URL: "http://127.0.0.1", APIKey: "k", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	for i := range results {
		results[i].IndexerID = idx.ID
	}

	author := &models.Author{ForeignID: "OL-pack", Name: "Pierce Brown", SortName: "Brown, Pierce"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-pack-book", AuthorID: author.ID, Title: "Red Rising",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{
		searcher:  &resultSearcher{results: results},
		downloads: downloads,
		clients:   clients,
		authors:   authors,
		books:     books,
		pending:   db.NewPendingReleaseRepo(database),
	}
	return s, downloads, *book, ctx
}

// TestAutoGrab_SkipsPacksNamedAfterTheBook is the wiring test for #3247: the
// automatic path hands the spec the author's other titles, so a semicolon
// list of them is refused, and a range after the wanted title is refused.
func TestAutoGrab_SkipsPacksNamedAfterTheBook(t *testing.T) {
	for _, tc := range []struct {
		name, book, release string
		siblings            []string
	}{
		{"range after the title", "Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades 1-2", nil},
		{"range after the title in an audiobook release", "Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades 1-2 [Audiobook]", nil},
		{
			"semicolon list of the author's titles", "Chamber",
			"John Grisham - Three Classic Thrillers (The Firm; The Appeal; The Chamber) (v5.0) (epub)",
			[]string{"The Firm", "The Appeal"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pack := newznab.SearchResult{GUID: "guid-pack", Title: tc.release, Protocol: "torrent", NZBURL: "magnet:?xt=pack"}
			s, downloads, book, ctx := packGrabFixture(t, []newznab.SearchResult{pack})
			book.Title = tc.book
			if err := s.books.Update(ctx, &book); err != nil {
				t.Fatal(err)
			}
			for i, title := range tc.siblings {
				sib := &models.Book{ForeignID: "OL-pack-sib-" + title, AuthorID: book.AuthorID, Title: title, MediaType: models.MediaTypeEbook}
				if err := s.books.Create(ctx, sib); err != nil {
					t.Fatalf("sibling %d: %v", i, err)
				}
			}

			s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

			if dl, err := downloads.GetByGUID(ctx, "guid-pack"); err != nil {
				t.Fatal(err)
			} else if dl != nil {
				t.Fatalf("the pack %q was grabbed for the single book %q", tc.release, tc.book)
			}
		})
	}
}

func packRelease() newznab.SearchResult {
	return newznab.SearchResult{
		GUID:     "guid-pack",
		Title:    "Red Rising Series - Books 1 - 4 by Pierce Brown [ENG / M4B MP3] [VIP]",
		Protocol: "torrent",
		NZBURL:   "magnet:?xt=pack",
	}
}

func singleRelease() newznab.SearchResult {
	return newznab.SearchResult{
		GUID:     "guid-single",
		Title:    "Red Rising - Pierce Brown [EPUB]",
		Protocol: "torrent",
		NZBURL:   "magnet:?xt=single",
	}
}

// TestAutoGrab_SkipsMultiBookPack is the wiring test for #2276. The spec
// existing is not the same as the automatic path constructing it, so this
// drives searchAndGrabFormat and reads what was actually grabbed.
func TestAutoGrab_SkipsMultiBookPack(t *testing.T) {
	t.Run("pack is the only candidate: nothing is grabbed", func(t *testing.T) {
		s, downloads, book, ctx := packGrabFixture(t, []newznab.SearchResult{packRelease()})

		s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

		if dl, err := downloads.GetByGUID(ctx, "guid-pack"); err != nil {
			t.Fatal(err)
		} else if dl != nil {
			t.Fatalf("the four-book pack was grabbed for the single book %q", book.Title)
		}
	})

	t.Run("pack ranked first: the single-book release is grabbed instead", func(t *testing.T) {
		s, downloads, book, ctx := packGrabFixture(t, []newznab.SearchResult{packRelease(), singleRelease()})

		s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

		if dl, err := downloads.GetByGUID(ctx, "guid-pack"); err != nil {
			t.Fatal(err)
		} else if dl != nil {
			t.Error("the pack was grabbed even though a single-book release was available")
		}
		dl, err := downloads.GetByGUID(ctx, "guid-single")
		if err != nil {
			t.Fatal(err)
		}
		if dl == nil {
			t.Fatal("the single-book release was not grabbed")
		}
	})

	// A pack must not be parked in pending either. Pending is for releases
	// that become grabbable later or on approval, and approving a pack would
	// only walk it into the importer's own block.
	t.Run("a pack is not stored as a pending release", func(t *testing.T) {
		s, _, book, ctx := packGrabFixture(t, []newznab.SearchResult{packRelease()})

		s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

		got, err := s.pending.ListByBook(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("pack was parked in pending releases: %+v", got)
		}
	})
}
