package audiblesynclistsyncer

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/audiblesync"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestMain(m *testing.M) {
	searchPace = time.Millisecond
	m.Run()
}

type fakeClient struct {
	mu      sync.Mutex
	items   []audiblesync.WishlistItem
	err     error
	removed []string
	asked   []int
	// library is the account's library; Library filters it by status.
	library []audiblesync.LibraryBook
	// libraryReads counts whole-library walks, lookups single-book reads.
	libraryReads, lookups int
	// hideOnLookup makes a single-book read of these ids find nothing.
	hideOnLookup map[int]bool
	// catalog answers catalogue lookups by ASIN; catalogAsked records them
	// with the account they went through.
	catalog      map[string]audiblesync.CatalogBook
	catalogAsked []string
}

func (f *fakeClient) CatalogBook(_ context.Context, asin string, accountID int) (*audiblesync.CatalogBook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalogAsked = append(f.catalogAsked, asin+"@"+strconv.Itoa(accountID))
	if b, ok := f.catalog[asin]; ok {
		return &b, nil
	}
	return nil, audiblesync.ErrNotFound
}

func (f *fakeClient) LibraryBook(_ context.Context, id int) (*audiblesync.LibraryBook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.hideOnLookup[id] {
		return nil, audiblesync.ErrNotFound
	}
	for _, b := range f.library {
		if b.ID == id {
			return &b, nil
		}
	}
	return nil, audiblesync.ErrNotFound
}

func (f *fakeClient) Library(_ context.Context, _ int, status string) ([]audiblesync.LibraryBook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if status == "" {
		f.libraryReads++
	}
	var out []audiblesync.LibraryBook
	for _, b := range f.library {
		if status == "" || b.Status == status {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeClient) Wishlist(_ context.Context, accountID int) ([]audiblesync.WishlistItem, []audiblesync.WishlistAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, accountID)
	return f.items, []audiblesync.WishlistAccount{{AccountID: 1}}, f.err
}

func (f *fakeClient) RemoveFromWishlist(_ context.Context, accountID int, asin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, asin)
	if accountID == 0 {
		return errors.New("an account is required")
	}
	return nil
}

// fakeResolver canonicalises ASINs in books and resolves authors in authors.
type fakeResolver struct {
	books   map[string]*models.Book
	authors map[string]*models.Author
}

func (r *fakeResolver) GetCanonicalBookByASIN(_ context.Context, asin string) (*models.Book, error) {
	return r.books[asin], nil
}

func (r *fakeResolver) ResolveCanonicalAuthor(_ context.Context, name string) (*models.Author, error) {
	return r.authors[name], nil
}

type recordingSearcher struct {
	mu    sync.Mutex
	books []models.Book
}

func (r *recordingSearcher) SearchAndGrabBook(_ context.Context, b models.Book) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.books = append(r.books, b)
}

func (r *recordingSearcher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.books)
}

type fixture struct {
	s        *ListSyncer
	lists    *db.ImportListRepo
	books    *db.BookRepo
	authors  *db.AuthorRepo
	client   *fakeClient
	resolver *fakeResolver
	searcher *recordingSearcher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &fixture{
		lists:    db.NewImportListRepo(database),
		books:    db.NewBookRepo(database),
		authors:  db.NewAuthorRepo(database),
		client:   &fakeClient{},
		resolver: &fakeResolver{books: map[string]*models.Book{}, authors: map[string]*models.Author{}},
		searcher: &recordingSearcher{},
	}
	f.s = New(f.lists, f.authors, f.books,
		func(context.Context) (Client, error) { return f.client, nil }, f.resolver).
		WithSearcher(f.searcher)
	return f
}

func (f *fixture) list(t *testing.T, mutate func(*models.ImportList)) models.ImportList {
	t.Helper()
	il := models.ImportList{Name: "Audible wishlist", Type: models.ImportListTypeAudibleSync, Enabled: true,
		MonitorNew: true, AutoAdd: true, SkipOwned: true, Account: "1"}
	if mutate != nil {
		mutate(&il)
	}
	if err := f.lists.Create(context.Background(), &il); err != nil {
		t.Fatal(err)
	}
	return il
}

func item(asin, title, author string) audiblesync.WishlistItem {
	secs := 36000
	return audiblesync.WishlistItem{
		CatalogBook: audiblesync.CatalogBook{
			ASIN: asin, Title: title, Authors: []audiblesync.CatalogAuthor{{Name: author}},
			Narrators: []string{"A Narrator"}, LengthSeconds: &secs, Language: "en",
			CoverURL: "https://m.media-amazon.com/x.jpg",
		},
		AccountID: 1,
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSyncCreatesCanonicalAndAudibleOnlyBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.client.items = []audiblesync.WishlistItem{
		item("B000000001", "Ancillary Justice", "Ann Leckie"),
		item("B000000002", "An Audible Original", "Ann Leckie"),
	}
	// The first ASIN matches a primary-provider work; the second does not,
	// but its author does.
	f.resolver.books["B000000001"] = &models.Book{
		ForeignID: "OL1W", Title: "Ancillary Justice", SortTitle: "Ancillary Justice", MetadataProvider: "openlibrary",
		Author: &models.Author{ForeignID: "OL1A", Name: "Ann Leckie", MetadataProvider: "openlibrary"},
	}
	f.resolver.authors["Ann Leckie"] = &models.Author{ForeignID: "OL1A", Name: "Ann Leckie", MetadataProvider: "openlibrary"}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	st := f.s.Progress().Stats
	if st.Imported != 2 || st.Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}

	canonical, _ := f.books.GetByForeignID(ctx, "OL1W")
	if canonical == nil || canonical.ASIN != "B000000001" || canonical.MediaType != models.MediaTypeAudiobook ||
		canonical.Narrator != "A Narrator" || canonical.DurationSeconds != 36000 || !canonical.Monitored ||
		canonical.Status != models.BookStatusWanted {
		t.Fatalf("canonical book = %+v", canonical)
	}
	original, _ := f.books.GetByForeignID(ctx, "audible:B000000002")
	if original == nil || original.AuthorID != canonical.AuthorID {
		t.Fatalf("Audible-only book = %+v; it must sit under the same, resolved author", original)
	}
	author, _ := f.authors.GetByID(ctx, canonical.AuthorID)
	if author == nil || author.MonitorMode != models.AuthorMonitorModeNone || author.MonitorNewItems != models.AuthorMonitorNewItemsNone {
		t.Fatalf("author = %+v; a list-created author must not want its back catalogue", author)
	}
	waitFor(t, func() bool { return f.searcher.count() == 2 })
	if len(f.client.asked) != 1 || f.client.asked[0] != 1 {
		t.Fatalf("account asked = %v, want [1]", f.client.asked)
	}
}

func TestSyncSkipsOwnedUnresolvedAndExcluded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owned := item("B000000003", "Owned", "Known Author")
	owned.InLibrary = true
	f.client.items = []audiblesync.WishlistItem{
		owned,
		item("B000000004", "Unknown Author Book", "Nobody Knows"),
		item("B000000005", "Excluded", "Known Author"),
	}
	f.resolver.authors["Known Author"] = &models.Author{ForeignID: "OL2A", Name: "Known Author"}
	if err := f.lists.CreateExclusion(ctx, &models.ImportListExclusion{ForeignID: "audible:B000000005", Title: "Excluded"}); err != nil {
		t.Fatal(err)
	}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	st := f.s.Progress().Stats
	if st.Owned != 1 || st.Unresolved != 1 || st.Skipped != 1 || st.Imported != 0 {
		t.Fatalf("stats = %+v", st)
	}

	// With skip owned off, the owned book is added.
	il.SkipOwned = false
	if err := f.lists.Update(ctx, &il); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.books.GetByForeignID(ctx, "audible:B000000003"); b == nil {
		t.Fatal("an owned book must be added when the list does not skip owned books")
	}
}

func TestSyncWidensTrackedEbookAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL3A", Name: "Ebook Author", SortName: "Author, Ebook", MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ebook := &models.Book{ForeignID: "OL3W", AuthorID: author.ID, Title: "Paper Book", SortTitle: "Paper Book",
		Status: models.BookStatusImported, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, ebook); err != nil {
		t.Fatal(err)
	}
	f.resolver.books["B000000006"] = &models.Book{ForeignID: "OL3W", Title: "Paper Book",
		Author: &models.Author{ForeignID: "OL3A", Name: "Ebook Author"}}
	f.client.items = []audiblesync.WishlistItem{item("B000000006", "Paper Book", "Ebook Author")}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.books.GetByID(ctx, ebook.ID)
	if got.MediaType != models.MediaTypeBoth || got.ASIN != "B000000006" {
		t.Fatalf("tracked ebook = media %q asin %q; want both and the ASIN", got.MediaType, got.ASIN)
	}

	// A second pass changes nothing and creates nothing.
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if st := f.s.Progress().Stats; st.Imported != 0 || st.Skipped != 1 {
		t.Fatalf("second pass stats = %+v", st)
	}
}

func TestRemoveOnImport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Done Author"] = &models.Author{ForeignID: "OL4A", Name: "Done Author"}
	f.client.items = []audiblesync.WishlistItem{item("B000000007", "Finished", "Done Author")}
	il := f.list(t, func(il *models.ImportList) { il.RemoveOnImport = true })

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.client.removed) != 0 {
		t.Fatalf("a wanted book must stay on the wishlist: removed %v", f.client.removed)
	}

	b, _ := f.books.GetByForeignID(ctx, "audible:B000000007")
	if err := f.books.AddBookFile(ctx, b.ID, models.MediaTypeAudiobook, "/lib/Finished.m4b"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.client.removed) != 1 || f.client.removed[0] != "B000000007" || f.s.Progress().Stats.Removed != 1 {
		t.Fatalf("removed = %v, stats %+v", f.client.removed, f.s.Progress().Stats)
	}
}

func TestRemoveOnImportWithDefaultAccountUsesItemAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Done Author"] = &models.Author{ForeignID: "OL5A", Name: "Done Author"}
	f.client.items = []audiblesync.WishlistItem{item("B000000008", "Finished", "Done Author")}
	il := f.list(t, func(il *models.ImportList) { il.RemoveOnImport = true; il.Account = "" })
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := f.books.GetByForeignID(ctx, "audible:B000000008")
	if err := f.books.AddBookFile(ctx, b.ID, models.MediaTypeAudiobook, "/lib/Finished 8.m4b"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	// fakeClient refuses account 0, so a removal only counts when the item's
	// own account was used.
	if st := f.s.Progress().Stats; st.Removed != 1 || st.Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPreconditions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.s.StartOne(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown list: %v", err)
	}
	hc := models.ImportList{Name: "hc", Type: models.ImportListTypeHardcover, Enabled: true}
	_ = f.lists.Create(ctx, &hc)
	if err := f.s.StartOne(ctx, hc.ID); !errors.Is(err, ErrWrongType) {
		t.Errorf("hardcover list: %v", err)
	}
	off := f.list(t, func(il *models.ImportList) { il.Enabled = false })
	if err := f.s.StartOne(ctx, off.ID); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled list: %v", err)
	}

	on := f.list(t, nil)
	f.s.clients = func(context.Context) (Client, error) { return nil, ErrNotConfigured }
	if err := f.s.StartOne(ctx, on.ID); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured: %v", err)
	}
}

func TestWishlistReadFailureFailsThePass(t *testing.T) {
	f := newFixture(t)
	f.client.err = errors.New("audible sync down")
	il := f.list(t, nil)
	if err := f.s.SyncOne(context.Background(), il.ID); err == nil {
		t.Fatal("want an error")
	}
	p := f.s.Progress()
	if p.Running || p.Error == "" {
		t.Fatalf("progress = %+v", p)
	}
}

func TestStartOneRunsInBackground(t *testing.T) {
	f := newFixture(t)
	f.resolver.authors["Bg Author"] = &models.Author{ForeignID: "OL6A", Name: "Bg Author"}
	f.client.items = []audiblesync.WishlistItem{item("B000000009", "Background", "Bg Author")}
	il := f.list(t, nil)
	if err := f.s.StartOne(context.Background(), il.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { p := f.s.Progress(); return !p.Running && p.FinishedAt != nil })
	if p := f.s.Progress(); p.Stats.Imported != 1 || p.Trigger != TriggerManual {
		t.Fatalf("progress = %+v", p)
	}
}

// libBook is a library book of account 1, its Audible Sync id taken from the
// ASIN's last two characters so each test book has its own.
func libBook(asin, title, author, status string) audiblesync.LibraryBook {
	id, _ := strconv.ParseInt(asin[len(asin)-2:], 16, 64)
	return audiblesync.LibraryBook{ID: int(id) + 1, ASIN: asin, Title: title, Authors: []string{author}, Narrators: []string{"N"},
		Length: 600, Status: status, AccountID: 1}
}

func TestUnavailableListAddsWithdrawnBooksAndReleasesReturnedOnes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Plus Author"] = &models.Author{ForeignID: "OL7A", Name: "Plus Author"}
	f.client.library = []audiblesync.LibraryBook{
		libBook("B00000000A", "Withdrawn", "Plus Author", audiblesync.LibraryStatusUnavailable),
		libBook("B00000000B", "Still Fine", "Plus Author", audiblesync.LibraryStatusDownloaded),
	}
	il := f.list(t, func(il *models.ImportList) {
		il.URL = models.ImportListSourceUnavailable
		il.SkipOwned, il.RemoveOnImport = true, true // ignored for this source
	})

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if st := f.s.Progress().Stats; st.Imported != 1 || st.Owned != 0 || st.Total != 1 {
		t.Fatalf("first pass stats = %+v; owned books must not be skipped on this list", st)
	}
	b, _ := f.books.GetByForeignID(ctx, "audible:B00000000A")
	if b == nil || !b.Monitored || b.DurationSeconds != 36000 {
		t.Fatalf("withdrawn book = %+v; want monitored, 600 minutes as seconds", b)
	}
	if len(f.client.asked) != 0 || len(f.client.removed) != 0 {
		t.Fatalf("an unavailable list must not touch the wishlist: asked %v removed %v", f.client.asked, f.client.removed)
	}

	// Audible brings the title back and Audible Sync starts downloading it.
	f.client.library[0].Status = audiblesync.LibraryStatusWaiting
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if st := f.s.Progress().Stats; st.Released != 1 {
		t.Fatalf("second pass stats = %+v, want one released", st)
	}
	b, _ = f.books.GetByID(ctx, b.ID)
	if b.Monitored || b.Status != models.BookStatusWanted {
		t.Fatalf("returned book = monitored %v status %q; want unmonitored and still in the library", b.Monitored, b.Status)
	}
	if recs, _ := f.lists.ListBooks(ctx, il.ID); len(recs) != 0 {
		t.Fatalf("a released book must be forgotten, still recorded: %v", recs)
	}
}

func TestUnavailableListOnlyReleasesItsOwnBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A book the operator wanted through another path, not created by the list.
	author := &models.Author{ForeignID: "OL8A", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary", Monitored: true}
	_ = f.authors.Create(ctx, author)
	mine := &models.Book{ForeignID: "OL8W", AuthorID: author.ID, Title: "Wanted Elsewhere", SortTitle: "Wanted Elsewhere",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeAudiobook, ASIN: "B00000000C",
		Genres: []string{}, MetadataProvider: "openlibrary"}
	_ = f.books.Create(ctx, mine)
	f.client.library = []audiblesync.LibraryBook{libBook("B00000000C", "Wanted Elsewhere", "Other", audiblesync.LibraryStatusUnavailable)}
	il := f.list(t, func(il *models.ImportList) { il.URL = models.ImportListSourceUnavailable })
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	f.client.library[0].Status = audiblesync.LibraryStatusDownloaded
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.books.GetByID(ctx, mine.ID)
	if !got.Monitored {
		t.Fatal("a book the list did not create must not be unmonitored")
	}
}

func TestReturnedBookAlreadyImportedIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Plus Author"] = &models.Author{ForeignID: "OL9A", Name: "Plus Author"}
	f.client.library = []audiblesync.LibraryBook{libBook("B00000000D", "Got It", "Plus Author", audiblesync.LibraryStatusUnavailable)}
	il := f.list(t, func(il *models.ImportList) { il.URL = models.ImportListSourceUnavailable })
	_ = f.s.SyncOne(ctx, il.ID)
	b, _ := f.books.GetByForeignID(ctx, "audible:B00000000D")
	b.Status = models.BookStatusImported
	_ = f.books.Update(ctx, b)
	f.client.library[0].Status = audiblesync.LibraryStatusDownloaded
	_ = f.s.SyncOne(ctx, il.ID)
	got, _ := f.books.GetByID(ctx, b.ID)
	if !got.Monitored || f.s.Progress().Stats.Released != 0 {
		t.Fatalf("imported book = monitored %v, released %d; want it untouched", got.Monitored, f.s.Progress().Stats.Released)
	}
}

func TestWishlistListRecordsWhatItCreates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Rec Author"] = &models.Author{ForeignID: "OL10A", Name: "Rec Author"}
	f.client.items = []audiblesync.WishlistItem{item("B00000000E", "Recorded", "Rec Author")}
	il := f.list(t, nil)
	_ = f.s.SyncOne(ctx, il.ID)
	if recs, _ := f.lists.ListBooks(ctx, il.ID); len(recs) != 1 || recs[0].ASIN != "B00000000E" {
		t.Fatalf("records = %v", recs)
	}
}

// An ebook Bindery imported is still wanted as an audiobook, which is why it
// is on the wishlist: it must not be taken off it.
func TestRemoveOnImportKeepsAnImportedEbook(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL11A", Name: "Paper Author", SortName: "Author, Paper", MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ebook := &models.Book{ForeignID: "OL11W", AuthorID: author.ID, Title: "Paper Only", SortTitle: "Paper Only", ASIN: "B00000000F",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, ebook); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, ebook.ID, models.MediaTypeEbook, "/lib/Paper Only.epub"); err != nil {
		t.Fatal(err)
	}
	f.client.items = []audiblesync.WishlistItem{item("B00000000F", "Paper Only", "Paper Author")}
	il := f.list(t, func(il *models.ImportList) { il.RemoveOnImport = true })

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.client.removed) != 0 {
		t.Fatalf("an imported ebook must stay on the wishlist: removed %v", f.client.removed)
	}
	got, _ := f.books.GetByID(ctx, ebook.ID)
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("media = %q; the ebook must now want its audiobook", got.MediaType)
	}
}

// A tracked ebook the Audible account already owns is not widened: Audible
// Sync downloads the audiobook, and a search would fetch a second copy.
func TestSkipOwnedAppliesToTrackedBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL12A", Name: "Owned Author", SortName: "Author, Owned", MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ebook := &models.Book{ForeignID: "OL12W", AuthorID: author.ID, Title: "Owned Twice", SortTitle: "Owned Twice", ASIN: "B000000010",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, ebook); err != nil {
		t.Fatal(err)
	}
	owned := item("B000000010", "Owned Twice", "Owned Author")
	owned.InLibrary = true
	owned.Library = []audiblesync.LibraryCopy{{AccountID: 1, BookID: 3, Status: audiblesync.LibraryStatusDownloading}}
	f.client.items = []audiblesync.WishlistItem{owned}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.books.GetByID(ctx, ebook.ID)
	if got.MediaType != models.MediaTypeEbook || f.s.Progress().Stats.Owned != 1 {
		t.Fatalf("media = %q, stats %+v; an owned book must be left as it is", got.MediaType, f.s.Progress().Stats)
	}
}

// Owned means owned by the list's own account. A book only another account
// on the same Audible Sync owns is added.
func TestSkipOwnedIsPerAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["House Author"] = &models.Author{ForeignID: "OL13A", Name: "House Author"}
	theirs := item("B000000011", "Theirs", "House Author")
	theirs.InLibrary = true
	theirs.Library = []audiblesync.LibraryCopy{{AccountID: 2, BookID: 5}}
	f.client.items = []audiblesync.WishlistItem{theirs}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.books.GetByForeignID(ctx, "audible:B000000011"); b == nil {
		t.Fatalf("a book only another account owns must be added; stats %+v", f.s.Progress().Stats)
	}
}

// Books that left the unavailable list are each looked up by id; the whole
// library is never read. One gone from the library is forgotten but still
// searched for; one that failed in Audible Sync is kept and asked about again.
func TestReturnedBooksAreLookedUpOneByOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Plus Author"] = &models.Author{ForeignID: "OL14A", Name: "Plus Author"}
	f.client.library = []audiblesync.LibraryBook{
		libBook("B000000012", "Refunded", "Plus Author", audiblesync.LibraryStatusUnavailable),
		libBook("B000000013", "Failing", "Plus Author", audiblesync.LibraryStatusUnavailable),
	}
	il := f.list(t, func(il *models.ImportList) { il.URL = models.ImportListSourceUnavailable })
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if recs, _ := f.lists.ListBooks(ctx, il.ID); len(recs) != 2 || recs[0].SourceID == 0 {
		t.Fatalf("records = %+v; want both, with their Audible Sync ids", recs)
	}

	// The first is refunded and leaves the library; the second fails. Only
	// the lookups of this pass count, not the first pass's description reads.
	f.client.lookups = 0
	f.client.library = []audiblesync.LibraryBook{f.client.library[1]}
	f.client.library[0].Status = audiblesync.LibraryStatusFailed
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if f.client.libraryReads != 0 || f.client.lookups != 2 {
		t.Fatalf("library walks %d, lookups %d; want none and 2", f.client.libraryReads, f.client.lookups)
	}
	recs, _ := f.lists.ListBooks(ctx, il.ID)
	if len(recs) != 1 || recs[0].ASIN != "B000000013" {
		t.Fatalf("records = %+v; want only the failing book kept", recs)
	}
	for _, asin := range []string{"B000000012", "B000000013"} {
		if b, _ := f.books.GetByForeignID(ctx, "audible:"+asin); b == nil || !b.Monitored {
			t.Fatalf("%s = %+v; want still monitored", asin, b)
		}
	}
}

// A book added from the library takes its description from Audible Sync's
// copy, one lookup per added book; a book the primary provider describes
// keeps its own, and a failed lookup still adds the book.
func TestLibraryBooksTakeTheirDescriptionFromAudibleSync(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Plus Author"] = &models.Author{ForeignID: "OL15A", Name: "Plus Author"}
	described := libBook("B000000014", "Described", "Plus Author", audiblesync.LibraryStatusUnavailable)
	described.Description = "  A withdrawn title, as Audible describes it.  "
	missing := libBook("B000000015", "Lookup Fails", "Plus Author", audiblesync.LibraryStatusUnavailable)
	f.resolver.books["B000000016"] = &models.Book{ForeignID: "OL16W", Title: "Has One", Description: "From the provider.",
		Author: &models.Author{ForeignID: "OL15A", Name: "Plus Author"}}
	provided := libBook("B000000016", "Has One", "Plus Author", audiblesync.LibraryStatusUnavailable)
	provided.Description = "From Audible."
	f.client.library = []audiblesync.LibraryBook{described, missing, provided}
	il := f.list(t, func(il *models.ImportList) { il.URL = models.ImportListSourceUnavailable })

	// The fake answers a lookup from its library, so drop the second book
	// from it after the list is read: its lookup then finds nothing.
	f.client.hideOnLookup = map[int]bool{missing.ID: true}
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"audible:B000000014": "A withdrawn title, as Audible describes it.",
		"audible:B000000015": "",
		"OL16W":              "From the provider.",
	}
	for id, desc := range want {
		b, _ := f.books.GetByForeignID(ctx, id)
		if b == nil || b.Description != desc {
			t.Fatalf("%s = %+v; want description %q", id, b, desc)
		}
	}
	if f.client.lookups != 2 {
		t.Fatalf("lookups = %d; want one per added book without a description", f.client.lookups)
	}
}

// A wishlist book takes the description the wishlist item carries; without
// one it is looked up in the catalogue through the list's account, and a
// failed lookup still adds the book.
func TestWishlistBooksTakeTheirDescription(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.resolver.authors["Wish Author"] = &models.Author{ForeignID: "OL17A", Name: "Wish Author"}
	carried := item("B000000017", "Carried", "Wish Author")
	carried.Description = "Stored with the wishlist."
	f.client.items = []audiblesync.WishlistItem{
		carried,
		item("B000000018", "Looked Up", "Wish Author"),
		item("B000000019", "Unknown To Audible", "Wish Author"),
	}
	f.client.catalog = map[string]audiblesync.CatalogBook{"B000000018": {ASIN: "B000000018", Description: " From the catalogue. "}}
	il := f.list(t, nil)

	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"audible:B000000017": "Stored with the wishlist.",
		"audible:B000000018": "From the catalogue.",
		"audible:B000000019": "",
	}
	for id, desc := range want {
		b, _ := f.books.GetByForeignID(ctx, id)
		if b == nil || b.Description != desc {
			t.Fatalf("%s = %+v; want description %q", id, b, desc)
		}
	}
	if got := strings.Join(f.client.catalogAsked, ","); got != "B000000018@1,B000000019@1" {
		t.Fatalf("catalogue lookups = %s; want only the two without one, through account 1", got)
	}

	// Tracked books are not looked up again on a later sync.
	f.client.catalogAsked = nil
	if err := f.s.SyncOne(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.client.catalogAsked) != 0 {
		t.Fatalf("second pass looked up %v", f.client.catalogAsked)
	}
}
