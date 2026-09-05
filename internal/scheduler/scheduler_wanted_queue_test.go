package scheduler

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// wantedQueueFixture builds an in-memory library with one author and the repos
// the wanted sweep reads, so each case below only has to describe its books and
// downloads.
type wantedQueueFixture struct {
	t         *testing.T
	ctx       context.Context
	database  *sql.DB
	books     *db.BookRepo
	downloads *db.DownloadRepo
	authorID  int64
}

func newWantedQueueFixture(t *testing.T) *wantedQueueFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL-WQ", Name: "Wanted Queue", SortName: "Wanted Queue",
		MetadataProvider: "ol", Monitored: true,
	}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	return &wantedQueueFixture{
		t:         t,
		ctx:       ctx,
		database:  database,
		books:     db.NewBookRepo(database),
		downloads: db.NewDownloadRepo(database),
		authorID:  author.ID,
	}
}

func (f *wantedQueueFixture) book(title, mediaType string) *models.Book {
	f.t.Helper()
	b := &models.Book{
		ForeignID: title, AuthorID: f.authorID, Title: title, SortTitle: title,
		Status: models.BookStatusWanted, MediaType: mediaType,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	}
	if err := f.books.Create(f.ctx, b); err != nil {
		f.t.Fatalf("create book %s: %v", title, err)
	}
	return b
}

// download creates a download row for a book with an explicit format token as
// its Quality, which is what a real grab records (see SearchAndGrabBook). The
// state is written directly because the import states are several legal hops
// away from StateGrabbed and the path there is not what these cases are about.
func (f *wantedQueueFixture) download(b *models.Book, quality string, state models.DownloadState) {
	f.t.Helper()
	dl := &models.Download{
		GUID:     "guid-" + b.ForeignID + "-" + quality,
		BookID:   &b.ID,
		Title:    b.Title + " [" + quality + "]",
		Quality:  quality,
		Status:   models.StateGrabbed,
		Protocol: "torrent",
	}
	if err := f.downloads.Create(f.ctx, dl); err != nil {
		f.t.Fatalf("create download for %s: %v", b.Title, err)
	}
	if state != models.StateGrabbed {
		if _, err := f.database.ExecContext(f.ctx,
			"UPDATE downloads SET status=? WHERE id=?", state, dl.ID); err != nil {
			f.t.Fatalf("set download status %s: %v", state, err)
		}
	}
}

func (f *wantedQueueFixture) queue() []wantedSearch {
	f.t.Helper()
	s := &Scheduler{books: f.books, downloads: f.downloads}
	return s.wantedSearchQueue(f.ctx)
}

// entryFor returns the sweep entry for a book, or nil when the sweep dropped it.
func entryFor(queue []wantedSearch, id int64) *wantedSearch {
	for i := range queue {
		if queue[i].book.ID == id {
			return &queue[i]
		}
	}
	return nil
}

// TestWantedSearchQueue_ParkedFormatLeavesSiblingSearchable is the regression
// guard for #2365: a media_type='both' book whose ebook is parked in a
// non-terminal import state must stay in the sweep, with only the audiobook
// left to search. Before the per-format filter the whole book was dropped, so
// under pair gating (#942) the hold waited for a sibling that the hold itself
// had prevented anyone from searching for.
func TestWantedSearchQueue_ParkedFormatLeavesSiblingSearchable(t *testing.T) {
	for _, state := range []models.DownloadState{models.StateImportHeld, models.StateImportExternal} {
		t.Run(string(state), func(t *testing.T) {
			f := newWantedQueueFixture(t)
			book := f.book("Parked Ebook "+string(state), models.MediaTypeBoth)
			f.download(book, "epub", state)

			entry := entryFor(f.queue(), book.ID)
			if entry == nil {
				t.Fatalf("dual-format book with only its ebook parked in %s was dropped from the sweep", state)
			}
			if len(entry.formats) != 1 || entry.formats[0] != models.MediaTypeAudiobook {
				t.Errorf("formats to search: want [%s], got %v", models.MediaTypeAudiobook, entry.formats)
			}
		})
	}
}

// TestWantedSearchQueue_SingleFormatDownloadingStillSkipped pins the original
// behaviour: an ebook-only book whose one grab is downloading must not be
// re-searched (the double-grab bug the filter exists for).
func TestWantedSearchQueue_SingleFormatDownloadingStillSkipped(t *testing.T) {
	f := newWantedQueueFixture(t)
	book := f.book("Single Format Downloading", models.MediaTypeEbook)
	f.download(book, "epub", models.StateDownloading)

	if entry := entryFor(f.queue(), book.ID); entry != nil {
		t.Errorf("single-format book with a downloading grab was queued for re-search: %v", entry.formats)
	}
}

// TestWantedSearchQueue_BothFormatsInFlightSkipped verifies the book is still
// dropped entirely when every format it needs already has a live download.
func TestWantedSearchQueue_BothFormatsInFlightSkipped(t *testing.T) {
	f := newWantedQueueFixture(t)
	book := f.book("Both In Flight", models.MediaTypeBoth)
	f.download(book, "epub", models.StateImportHeld)
	f.download(book, "m4b", models.StateDownloading)

	if entry := entryFor(f.queue(), book.ID); entry != nil {
		t.Errorf("book with both formats in flight was queued: %v", entry.formats)
	}
}

// TestWantedSearchQueue_UndeterminedFormatBlocksWholeBook covers the cautious
// direction: a download whose release names no format Bindery recognises could
// be either slot, so it keeps blocking both rather than risk a duplicate grab.
func TestWantedSearchQueue_UndeterminedFormatBlocksWholeBook(t *testing.T) {
	f := newWantedQueueFixture(t)
	book := f.book("Untyped Release", models.MediaTypeBoth)
	f.download(book, "", models.StateDownloading)

	if entry := entryFor(f.queue(), book.ID); entry != nil {
		t.Errorf("book with an unresolvable in-flight format was queued: %v", entry.formats)
	}
}

// TestWantedSearchQueue_FormatOnDiskNotSearched confirms the sweep still
// respects what is already imported: a 'both' book with its ebook on disk and
// its audiobook downloading has nothing left to search.
func TestWantedSearchQueue_FormatOnDiskNotSearched(t *testing.T) {
	f := newWantedQueueFixture(t)
	book := f.book("Half Imported", models.MediaTypeBoth)
	book.EbookFilePath = "/lib/half-imported.epub"
	if err := f.books.Update(f.ctx, book); err != nil {
		t.Fatalf("update book: %v", err)
	}
	f.download(book, "m4b", models.StateDownloading)

	if entry := entryFor(f.queue(), book.ID); entry != nil {
		t.Errorf("book with its only missing format in flight was queued: %v", entry.formats)
	}
}

// TestWantedSearchQueue_SingleFormatBookIgnoresReleaseTokens pins the
// media-type precedence: a book monitored for one format has one slot, so any
// live download for it blocks that book no matter what the release title says.
//
// Both titles below resolve to "ebook" through downloadMediaType while the
// book is audiobook-only. The first names a PDF booklet and no audio
// container; the second is Mary Karr's memoir "Lit", whose own title carries
// the "lit" ebook token. Inferring from the release alone would leave the
// audiobook slot looking free and grab a second copy of the release that is
// already downloading.
func TestWantedSearchQueue_SingleFormatBookIgnoresReleaseTokens(t *testing.T) {
	cases := []struct {
		name  string
		title string
		qual  string
	}{
		{"pdf booklet, no audio container", "Some Author - Audio Only (Unabridged) [64kbps] [PDF booklet]", "pdf"},
		{"format token inside the book title", "Mary Karr - Lit A Memoir (Unabridged)", "lit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWantedQueueFixture(t)
			book := f.book(tc.name, models.MediaTypeAudiobook)
			dl := &models.Download{
				GUID:     "guid-" + tc.name,
				BookID:   &book.ID,
				Title:    tc.title,
				Quality:  tc.qual,
				Status:   models.StateDownloading,
				Protocol: "torrent",
			}
			if err := f.downloads.Create(f.ctx, dl); err != nil {
				t.Fatalf("create download: %v", err)
			}
			if got := downloadMediaType(dl); got != models.MediaTypeEbook {
				t.Fatalf("precondition: this case only bites when the release reads as an ebook, got %q", got)
			}
			if entry := entryFor(f.queue(), book.ID); entry != nil {
				t.Errorf("audiobook-only book with its audiobook downloading was queued anyway: %v (duplicate grab)", entry.formats)
			}
		})
	}
}

// TestSearchAndGrabFormats_SearchesOnlyGivenFormats verifies the sweep's format
// narrowing actually reaches the indexer search, not just the queue entry.
func TestSearchAndGrabFormats_SearchesOnlyGivenFormats(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{searcher: ss}

	book := models.Book{Title: "The Martian", MediaType: models.MediaTypeBoth}
	sched.searchAndGrabFormats(context.Background(), book, []string{models.MediaTypeAudiobook})

	if n := int(ss.calls.Load()); n != 1 {
		t.Fatalf("expected 1 search call, got %d", n)
	}
	if ss.mediaTypes[0] != models.MediaTypeAudiobook {
		t.Errorf("searched mediaType: want %q, got %q", models.MediaTypeAudiobook, ss.mediaTypes[0])
	}
}

// TestDownloadMediaType covers the resolver's three answers directly, including
// the both-kinds title that has to stay undetermined.
func TestDownloadMediaType(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		quality string
		want    string
	}{
		{"ebook from quality", "Some Book", "epub", models.MediaTypeEbook},
		{"audiobook from quality", "Some Book", "m4b", models.MediaTypeAudiobook},
		{"ebook from title", "Some Book EPUB", "", models.MediaTypeEbook},
		{"audiobook from title", "Some Book M4B", "", models.MediaTypeAudiobook},
		{"nothing recognisable", "Some Book", "", ""},
		{"both kinds stays undetermined", "Some Book M4B + PDF booklet", "pdf", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := downloadMediaType(&models.Download{Title: tc.title, Quality: tc.quality})
			if got != tc.want {
				t.Errorf("downloadMediaType(%q, %q) = %q, want %q", tc.title, tc.quality, got, tc.want)
			}
		})
	}
	if got := downloadMediaType(nil); got != "" {
		t.Errorf("downloadMediaType(nil) = %q, want empty", got)
	}
}
