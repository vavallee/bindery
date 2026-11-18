package audiblesynclistsyncer

import (
	"context"
	"errors"
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
		func(context.Context) (WishlistClient, error) { return f.client, nil }, f.resolver).
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
	b.Status = models.BookStatusImported
	if err := f.books.Update(ctx, b); err != nil {
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
	b.Status = models.BookStatusImported
	_ = f.books.Update(ctx, b)
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
	f.s.clients = func(context.Context) (WishlistClient, error) { return nil, ErrNotConfigured }
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
