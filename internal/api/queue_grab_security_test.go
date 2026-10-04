package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The tests in this file hold POST /queue/grab to what a non-admin account is
// for: grabbing a release a search returned. The handler used to trust the
// posted nzbUrl, bookId and guid, which let any user role account make Bindery
// fetch an arbitrary URL with an indexer key attached and read the answer back
// from the error, grab into another user's book, and take over another user's
// download row.

// fixedSearcher answers every free-text search with the same results.
type fixedSearcher struct{ results []newznab.SearchResult }

func (s fixedSearcher) SearchBookWithDebug(context.Context, []models.Indexer, indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug) {
	return s.results, nil
}

func (s fixedSearcher) SearchQuery(context.Context, []models.Indexer, string) []newznab.SearchResult {
	return s.results
}

func (fixedSearcher) Cooldown(models.Indexer) (time.Time, string, bool) {
	return time.Time{}, "", false
}

// grabSecFixture is a Prowlarr stand in with a download endpoint and an API
// endpoint that answers with its own credentials, a SABnzbd stub, and the two
// handlers wired to one search result registry the way main.go wires them.
type grabSecFixture struct {
	queue     *QueueHandler
	search    *IndexerHandler
	database  *sql.DB
	downloads *db.DownloadRepo
	books     *db.BookRepo
	ctx       context.Context

	indexerURL   string // base URL of the Prowlarr stand in
	indexerID    int64
	guid         string // the GUID the search returns
	downloadURL  string // the download URL the search returns, unsigned
	configHits   *atomic.Int32
	downloadHits *atomic.Int32
	adds         *atomic.Int32
	alice, bob   int64
}

func newGrabSecFixture(t *testing.T) *grabSecFixture {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())

	f := &grabSecFixture{configHits: &atomic.Int32{}, downloadHits: &atomic.Int32{}, adds: &atomic.Int32{}}
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != leakedAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/config/host":
			f.configHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"apiKey":"` + leakedAPIKey + `","password":"hunter2"}`))
		case "/3/download":
			f.downloadHits.Add(1)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(indexerSrv.Close)
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.adds.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-sec"}})
	}))
	t.Cleanup(sab.Close)

	queue, database, downloads, clients, books, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	indexers := db.NewIndexerRepo(database)
	idx := &models.Indexer{Name: "prowlarr", Type: "newznab", URL: indexerSrv.URL + "/3/api", APIKey: leakedAPIKey, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	f.indexerURL = indexerSrv.URL
	f.indexerID = idx.ID
	f.guid = "guid-sec-1"
	f.downloadURL = indexerSrv.URL + "/3/download?file=One+Shot&link=abc"
	// The searcher signs download URLs on the indexer's host, as the real
	// search path does; the search handler strips the key before replying.
	signed := newznab.SignDownloadURLFor(f.downloadURL, idx.URL, idx.APIKey)
	searcher := fixedSearcher{results: []newznab.SearchResult{{
		GUID: f.guid, IndexerID: idx.ID, IndexerName: idx.Name, Title: "Lee Child - One Shot (epub)",
		Size: 1234, NZBURL: signed, Protocol: "usenet",
	}}}

	registry := NewSearchResultRegistry()
	f.queue = queue.WithIndexers(indexers).WithSearchResults(registry)
	f.search = NewIndexerHandler(indexers, books, db.NewAuthorRepo(database), db.NewMetadataProfileRepo(database),
		searcher, db.NewSettingsRepo(database), db.NewBlocklistRepo(database)).WithSearchResults(registry)
	f.database, f.downloads, f.books, f.ctx = database, downloads, books, ctx
	f.alice, f.bob = regrabUsers(t, database)
	return f
}

// asRole puts a signed in user with role on r, as the auth middleware does.
func asRole(r *http.Request, uid int64, role string) *http.Request {
	return r.WithContext(auth.WithUserRole(auth.WithUserID(r.Context(), uid), role))
}

// searchAs runs the free-text search as uid with role "user", which records
// the results the server returned.
func (f *grabSecFixture) searchAs(t *testing.T, uid int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.search.SearchQuery(rec, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/indexer/search?q=one+shot", nil), uid, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("search: got %d: %s", rec.Code, rec.Body.String())
	}
	assertNoIndexerAPIKey(t, "search response", rec.Body.Bytes())
}

func (f *grabSecFixture) grabAs(uid int64, role string, payload map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	f.queue.Grab(rec, asRole(httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(b)), uid, role))
	return rec
}

// TestQueueGrab_NonAdminCannotFetchAPostedURL is problem 1. A user role
// account posting a URL on a configured indexer's host had it signed with that
// indexer's key and fetched, and the 256 byte excerpt of the non-NZB answer
// came back in the 502. Any other LAN service could be read the same way,
// without the key.
func TestQueueGrab_NonAdminCannotFetchAPostedURL(t *testing.T) {
	f := newGrabSecFixture(t)

	lanHits := &atomic.Int32{}
	lan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lanHits.Add(1)
		_, _ = w.Write([]byte(`router-admin-page`))
	}))
	t.Cleanup(lan.Close)

	for _, tc := range []struct {
		name string
		url  string
	}{
		{"indexer API path", f.indexerURL + "/api/v1/config/host"},
		{"other LAN service", lan.URL + "/status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
				"guid": "guid-forged-" + tc.name, "title": "x", "nzbUrl": tc.url, "indexerId": f.indexerID,
			})
			assertNoIndexerAPIKey(t, "grab response", rec.Body.Bytes())
			if strings.Contains(rec.Body.String(), "router-admin-page") || strings.Contains(rec.Body.String(), "hunter2") {
				t.Errorf("the grab response must not carry the fetched body: %s", rec.Body.String())
			}
			if rec.Code == http.StatusAccepted || rec.Code == http.StatusBadGateway {
				t.Errorf("a grab of a release no search returned must be refused before anything is fetched; got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if n := f.configHits.Load(); n != 0 {
		t.Errorf("the indexer's API endpoint must never be fetched for a non-admin grab; it was hit %d time(s)", n)
	}
	if n := lanHits.Load(); n != 0 {
		t.Errorf("a LAN service must never be fetched for a non-admin grab; it was hit %d time(s)", n)
	}
	if n := f.adds.Load(); n != 0 {
		t.Errorf("nothing may reach the download client; got %d add(s)", n)
	}
	var rows int
	if err := f.database.QueryRow("SELECT COUNT(*) FROM downloads").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("a refused grab must not leave a download row; found %d", rows)
	}
}

// TestQueueGrab_NonAdminGrabUsesTheSearchedURL is the feature that stays: a
// user grabs a release their search returned. The download URL is the one the
// server returned, whatever the request carries in nzbUrl.
func TestQueueGrab_NonAdminGrabUsesTheSearchedURL(t *testing.T) {
	f := newGrabSecFixture(t)
	f.searchAs(t, f.bob)

	rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
		"guid": f.guid, "title": "Lee Child - One Shot (epub)", "indexerId": f.indexerID,
		"nzbUrl": f.indexerURL + "/api/v1/config/host",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grab of a searched release: got %d: %s", rec.Code, rec.Body.String())
	}
	assertNoIndexerAPIKey(t, "grab response", rec.Body.Bytes())
	if n := f.configHits.Load(); n != 0 {
		t.Errorf("the posted nzbUrl must not be fetched; the indexer API endpoint was hit %d time(s)", n)
	}
	if n := f.downloadHits.Load(); n != 1 {
		t.Errorf("the searched download URL must be fetched, signed, once; got %d", n)
	}
	if n := f.adds.Load(); n != 1 {
		t.Errorf("expected one add at the download client, got %d", n)
	}
	dl, err := f.downloads.GetByGUID(f.ctx, f.guid)
	if err != nil || dl == nil {
		t.Fatalf("reload download: %v", err)
	}
	if !strings.HasPrefix(dl.NZBURL, f.indexerURL+"/3/download") {
		t.Errorf("the stored URL must be the searched one, got %q", newznab.RedactDownloadURL(dl.NZBURL))
	}
	if dl.OwnerUserID != f.bob {
		t.Errorf("the download must belong to the grabber %d, got %d", f.bob, dl.OwnerUserID)
	}
}

// TestQueueGrab_AdminMayStillPostAURL: admins and API key callers (which carry
// the admin role) keep posting their own download URLs, which is what API
// clients that search elsewhere do.
func TestQueueGrab_AdminMayStillPostAURL(t *testing.T) {
	f := newGrabSecFixture(t)
	rec := f.grabAs(f.alice, auth.RoleAdmin, map[string]any{
		"guid": "guid-admin-unsearched", "title": "x", "nzbUrl": f.downloadURL,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("admin grab of a posted URL: got %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.downloadHits.Load(); n != 1 {
		t.Errorf("expected the posted URL to be fetched once, got %d", n)
	}
}

// TestQueueGrab_RefusesAnotherUsersBook is problem 2: with tenancy on, a user
// must not grab into a book another user owns.
func TestQueueGrab_RefusesAnotherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	aliceBook := regrabOwnedBook(t, f.database, f.books, "alice-sec", f.alice)
	f.searchAs(t, f.bob)

	rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
		"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL,
		"indexerId": f.indexerID, "bookId": aliceBook.ID,
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob grabbing into alice's book: want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.adds.Load(); n != 0 {
		t.Errorf("nothing may reach the download client; got %d add(s)", n)
	}
	if dl, err := f.downloads.GetByGUID(f.ctx, f.guid); err != nil || dl != nil {
		t.Errorf("a refused grab must not leave a download row: %+v, %v", dl, err)
	}

	// His own book is fine.
	bobBook := regrabOwnedBook(t, f.database, f.books, "bob-sec", f.bob)
	rec = f.grabAs(f.bob, auth.RoleUser, map[string]any{
		"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL,
		"indexerId": f.indexerID, "bookId": bobBook.ID,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bob grabbing into his own book: got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestQueueGrab_DoesNotTakeOverAnotherUsersRow is problem 3: the GUID lookup
// is global (the column is UNIQUE), and a dead row was reused with the caller
// stamped as its new owner, inheriting the row's book. With tenancy on, a dead
// row that still means something to its owner (failed: Retry sends it again;
// importBlocked: Retry import re-runs its files) is not someone else's to
// claim. The caller's own rows keep #2289's reuse.
func TestQueueGrab_DoesNotTakeOverAnotherUsersRow(t *testing.T) {
	for _, status := range []models.DownloadState{models.StateFailed, models.StateImportBlocked} {
		t.Run(string(status), func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newGrabSecFixture(t)
			aliceBook := regrabOwnedBook(t, f.database, f.books, "alice-row", f.alice)
			row := &models.Download{
				GUID: f.guid, BookID: &aliceBook.ID, OwnerUserID: f.alice, Title: "Alice Release",
				NZBURL: f.downloadURL, Status: status, Protocol: "usenet",
			}
			if err := f.downloads.Create(f.ctx, row); err != nil {
				t.Fatal(err)
			}
			f.searchAs(t, f.bob)

			rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
				"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
			})
			if rec.Code != http.StatusConflict {
				t.Errorf("bob grabbing a release alice's row holds: want 409, got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), string(status)) {
				t.Errorf("the refusal must not describe alice's row: %s", rec.Body.String())
			}
			if n := f.adds.Load(); n != 0 {
				t.Errorf("nothing may reach the download client; got %d add(s)", n)
			}
			got, err := f.downloads.GetByID(f.ctx, row.ID)
			if err != nil || got == nil {
				t.Fatalf("reload alice's row: %v", err)
			}
			if got.OwnerUserID != f.alice || got.Status != status || got.BookID == nil || *got.BookID != aliceBook.ID {
				t.Errorf("alice's row must be untouched; owner=%d status=%s book=%v", got.OwnerUserID, got.Status, got.BookID)
			}

			// Alice re-grabbing her own dead row still reuses it (#2289).
			f.searchAs(t, f.alice)
			rec = f.grabAs(f.alice, auth.RoleUser, map[string]any{
				"guid": f.guid, "title": "Lee Child - One Shot (epub)", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
			})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("alice re-grabbing her own row: got %d: %s", rec.Code, rec.Body.String())
			}
			got, err = f.downloads.GetByID(f.ctx, row.ID)
			if err != nil || got == nil {
				t.Fatalf("reload alice's row: %v", err)
			}
			if got.Status != models.StateDownloading || got.BookID == nil || *got.BookID != aliceBook.ID {
				t.Errorf("alice's reused row: status=%s book=%v", got.Status, got.BookID)
			}
		})
	}
}

// TestQueueGrab_ForeignLiveRowRefusalIsOpaque: a live row another user owns
// answers 409 as before, without naming its state.
func TestQueueGrab_ForeignLiveRowRefusalIsOpaque(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	if err := f.downloads.Create(f.ctx, &models.Download{
		GUID: f.guid, OwnerUserID: f.alice, Title: "Alice Release",
		NZBURL: f.downloadURL, Status: models.StateDownloading, Protocol: "usenet",
	}); err != nil {
		t.Fatal(err)
	}
	f.searchAs(t, f.bob)
	rec := f.grabAs(f.bob, auth.RoleUser, map[string]any{
		"guid": f.guid, "title": "x", "nzbUrl": f.downloadURL, "indexerId": f.indexerID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), string(models.StateDownloading)) {
		t.Errorf("the refusal must not describe another user's row: %s", rec.Body.String())
	}
}

// TestQueueRetry_NonAdminStillResendsOwnRow: the queue's Retry re-sends the URL
// the row stored, which a search produced when the row was made. It is not
// held to the search registry, which a restart empties.
func TestQueueRetry_NonAdminStillResendsOwnRow(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGrabSecFixture(t)
	signed := newznab.SignDownloadURLFor(f.downloadURL, f.indexerURL+"/3/api", leakedAPIKey)
	row := &models.Download{
		GUID: "guid-retry-own", OwnerUserID: f.bob, Title: "Bob Release",
		NZBURL: signed, Status: models.StateFailed, Protocol: "usenet",
	}
	if err := f.downloads.Create(f.ctx, row); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	rec := httptest.NewRecorder()
	f.queue.RetryDownload(rec, withURLParam(asRole(httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+id+"/retry", nil), f.bob, auth.RoleUser), "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bob retrying his own failed row: got %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.downloadHits.Load(); n != 1 {
		t.Errorf("expected the stored URL to be fetched once, got %d", n)
	}
}
