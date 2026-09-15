package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// fakeAudibleCatalogue stands in for api.audible.com. The sync calls it from
// its own goroutine, so every field sits behind mu.
type fakeAudibleCatalogue struct {
	mu       sync.Mutex
	books    []models.Book
	calls    int
	bypassed int
}

func (c *fakeAudibleCatalogue) SearchBooksByAuthor(ctx context.Context, _ string) ([]models.Book, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if metadata.CacheBypassed(ctx) {
		c.bypassed++
	}
	return append([]models.Book(nil), c.books...), nil
}

func (c *fakeAudibleCatalogue) setBooks(books []models.Book) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.books = books
}

func (c *fakeAudibleCatalogue) counts() (calls, bypassed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.bypassed
}

// TestAuthorRefresh_ManualRefreshBypassesAudibleCache: on an audiobook or
// "both" setup the catalogue sync adds the author's Audible list, which the
// aggregator caches for a day. The manual Refresh has to hand that lookup the
// bypass context too, or a new Audible only release stays hidden after the
// click (#2601 review). The catalogue sync bulk refresh and Refresh all run
// (RefreshAuthorBooks) keeps the cached list. The scheduled metadata refresh
// only rereads author profiles and never asks Audible, so it has no Audible
// call to pin.
func TestAuthorRefresh_ManualRefreshBypassesAudibleCache(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	if err := settingsRepo.Set(ctx, SettingDefaultMediaType, models.MediaTypeAudiobook); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{
		ForeignID: "OL2601A", Name: "Ann Leckie", SortName: "Leckie, Ann",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	stub := &stubMetaProvider{
		works: []models.Book{{ForeignID: "OL2601W1", Title: "Ancillary Justice", Language: "eng",
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}},
		author: &models.Author{ForeignID: "OL2601A", Name: "Ann Leckie", MetadataProvider: "openlibrary"},
	}
	audiobook := func(asin, title string) models.Book {
		return models.Book{ForeignID: "audible:" + asin, ASIN: asin, Title: title, Language: "eng",
			MediaType: models.MediaTypeAudiobook, MetadataProvider: "audible"}
	}
	aud := &fakeAudibleCatalogue{books: []models.Book{audiobook("B0012601A1", "Ancillary Justice")}}
	agg := metadata.NewAggregator(stub).WithAudibleCatalogue(aud)
	group := jobs.NewGroup(context.Background())
	defer group.Shutdown(5 * time.Second) // runs before database.Close
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).WithJobs(group)

	reload := func() *models.Author {
		t.Helper()
		a, err := authorRepo.GetByID(ctx, author.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	// Warm the Audible cache the way a bulk refresh does.
	h.RefreshAuthorBooks(reload(), false, models.MediaTypeAudiobook)
	if calls, _ := aud.counts(); calls != 1 {
		t.Fatalf("warm sync made %d Audible calls, want 1", calls)
	}

	// A new Audible only release appears upstream.
	aud.setBooks([]models.Book{audiobook("B0012601A1", "Ancillary Justice"), audiobook("B0012601A2", "Lake of Souls")})

	// The bulk and Refresh all sync keeps the cached list.
	h.RefreshAuthorBooks(reload(), false, models.MediaTypeAudiobook)
	if calls, _ := aud.counts(); calls != 1 {
		t.Fatalf("bulk sync made %d Audible calls, want the 1 warm call: it went past the cache", calls)
	}

	// The manual Refresh must reach Audible with the bypass.
	id := strconv.FormatInt(author.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/refresh", nil), "id", id)
	rec := httptest.NewRecorder()
	h.Refresh(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("Refresh status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.runningSyncs.running(author.ID) {
		if time.Now().After(deadline) {
			t.Fatal("manual refresh sync never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls, bypassed := aud.counts()
	if calls != 2 || bypassed != 1 {
		t.Fatalf("after the manual refresh: %d Audible calls (%d bypassed), want 2 with 1 bypassed: the cached Audible list answered the refresh", calls, bypassed)
	}

	// The fresh list was written back for ordinary readers.
	got, err := agg.GetAuthorAudiobooks(ctx, author.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ordinary GetAuthorAudiobooks after the refresh returned %d books, want the refreshed 2", len(got))
	}
	if calls, _ := aud.counts(); calls != 2 {
		t.Fatalf("ordinary GetAuthorAudiobooks after the refresh reached Audible (%d calls); the cache should answer it", calls)
	}
}
