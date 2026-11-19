package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// callCountingProvider counts every lookup that reaches the provider, so a
// test can show a hand-made book was added without asking anyone (#3840).
type callCountingProvider struct {
	*stubMetaProvider
	calls atomic.Int32
}

func (p *callCountingProvider) SearchAuthors(ctx context.Context, q string) ([]models.Author, error) {
	p.calls.Add(1)
	return p.stubMetaProvider.SearchAuthors(ctx, q)
}

func (p *callCountingProvider) SearchBooks(ctx context.Context, q string) ([]models.Book, error) {
	p.calls.Add(1)
	return p.stubMetaProvider.SearchBooks(ctx, q)
}

func (p *callCountingProvider) GetAuthor(ctx context.Context, id string) (*models.Author, error) {
	p.calls.Add(1)
	return p.stubMetaProvider.GetAuthor(ctx, id)
}

func (p *callCountingProvider) GetBook(ctx context.Context, id string) (*models.Book, error) {
	p.calls.Add(1)
	return p.stubMetaProvider.GetBook(ctx, id)
}

func (p *callCountingProvider) GetAuthorWorks(ctx context.Context, id string) ([]models.Book, error) {
	p.calls.Add(1)
	return p.stubMetaProvider.GetAuthorWorks(ctx, id)
}

// fixedFinder reports path as the library file for every title.
type fixedFinder struct{ path string }

func (f fixedFinder) FindExisting(context.Context, string, string, string) string { return f.path }

type manualBookFixture struct {
	h        *AuthorHandler
	authors  *db.AuthorRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	provider *callCountingProvider
	searcher *searcherSpy
}

func newManualBookFixture(t *testing.T) manualBookFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	provider := &callCountingProvider{stubMetaProvider: &stubMetaProvider{name: "openlibrary"}}
	searcher := &searcherSpy{}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo,
		metadata.NewAggregator(provider), db.NewSettingsRepo(database), db.NewMetadataProfileRepo(database), searcher)
	return manualBookFixture{h: h, authors: authorRepo, books: bookRepo, series: seriesRepo, provider: provider, searcher: searcher}
}

func (f manualBookFixture) post(t *testing.T, ctx context.Context, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book/manual", bytes.NewReader(raw)).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.h.AddManualBook(rec, req)
	return rec
}

func decodeManualBook(t *testing.T, rec *httptest.ResponseRecorder) models.Book {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var book models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	return book
}

func seedLibraryAuthor(t *testing.T, authors *db.AuthorRepo, foreignID, name string) *models.Author {
	t.Helper()
	author := &models.Author{
		ForeignID: foreignID, Name: name, SortName: sortName(name),
		MetadataProvider: models.AuthorProviderFromForeignID(foreignID), Monitored: true,
	}
	if err := authors.Create(context.Background(), author); err != nil {
		t.Fatal(err)
	}
	return author
}

// The issue's case: a short story nobody has metadata for, by an author not in
// the library. The book and a hand-made author are created as typed, and no
// provider is asked anything.
func TestAddManualBook_CreatesBookAndAuthorWithoutAskingAProvider(t *testing.T) {
	f := newManualBookFixture(t)
	ctx := context.Background()

	book := decodeManualBook(t, f.post(t, ctx, map[string]any{
		"title": "  The Last Church  ", "authorName": "Graham McNeill", "mediaType": "audiobook",
	}))

	if !strings.HasPrefix(book.ForeignID, models.ManualBookForeignIDPrefix) {
		t.Errorf("foreign id = %q, want a %q id", book.ForeignID, models.ManualBookForeignIDPrefix)
	}
	if book.Title != "The Last Church" || book.SortTitle != "The Last Church" {
		t.Errorf("title = %q, sort title = %q, want the trimmed title", book.Title, book.SortTitle)
	}
	if book.MetadataProvider != models.ManualMetadataProvider {
		t.Errorf("metadata provider = %q, want %q", book.MetadataProvider, models.ManualMetadataProvider)
	}
	if book.Status != models.BookStatusWanted || !book.Monitored || book.MediaType != models.MediaTypeAudiobook {
		t.Errorf("status %q monitored %v media type %q, want a monitored wanted audiobook", book.Status, book.Monitored, book.MediaType)
	}
	if n := f.provider.calls.Load(); n != 0 {
		t.Errorf("the add asked the metadata provider %d times, want none", n)
	}

	author, err := f.authors.GetByID(ctx, book.AuthorID)
	if err != nil || author == nil {
		t.Fatalf("author %d: %v, %v", book.AuthorID, author, err)
	}
	if author.ForeignID != "manual:author:graham-mcneill" || author.Name != "Graham McNeill" ||
		author.MetadataProvider != models.ManualMetadataProvider || author.Monitored {
		t.Errorf("author = %+v, want an unmonitored hand-made Graham McNeill", author)
	}

	// The same spelling again reuses the author instead of making a second one.
	second := decodeManualBook(t, f.post(t, ctx, map[string]any{"title": "Fulgrim", "authorName": "graham mcneill"}))
	if second.AuthorID != author.ID {
		t.Errorf("second book filed under author %d, want %d", second.AuthorID, author.ID)
	}
	if second.MediaType != models.MediaTypeEbook {
		t.Errorf("media type = %q, want the default ebook", second.MediaType)
	}
	all, err := f.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("%d authors, want 1", len(all))
	}
}

func TestAddManualBook_ReusesLibraryAuthor(t *testing.T) {
	f := newManualBookFixture(t)
	ctx := context.Background()
	abnett := seedLibraryAuthor(t, f.authors, "OL1234567A", "Dan Abnett")

	byName := decodeManualBook(t, f.post(t, ctx, map[string]any{"title": "Blood Games", "authorName": "dan abnett"}))
	if byName.AuthorID != abnett.ID {
		t.Errorf("by name: filed under author %d, want the library author %d", byName.AuthorID, abnett.ID)
	}
	byID := decodeManualBook(t, f.post(t, ctx, map[string]any{"title": "Lone Wolves", "authorId": abnett.ID}))
	if byID.AuthorID != abnett.ID {
		t.Errorf("by id: filed under author %d, want %d", byID.AuthorID, abnett.ID)
	}

	rec := f.post(t, ctx, map[string]any{"title": "Lone Wolves", "authorId": abnett.ID + 100})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown authorId: status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if n := f.provider.calls.Load(); n != 0 {
		t.Errorf("the adds asked the metadata provider %d times, want none", n)
	}
}

// A title the author already has answers with that row, as Add Book does
// (#1227), whether the row is hand-made or a provider book. A different
// position in the same series is a different book.
func TestAddManualBook_DuplicateTitleAnswersWithExistingBook(t *testing.T) {
	f := newManualBookFixture(t)
	ctx := context.Background()
	author := seedLibraryAuthor(t, f.authors, "OL1A", "Aaron Dembski-Bowden")
	provided := &models.Book{
		ForeignID: "OL9W", AuthorID: author.ID, Title: "The First Heretic", SortTitle: "The First Heretic",
		Status: models.BookStatusWanted, Genres: []string{}, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := f.books.Create(ctx, provided); err != nil {
		t.Fatal(err)
	}

	rec := f.post(t, ctx, map[string]any{"title": "the first heretic", "authorId": author.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		ExistingBookID int64 `json:"existingBookId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.ExistingBookID != provided.ID {
		t.Errorf("existingBookId = %d, want %d", conflict.ExistingBookID, provided.ID)
	}

	first := decodeManualBook(t, f.post(t, ctx, map[string]any{
		"title": "Tales", "authorId": author.ID, "seriesTitle": "Anthology", "seriesPosition": "1",
	}))
	if rec := f.post(t, ctx, map[string]any{
		"title": "Tales", "authorId": author.ID, "seriesTitle": "Anthology", "seriesPosition": "1",
	}); rec.Code != http.StatusConflict {
		t.Errorf("same title and position: status %d, want 409", rec.Code)
	}
	second := decodeManualBook(t, f.post(t, ctx, map[string]any{
		"title": "Tales", "authorId": author.ID, "seriesTitle": "Anthology", "seriesPosition": "2",
	}))
	if second.ID == first.ID {
		t.Error("a different position in the same series reused the first book")
	}
}

// The series joins the one the other books are in: first the author's own,
// then any series of that title in the library, so a story added to "The Horus
// Heresy" by an author with no other book there does not start a second one.
func TestAddManualBook_LinksSeries(t *testing.T) {
	f := newManualBookFixture(t)
	ctx := context.Background()

	first := decodeManualBook(t, f.post(t, ctx, map[string]any{
		"title": "The Last Church", "authorName": "Graham McNeill", "seriesTitle": "The Horus Heresy", "seriesPosition": "9.5",
	}))
	title, position, err := f.series.GetPrimarySeriesForBook(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if title != "The Horus Heresy" || position != "9.5" {
		t.Fatalf("primary series = %q #%q, want The Horus Heresy #9.5", title, position)
	}
	all, err := f.series.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !strings.HasPrefix(all[0].ForeignID, "manual:series:") {
		t.Fatalf("series = %+v, want one hand-made series", all)
	}

	other := decodeManualBook(t, f.post(t, ctx, map[string]any{
		"title": "The Lightning Tower", "authorName": "Dan Abnett", "seriesTitle": "the horus heresy", "seriesPosition": "7.5",
	}))
	ids, err := f.series.GetSeriesIDsForBook(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != all[0].ID {
		t.Errorf("another author's story joined series %v, want [%d]", ids, all[0].ID)
	}
	if after, _ := f.series.List(ctx); len(after) != 1 {
		t.Errorf("%d series after the second story, want 1", len(after))
	}
}

func TestAddManualBook_RejectsBadRequests(t *testing.T) {
	f := newManualBookFixture(t)
	long := strings.Repeat("x", manualBookTextMax+1)
	cases := map[string]map[string]any{
		"no title":              {"authorName": "A"},
		"blank title":           {"title": "   ", "authorName": "A"},
		"no author":             {"title": "T"},
		"negative author id":    {"title": "T", "authorId": -1},
		"punctuation author":    {"title": "T", "authorName": "?!"},
		"position no series":    {"title": "T", "authorName": "A", "seriesPosition": "1"},
		"bad media type":        {"title": "T", "authorName": "A", "mediaType": "comic"},
		"title too long":        {"title": long, "authorName": "A"},
		"author too long":       {"title": "T", "authorName": long},
		"series too long":       {"title": "T", "authorName": "A", "seriesTitle": long},
		"position too long":     {"title": "T", "authorName": "A", "seriesTitle": "S", "seriesPosition": long},
		"title is not a string": {"title": 7, "authorName": "A"},
	}
	for name, body := range cases {
		if rec := f.post(t, context.Background(), body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
	books, err := f.books.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.authors.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 || len(all) != 0 {
		t.Errorf("rejected requests left %d books and %d authors behind", len(books), len(all))
	}
}

func TestAddManualBook_SearchOnAdd(t *testing.T) {
	f := newManualBookFixture(t)
	ctx := context.Background()

	decodeManualBook(t, f.post(t, ctx, map[string]any{"title": "Not Searched", "authorName": "A. Writer"}))
	decodeManualBook(t, f.post(t, ctx, map[string]any{"title": "Searched", "authorName": "A. Writer", "searchOnAdd": true}))

	deadline := time.Now().Add(5 * time.Second)
	for len(f.searcher.titles()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.searcher.titles(); len(got) != 1 || got[0] != "Searched" {
		t.Errorf("searched %v, want only [Searched]", got)
	}
}

// A file the library already holds is bound to the new book, and search on
// add then stays off, so Bindery does not download a book the user has.
func TestAddManualBook_BindsLibraryFileInsteadOfSearching(t *testing.T) {
	f := newManualBookFixture(t)
	f.h.WithFinder(fixedFinder{path: "/library/Graham McNeill/The Last Church.epub"})

	book := decodeManualBook(t, f.post(t, context.Background(), map[string]any{
		"title": "The Last Church", "authorName": "Graham McNeill", "searchOnAdd": true,
	}))
	if book.FilePath != "/library/Graham McNeill/The Last Church.epub" {
		t.Errorf("file path = %q, want the library file", book.FilePath)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.searcher.titles(); len(got) != 0 {
		t.Errorf("searched %v for a book whose file is already in the library", got)
	}
}

// Under tenancy a user never files a book under another user's author, by id
// or by name, and the book is theirs.
func TestAddManualBook_TenancyScopesAuthor(t *testing.T) {
	f := newTenancyFixture(t, true, &stubMetaProvider{name: "openlibrary"})
	f.h.series = db.NewSeriesRepo(f.db)
	ctx := context.Background()
	bobs := &models.Author{ForeignID: "OL-BOB-A", Name: "Shared Name", SortName: "Name, Shared", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, bobs, f.bob); err != nil {
		t.Fatal(err)
	}

	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book/manual", bytes.NewReader(raw)).WithContext(f.userCtx(f.alice))
		rec := httptest.NewRecorder()
		f.h.AddManualBook(rec, req)
		return rec
	}

	if rec := post(map[string]any{"title": "T", "authorId": bobs.ID}); rec.Code != http.StatusNotFound {
		t.Errorf("another user's authorId: status %d, want 404", rec.Code)
	}
	rec := post(map[string]any{"title": "T", "authorName": "Shared Name"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var book models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	if book.AuthorID == bobs.ID {
		t.Fatal("alice's book was filed under bob's author")
	}
	// The owner is not serialised, so read it back from the row.
	stored, err := f.books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("book: %v, %v", stored, err)
	}
	if stored.OwnerUserID != f.alice {
		t.Errorf("book owner = %d, want alice %d", stored.OwnerUserID, f.alice)
	}
	author, err := f.authors.GetByID(ctx, book.AuthorID)
	if err != nil || author == nil {
		t.Fatalf("author: %v, %v", author, err)
	}
	if author.OwnerUserID != f.alice || !models.IsManualForeignID(author.ForeignID) {
		t.Errorf("author = %+v, want a hand-made author owned by alice", author)
	}
	if visible, _ := f.authors.GetByIDForUser(ctx, author.ID, f.bob); visible != nil {
		t.Error("bob can see alice's hand-made author")
	}
}

// A catalogue sync that lists a work with the same title as a book made by
// hand leaves that book alone: no provider ids recorded on it, no ratings or
// series written, and no second row created beside it (#3840).
func TestFetchAuthorBooks_LeavesManualBookAlone(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)

	author := &models.Author{
		ForeignID: "OL-HH-A", Name: "Graham McNeill", SortName: "McNeill, Graham",
		MetadataProvider: "openlibrary", Monitored: true,
		MonitorMode: models.AuthorMonitorModeAll, MonitorNewItems: models.AuthorMonitorNewItemsAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	manual := &models.Book{
		ForeignID: models.ManualBookForeignIDPrefix + "1", AuthorID: author.ID, Title: "The Last Church",
		SortTitle: "The Last Church", Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
		MediaType: models.MediaTypeEbook, MetadataProvider: models.ManualMetadataProvider,
	}
	if err := bookRepo.Create(ctx, manual); err != nil {
		t.Fatal(err)
	}

	works := []models.Book{
		{
			ForeignID: "OL-LASTCHURCH-W", HardcoverForeignID: "hc:the-last-church", Title: "The Last Church",
			SortTitle: "the last church", Language: "eng", MediaType: models.MediaTypeAudiobook,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
			RatingsCount: 40, AverageRating: 4.1,
			SeriesRefs: []models.SeriesRef{{ForeignID: "hc-series:1", Title: "Horus Heresy", Position: "9"}},
		},
		{
			ForeignID: "OL-FULGRIM-W", Title: "Fulgrim", SortTitle: "fulgrim", Language: "eng",
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo,
		metadata.NewAggregator(&stubMetaProvider{name: "openlibrary", works: works}), nil, db.NewMetadataProfileRepo(database), nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("books = %v, want the hand-made one and Fulgrim", bookTitles(books))
	}
	got, err := bookRepo.GetByID(ctx, manual.ID)
	if err != nil || got == nil {
		t.Fatalf("manual book: %v, %v", got, err)
	}
	if got.ForeignID != manual.ForeignID || got.MediaType != models.MediaTypeEbook || got.RatingsCount != 0 {
		t.Errorf("manual book was changed by the sync: %+v", got)
	}
	for _, id := range []string{"OL-LASTCHURCH-W", "hc:the-last-church"} {
		if owner, err := bookRepo.GetBookIdentifier(ctx, id); err != nil || owner != nil {
			t.Errorf("identifier %q recorded on %+v (err %v), want none", id, owner, err)
		}
	}
	if ids, err := seriesRepo.GetSeriesIDsForBook(ctx, manual.ID); err != nil || len(ids) != 0 {
		t.Errorf("manual book linked to series %v (err %v), want none", ids, err)
	}
}
