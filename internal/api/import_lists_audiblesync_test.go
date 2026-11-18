package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/audiblesync"
	"github.com/vavallee/bindery/internal/audiblesynclistsyncer"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type fakeASSyncer struct {
	startErr error
	starts   []int64
}

func (f *fakeASSyncer) StartOne(_ context.Context, id int64) error {
	f.starts = append(f.starts, id)
	return f.startErr
}

func (f *fakeASSyncer) Progress() audiblesynclistsyncer.SyncProgress {
	return audiblesynclistsyncer.SyncProgress{Running: true, ListName: "Audible wishlist", Stats: audiblesynclistsyncer.SyncStats{Owned: 2}}
}

func createList(t *testing.T, h *ImportListHandler, body string) (*httptest.ResponseRecorder, models.ImportList) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/importlist", bytes.NewBufferString(body)))
	var il models.ImportList
	_ = json.NewDecoder(rec.Body).Decode(&il)
	return rec, il
}

func TestCreateAudibleSyncListNormalises(t *testing.T) {
	h, _, _ := importListFixture(t)
	rec, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","apiKey":"ignored","account":" 2 ","mediaType":"ebook","enabled":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if il.MediaType != models.MediaTypeAudiobook || il.URL != models.ImportListSourceWishlist || il.APIKeyConfigured || il.Account != "2" {
		t.Fatalf("list = %+v", il)
	}
	if !il.SkipOwned || il.RemoveOnImport {
		t.Fatalf("defaults: skipOwned=%v removeOnImport=%v, want true and false", il.SkipOwned, il.RemoveOnImport)
	}

	rec, _ = createList(t, h, `{"name":"Bad","type":"audiblesync","account":"main"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a non-numeric account must be refused, got %d", rec.Code)
	}
	rec, _ = createList(t, h, `{"name":"Bad","type":"audiblesync","url":"library"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown source must be refused, got %d", rec.Code)
	}

	// The unavailable books source ignores the wishlist options.
	rec, il = createList(t, h, `{"name":"Gone","type":"audiblesync","url":"unavailable","account":"2","skipOwned":true,"removeOnImport":true}`)
	if rec.Code != http.StatusCreated || il.URL != models.ImportListSourceUnavailable || il.SkipOwned || il.RemoveOnImport {
		t.Fatalf("%d %+v", rec.Code, il)
	}
}

type fakeASBrowser struct{}

func (fakeASBrowser) Accounts(context.Context) ([]audiblesync.Account, error) {
	return []audiblesync.Account{{ID: 1, Name: "Main", CountryCode: "uk"}, {ID: 2, Name: "Spare", CountryCode: "us", NeedsLogin: true}}, nil
}
func (fakeASBrowser) WishlistTotal(_ context.Context, id int) (int, error) { return 10 * id, nil }
func (fakeASBrowser) LibraryTotal(_ context.Context, id int, status string) (int, error) {
	if id == 2 {
		return 0, errors.New("needs login")
	}
	if status != audiblesync.LibraryStatusUnavailable {
		return 0, errors.New("wrong status")
	}
	return 3, nil
}

func TestAudibleSyncListsOffersTwoListsPerAccount(t *testing.T) {
	h, _, settings := importListFixture(t)
	rec := httptest.NewRecorder()
	h.AudibleSyncLists(rec, httptest.NewRequest(http.MethodGet, "/importlist/audiblesync/lists", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured: %d", rec.Code)
	}

	ctx := context.Background()
	_ = settings.Set(ctx, SettingAudibleSyncEnabled, "true")
	_ = settings.Set(ctx, SettingAudibleSyncBaseURL, "http://10.0.0.5:8080")
	_ = settings.Set(ctx, SettingAudibleSyncAPIToken, "t")
	h.asLists = func(AudibleSyncStoredConfig) (audibleSyncListBrowser, error) { return fakeASBrowser{}, nil }
	rec = httptest.NewRecorder()
	h.AudibleSyncLists(rec, httptest.NewRequest(http.MethodGet, "/importlist/audiblesync/lists", nil))
	var lists []AudibleSyncList
	_ = json.NewDecoder(rec.Body).Decode(&lists)
	if rec.Code != http.StatusOK || len(lists) != 4 {
		t.Fatalf("%d %+v", rec.Code, lists)
	}
	want := []AudibleSyncList{
		{AccountID: 1, AccountName: "Main", CountryCode: "uk", Source: "wishlist", BooksCount: 10},
		{AccountID: 1, AccountName: "Main", CountryCode: "uk", Source: "unavailable", BooksCount: 3},
		{AccountID: 2, AccountName: "Spare", CountryCode: "us", NeedsLogin: true, Source: "wishlist", BooksCount: 20},
		{AccountID: 2, AccountName: "Spare", CountryCode: "us", NeedsLogin: true, Source: "unavailable", BooksCount: -1},
	}
	for i := range want {
		if lists[i] != want[i] {
			t.Errorf("list %d = %+v, want %+v", i, lists[i], want[i])
		}
	}
}

func TestUpdateAudibleSyncListOptions(t *testing.T) {
	h, repo, _ := importListFixture(t)
	_, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","enabled":true}`)

	r := chi.NewRouter()
	r.Put("/importlist/{id}", h.Update)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/importlist/"+strconv.FormatInt(il.ID, 10),
		bytes.NewBufferString(`{"skipOwned":false,"removeOnImport":true,"mediaType":"ebook"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(context.Background(), il.ID)
	if got.SkipOwned || !got.RemoveOnImport || got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("after update = %+v", got)
	}
}

func TestSyncDispatchesAudibleSyncLists(t *testing.T) {
	h, repo, _ := importListFixture(t)
	hc := &fakeHCSyncer{}
	as := &fakeASSyncer{}
	h.hcSync = hc
	h.WithAudibleSyncSyncer(as)
	ctx := context.Background()
	wish := models.ImportList{Name: "Wishlist", Type: models.ImportListTypeAudibleSync, Enabled: true}
	shelf := models.ImportList{Name: "Shelf", Type: models.ImportListTypeHardcover, URL: "wtr", Enabled: true}
	_ = repo.Create(ctx, &wish)
	_ = repo.Create(ctx, &shelf)

	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("wishlist sync: %d %s", rec.Code, rec.Body.String())
	}
	if rec := syncRequest(h, shelf.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("hardcover sync: %d %s", rec.Code, rec.Body.String())
	}
	if len(as.starts) != 1 || as.starts[0] != wish.ID || len(hc.starts) != 1 || hc.starts[0] != shelf.ID {
		t.Fatalf("audible sync starts %v, hardcover starts %v", as.starts, hc.starts)
	}

	as.startErr = audiblesynclistsyncer.ErrNotConfigured
	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured: %d", rec.Code)
	}
	as.startErr = audiblesynclistsyncer.ErrSyncAlreadyRunning
	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusConflict {
		t.Fatalf("already running: %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	h.SyncStatus(rec, httptest.NewRequest(http.MethodGet, "/importlist/sync/status?type=audiblesync", nil))
	var p audiblesynclistsyncer.SyncProgress
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != http.StatusOK || p.Stats.Owned != 2 {
		t.Fatalf("status: %d %+v", rec.Code, p)
	}
}

// An Audible Sync list never keeps a token, whether one is sent with an
// update or left over from a list whose type was changed to this one.
func TestUpdateAudibleSyncListDropsAToken(t *testing.T) {
	h, repo, _ := importListFixture(t)
	ctx := context.Background()
	r := chi.NewRouter()
	r.Put("/importlist/{id}", h.Update)
	put := func(id int64, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/importlist/"+strconv.FormatInt(id, 10), bytes.NewBufferString(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}

	_, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","enabled":true}`)
	put(il.ID, `{"apiKey":"smuggled"}`)
	if got, _ := repo.GetByID(ctx, il.ID); got.APIKey != "" {
		t.Fatalf("token kept on an Audible Sync list: %q", got.APIKey)
	}

	shelf := models.ImportList{Name: "Shelf", Type: models.ImportListTypeHardcover, URL: "wtr", APIKey: "hc-token", Enabled: true}
	if err := repo.Create(ctx, &shelf); err != nil {
		t.Fatal(err)
	}
	put(shelf.ID, `{"type":"audiblesync","url":""}`)
	if got, _ := repo.GetByID(ctx, shelf.ID); got.Type != models.ImportListTypeAudibleSync || got.APIKey != "" {
		t.Fatalf("after the type change = type %q token %q", got.Type, got.APIKey)
	}
}

// A failed read of the list is an error, not a guess: guessing would send an
// Audible Sync list to the Hardcover syncer.
func TestSyncReportsAFailedListRead(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	h := NewImportListHandler(db.NewImportListRepo(database), db.NewSettingsRepo(database), nil, db.NewUserRepo(database))
	hc := &fakeHCSyncer{}
	h.hcSync = hc
	h.WithAudibleSyncSyncer(&fakeASSyncer{})
	database.Close()

	if rec := syncRequest(h, 1); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if len(hc.starts) != 0 {
		t.Fatalf("hardcover syncer started for an unread list: %v", hc.starts)
	}
}

// fakeSlowBrowser answers the accounts but never a count, as an Audible Sync
// behind a proxy that hangs would.
type fakeSlowBrowser struct{ fakeASBrowser }

func (fakeSlowBrowser) WishlistTotal(ctx context.Context, _ int) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}
func (fakeSlowBrowser) LibraryTotal(ctx context.Context, _ int, _ string) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// Slow counts do not hold the lists up: they come back unknown once the
// short deadline passes.
func TestAudibleSyncListsDoesNotWaitOnSlowCounts(t *testing.T) {
	h, _, settings := importListFixture(t)
	ctx := context.Background()
	_ = settings.Set(ctx, SettingAudibleSyncEnabled, "true")
	_ = settings.Set(ctx, SettingAudibleSyncBaseURL, "http://10.0.0.5:8080")
	_ = settings.Set(ctx, SettingAudibleSyncAPIToken, "t")
	h.asLists = func(AudibleSyncStoredConfig) (audibleSyncListBrowser, error) { return fakeSlowBrowser{}, nil }
	prev := audibleSyncCountTimeout
	audibleSyncCountTimeout = 50 * time.Millisecond
	t.Cleanup(func() { audibleSyncCountTimeout = prev })

	start := time.Now()
	rec := httptest.NewRecorder()
	h.AudibleSyncLists(rec, httptest.NewRequest(http.MethodGet, "/importlist/audiblesync/lists", nil))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v; the counts must be read side by side under one deadline", elapsed)
	}
	var lists []AudibleSyncList
	_ = json.NewDecoder(rec.Body).Decode(&lists)
	if rec.Code != http.StatusOK || len(lists) != 4 {
		t.Fatalf("%d %+v", rec.Code, lists)
	}
	for _, l := range lists {
		if l.BooksCount != -1 {
			t.Fatalf("list %+v; a count that timed out is unknown", l)
		}
	}
}

// A list pointed at another source forgets the books it created, so the next
// sync cannot act on books the new source never listed.
func TestChangingAnAudibleSyncListsSourceForgetsItsBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewImportListRepo(database)
	h := NewImportListHandler(repo, db.NewSettingsRepo(database), nil, db.NewUserRepo(database))
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1W", AuthorID: author.ID, Title: "B", SortTitle: "B", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := db.NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	bookID := book.ID
	_, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","account":"1","enabled":true}`)
	r := chi.NewRouter()
	r.Put("/importlist/{id}", h.Update)
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/importlist/"+strconv.FormatInt(il.ID, 10), bytes.NewBufferString(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	record := func() {
		t.Helper()
		if err := repo.RecordBook(ctx, il.ID, bookID, "B000000001", 7); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		recs, err := repo.ListBooks(ctx, il.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(recs)
	}

	record()
	put(`{"skipOwned":false}`)
	if count() != 1 {
		t.Fatal("an option change must keep the list's books")
	}
	put(`{"url":"unavailable"}`)
	if count() != 0 {
		t.Fatal("a source change must forget the list's books")
	}
	record()
	put(`{"account":"2"}`)
	if count() != 0 {
		t.Fatal("an account change must forget the list's books")
	}
}
