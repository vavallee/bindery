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

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestRegrabbableState pins exactly which existing download states release
// their GUID for a fresh grab (#1955). The set must contain only states that
// are dead to every automatic path: anything still in flight would be
// duplicated or clobbered by a re-grab.
func TestRegrabbableState(t *testing.T) {
	tests := []struct {
		status models.DownloadState
		want   bool
	}{
		{models.StateFailed, true},
		// #1955: terminal to the pollers, so without this the row pins the GUID
		// forever and every later Grab answers "already grabbed".
		{models.StateImportBlocked, true},
		// Still live work — a re-grab would race the scanner or duplicate the
		// torrent.
		{models.StateGrabbed, false},
		{models.StateDownloading, false},
		{models.StateCompleted, false},
		{models.StateImportPending, false},
		{models.StateImporting, false},
		{models.StateImportFailed, false},
		{models.StateImported, false},
		{models.StateImportExternal, false},
		{models.StateImportHeld, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := regrabbableState(tc.status); got != tc.want {
				t.Errorf("regrabbableState(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestAlreadyGrabbedDetail proves the refusal explains itself. The reporter of
// #1955 saw only the bare "already grabbed" sentinel on a screen that shows no
// queue state, so the message must name the situation and point somewhere.
func TestAlreadyGrabbedDetail(t *testing.T) {
	tests := []struct {
		status  models.DownloadState
		want    []string
		notWant []string
	}{
		{status: models.StateImported, want: []string{"already been imported"}},
		{
			status: models.StateImportFailed,
			want:   []string{"retrying", "Retry import"},
			// The message must offer an action rather than tell the user to
			// wait: "wait for it to settle" was advice for an outcome that,
			// before the skip limit, could never arrive.
			notWant: []string{"wait"},
		},
		{status: models.StateImportExternal, want: []string{"external import tool"}},
		{status: models.StateImportHeld, want: []string{"external import tool"}},
		{status: models.StateDownloading, want: []string{"already in the queue", "downloading"}},
		{status: models.StateGrabbed, want: []string{"already in the queue", "grabbed"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := alreadyGrabbedDetail(tc.status)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("alreadyGrabbedDetail(%q) = %q, want it to mention %q", tc.status, got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(strings.ToLower(got), notWant) {
					t.Errorf("alreadyGrabbedDetail(%q) = %q, must not mention %q", tc.status, got, notWant)
				}
			}
		})
	}
}

// TestQueueGrab_ImportBlockedIsRegrabbable replays flaevers' report (#1955).
//
// Their audiobook download exhausted its import retry budget and was terminally
// blocked. Clicking Grab on the same release from the search page then answered
// 409 "already grabbed" — the row pinned the GUID and there was no way forward
// from that screen. A blocked download must release the release for a re-grab,
// reusing the same row with a clean error message and a fresh retry budget.
func TestQueueGrab_ImportBlockedIsRegrabbable(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()
	addCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1955"}})
	}))
	defer srv.Close()

	h, _, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, srv.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	blocked := &models.Download{
		GUID:         "guid-1955",
		Title:        "Harriet Tubman: Live in Concert by Bob the Drag Queen [ENG / M4B]",
		NZBURL:       indexerSrv.URL + "/old.nzb",
		Status:       models.StateImportBlocked,
		Protocol:     "usenet",
		ErrorMessage: "import retry limit reached (5 attempts) — fix the underlying problem, then retry manually",
	}
	if err := downloads.Create(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if err := downloads.IncrementImportRetryCount(ctx, blocked.ID); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"guid":"guid-1955","nzbUrl":"` + indexerSrv.URL + `/new.nzb","title":"New","size":7}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("#1955 regression: a terminally blocked download must not block a re-grab; got %d: %s",
			rec.Code, rec.Body.String())
	}
	if addCalls != 1 {
		t.Fatalf("expected the release to be sent to the download client once, got %d calls", addCalls)
	}

	got, err := downloads.GetByGUID(ctx, "guid-1955")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.ID != blocked.ID {
		t.Errorf("expected the re-grab to reuse row %d, got %d", blocked.ID, got.ID)
	}
	if got.Status == models.StateImportBlocked {
		t.Errorf("expected the row to leave importBlocked after the re-grab, still %q", got.Status)
	}
	if got.ErrorMessage != "" {
		t.Errorf("expected the stale blocking reason cleared, got %q", got.ErrorMessage)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("expected a fresh retry budget after the re-grab, got %d", got.ImportRetryCount)
	}
	if got.Title != "New" || got.NZBURL != indexerSrv.URL+"/new.nzb" {
		t.Errorf("expected the re-grab to refresh the release fields, got title=%q url=%q", got.Title, got.NZBURL)
	}
}

// TestQueueGrab_LiveDownloadStillBlocksRegrabWithReason is the other half of
// #1955: widening the re-grab must not open a hole for downloads that are
// still live. The 409 stays, and now carries the explanation the search page
// renders instead of the bare sentinel.
//
// The imported case keeps its book (#2289). That is the by-design guard: the
// release is already in the library, so a re-grab would only duplicate it.
// The other two have no book on purpose, because an in-flight row with a NULL
// book_id (a free-text grab the importer has not matched yet) is still live
// work and must stay refused.
func TestQueueGrab_LiveDownloadStillBlocksRegrabWithReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   models.DownloadState
		withBook bool
		want     string
	}{
		{"downloading without a book", models.StateDownloading, false, "already in the queue"},
		{"imported with its book present", models.StateImported, true, "already been imported"},
		{"importFailed without a book", models.StateImportFailed, false, "Retry import"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, database, downloads, _, books, ctx := queueFixture(t)
			dl := &models.Download{GUID: "live-guid", Title: "T", Protocol: "usenet", Status: tc.status}
			if tc.withBook {
				book := regrabBook(t, database, books, "live")
				dl.BookID = &book.ID
			}
			if err := downloads.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}

			rec := regrabPost(h, `{"guid":"live-guid","nzbUrl":"http://example/x.nzb","title":"T"}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 for a %s download, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload["error"], "already grabbed") {
				t.Errorf("expected the sentinel preserved for clients matching on it, got %q", payload["error"])
			}
			if !strings.Contains(payload["error"], tc.want) {
				t.Errorf("expected the 409 body to explain why (%q), got %q", tc.want, payload["error"])
			}
		})
	}
}

// TestQueueGrab_ImportedReleaseRegrabbableAfterBookDeleted replays #2289.
//
// schmitzkr and Terebi42 deleted a book whose release had imported, then tried
// to grab that release again. Every attempt answered 409 "already grabbed: this
// release has already been imported": downloads.book_id is ON DELETE SET NULL,
// so the row outlived the book, kept the GUID and kept reporting imported, and
// nothing in the UI could clear it. The grab is refused while the book exists
// and must go through once it is gone, reusing the row rather than tripping
// the GUID's UNIQUE constraint.
//
// Both deletion paths are exercised because only one of them runs through the
// book handler: deleting an author removes its books by FK cascade, and the
// fix has to hold for that too.
func TestQueueGrab_ImportedReleaseRegrabbableAfterBookDeleted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(ctx context.Context, database *sql.DB, books *db.BookRepo, book *models.Book) error
	}{
		{"book deleted", func(ctx context.Context, _ *sql.DB, books *db.BookRepo, book *models.Book) error {
			return books.Delete(ctx, book.ID)
		}},
		{"author deleted", func(ctx context.Context, database *sql.DB, _ *db.BookRepo, book *models.Book) error {
			return db.NewAuthorRepo(database).Delete(ctx, book.AuthorID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, database, downloads, clients, books, ctx := queueFixture(t)
			indexerURL, adds := regrabDownloadClient(t, clients)
			book := regrabBook(t, database, books, "deleted")

			imported := &models.Download{
				GUID:     "guid-2289",
				BookID:   &book.ID,
				Title:    "Old Release",
				NZBURL:   indexerURL + "/old.nzb",
				Status:   models.StateImported,
				Protocol: "usenet",
			}
			if err := downloads.Create(ctx, imported); err != nil {
				t.Fatal(err)
			}

			body := `{"guid":"guid-2289","nzbUrl":"` + indexerURL + `/new.nzb","title":"New Release","size":7}`
			if rec := regrabPost(h, body); rec.Code != http.StatusConflict {
				t.Fatalf("while the book exists the imported release must stay refused; got %d: %s", rec.Code, rec.Body.String())
			}

			if err := tc.remove(ctx, database, books, book); err != nil {
				t.Fatalf("delete: %v", err)
			}
			orphan, err := downloads.GetByGUID(ctx, "guid-2289")
			if err != nil || orphan == nil {
				t.Fatalf("expected the download row to survive the deletion: %v", err)
			}
			if orphan.BookID != nil || orphan.Status != models.StateImported {
				t.Fatalf("expected an orphaned imported row (book_id NULL), got book_id=%v status=%q", orphan.BookID, orphan.Status)
			}

			rec := regrabPost(h, body)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("#2289 regression: an imported release whose book was deleted must be grabbable again; got %d: %s",
					rec.Code, rec.Body.String())
			}
			if n := adds.Load(); n != 1 {
				t.Fatalf("expected the release sent to the download client once, got %d", n)
			}
			got, err := downloads.GetByGUID(ctx, "guid-2289")
			if err != nil || got == nil {
				t.Fatalf("reload download: %v", err)
			}
			if got.ID != imported.ID {
				t.Errorf("expected the re-grab to reuse row %d, got %d", imported.ID, got.ID)
			}
			if got.Status == models.StateImported || got.ImportedAt != nil {
				t.Errorf("expected the row to leave imported after the re-grab, got status=%q imported_at=%v", got.Status, got.ImportedAt)
			}
			if got.Title != "New Release" {
				t.Errorf("expected the re-grab to refresh the release fields, got title=%q", got.Title)
			}
		})
	}
}

// TestQueueGrab_OrphanedImportFromExistingInstallIsRegrabbable covers rows
// orphaned before the #2289 fix shipped. On an existing install the book was
// deleted long ago and the row already sits there with book_id NULL and status
// imported. No migration touches it, so the check itself has to accept it.
// The user has since added the book back and grabs from its page, so the reused
// row must now point at the new book.
func TestQueueGrab_OrphanedImportFromExistingInstallIsRegrabbable(t *testing.T) {
	h, database, downloads, clients, books, ctx := queueFixture(t)
	indexerURL, adds := regrabDownloadClient(t, clients)

	orphan := &models.Download{
		GUID:     "guid-2289-legacy",
		Title:    "Old Release",
		NZBURL:   indexerURL + "/old.nzb",
		Status:   models.StateImported,
		Protocol: "usenet",
	}
	if err := downloads.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	readded := regrabBook(t, database, books, "readded")

	rec := regrabPost(h, `{"guid":"guid-2289-legacy","nzbUrl":"`+indexerURL+`/new.nzb","title":"New Release","bookId":`+
		strconv.FormatInt(readded.ID, 10)+`}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("#2289 regression: a row orphaned before the fix must not block a grab; got %d: %s", rec.Code, rec.Body.String())
	}
	if n := adds.Load(); n != 1 {
		t.Fatalf("expected the release sent to the download client once, got %d", n)
	}
	got, err := downloads.GetByGUID(ctx, "guid-2289-legacy")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.ID != orphan.ID {
		t.Errorf("expected the re-grab to reuse row %d, got %d", orphan.ID, got.ID)
	}
	if got.BookID == nil || *got.BookID != readded.ID {
		t.Errorf("expected the reused row linked to the re-added book %d, got %v", readded.ID, got.BookID)
	}
}

// regrabPost sends a grab request through the real handler.
func regrabPost(h *QueueHandler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString(body)))
	return rec
}

// regrabBook creates an author and one book under it.
func regrabBook(t *testing.T, database *sql.DB, books *db.BookRepo, slug string) *models.Book {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "regrab-author-" + slug,
		Name:             "Regrab Author " + slug,
		SortName:         "Author, Regrab " + slug,
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{
		ForeignID:        "regrab-book-" + slug,
		AuthorID:         author.ID,
		Title:            "Regrab Book " + slug,
		SortTitle:        "regrab book " + slug,
		Genres:           []string{},
		Status:           models.BookStatusImported,
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}
	return book
}

// regrabDownloadClient registers a SABnzbd stub as the enabled client and
// returns an indexer stub's base URL plus a count of adds the client received.
func regrabDownloadClient(t *testing.T, clients *db.DownloadClientRepo) (string, *atomic.Int32) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)
	adds := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		adds.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-2289"}})
	}))
	t.Cleanup(srv.Close)
	host, port := testServerHostPort(t, srv.URL)
	if err := clients.Create(context.Background(), &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return indexerSrv.URL, adds
}

// TestQueueGrab_ReusedRowBelongsToNewGrabber pins the owner half of a reuse
// (#2289). With tenancy on, alice imports a release and then deletes the book;
// bob grabs the same release into his own book. The grab reuses alice's row,
// and unless the reuse writes the owner the row stays hers: it shows in her
// queue and not his, he gets 404 acting on his own download, and she can
// delete it, which removes his torrent from the client.
func TestQueueGrab_ReusedRowBelongsToNewGrabber(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h, database, downloads, clients, books, ctx := queueFixture(t)
	indexerURL, adds := regrabDownloadClient(t, clients)
	alice, bob := regrabUsers(t, database)
	as := func(r *http.Request, uid int64) *http.Request {
		return r.WithContext(auth.WithUserRole(auth.WithUserID(r.Context(), uid), "user"))
	}

	aliceBook := regrabOwnedBook(t, database, books, "alice", alice)
	row := &models.Download{
		GUID: "guid-2289-owner", BookID: &aliceBook.ID, OwnerUserID: alice,
		Title: "Old Release", NZBURL: indexerURL + "/old.nzb",
		Status: models.StateImported, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := books.Delete(ctx, aliceBook.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}

	bobBook := regrabOwnedBook(t, database, books, "bob", bob)
	body := `{"guid":"guid-2289-owner","nzbUrl":"` + indexerURL + `/new.nzb","title":"New Release","bookId":` +
		strconv.FormatInt(bobBook.ID, 10) + `}`
	rec := httptest.NewRecorder()
	h.Grab(rec, as(httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString(body)), bob))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bob's grab: got %d: %s", rec.Code, rec.Body.String())
	}
	if n := adds.Load(); n != 1 {
		t.Fatalf("expected one send to the download client, got %d", n)
	}
	owner, exists, err := downloads.GetOwnerByID(ctx, row.ID)
	if err != nil || !exists {
		t.Fatalf("owner lookup: exists=%v err=%v", exists, err)
	}
	if owner != bob {
		t.Errorf("the reused row must belong to bob (%d), whose grab it now is; owner is %d", bob, owner)
	}

	queueIDs := func(uid int64) map[int64]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		h.List(rec, as(httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), uid))
		if rec.Code != http.StatusOK {
			t.Fatalf("list as user %d: got %d: %s", uid, rec.Code, rec.Body.String())
		}
		var payload struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		ids := map[int64]bool{}
		for _, it := range payload.Items {
			ids[it.ID] = true
		}
		return ids
	}
	if !queueIDs(bob)[row.ID] {
		t.Error("bob's queue must list the download his grab created")
	}
	if queueIDs(alice)[row.ID] {
		t.Error("alice's queue must not list bob's download")
	}

	id := strconv.FormatInt(row.ID, 10)
	rec = httptest.NewRecorder()
	h.RetryImport(rec, withURLParam(as(httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+id+"/retry-import", nil), bob), "id", id))
	if rec.Code == http.StatusNotFound {
		t.Errorf("bob must be able to act on his own download; Retry import answered 404: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(as(httptest.NewRequest(http.MethodDelete, "/api/v1/queue/"+id, nil), alice), "id", id))
	if rec.Code != http.StatusNotFound {
		t.Errorf("alice must not be able to delete bob's download; got %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := downloads.GetByID(ctx, row.ID); err != nil || got == nil {
		t.Fatalf("bob's download must survive alice's delete attempt: %v", err)
	}
}

// TestQueueGrab_ReuseWithoutIdentityMatchesCreate: a reused row gets exactly
// the owner a fresh Create would. A grab with no user and no book is stored
// unowned (NULL) on a new row, so the reused row is stored unowned too rather
// than keeping the owner of the grab it replaces. API key and trusted local
// requests do not land here once an admin account exists, because
// auth.withOperatorUserID stamps them with the first admin's id; this is the
// no admin (or auth disabled) case.
func TestQueueGrab_ReuseWithoutIdentityMatchesCreate(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	h, database, downloads, clients, _, ctx := queueFixture(t)
	indexerURL, _ := regrabDownloadClient(t, clients)
	alice, _ := regrabUsers(t, database)

	failed := &models.Download{
		GUID: "guid-2289-noident-reuse", OwnerUserID: alice, Title: "Old Release",
		NZBURL: indexerURL + "/old.nzb", Status: models.StateFailed, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, failed); err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{"guid-2289-noident-reuse", "guid-2289-noident-fresh"} {
		rec := regrabPost(h, `{"guid":"`+guid+`","nzbUrl":"`+indexerURL+`/new.nzb","title":"New Release"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("grab %s: got %d: %s", guid, rec.Code, rec.Body.String())
		}
	}

	fresh, err := downloads.GetByGUID(ctx, "guid-2289-noident-fresh")
	if err != nil || fresh == nil {
		t.Fatalf("reload fresh download: %v", err)
	}
	freshOwner, _, err := downloads.GetOwnerByID(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	reusedOwner, _, err := downloads.GetOwnerByID(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshOwner != 0 {
		t.Fatalf("a fresh grab with no user and no book must be stored unowned, got owner %d", freshOwner)
	}
	if reusedOwner != freshOwner {
		t.Errorf("a reused row must get the owner Create gives a new one (%d), got %d (the old grab's owner is %d)",
			freshOwner, reusedOwner, alice)
	}
}

// TestQueueGrab_ReuseClearsStaleImportPath: a reused row must not carry the
// import_path the scanner recorded for the previous grab's files. Match to
// book imports straight from import_path, so a stale one would import the old
// release folder, or whatever sits there now, instead of the new download.
func TestQueueGrab_ReuseClearsStaleImportPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status models.DownloadState
	}{
		{"importBlocked", models.StateImportBlocked},
		{"orphaned import", models.StateImported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, downloads, clients, _, ctx := queueFixture(t)
			indexerURL, _ := regrabDownloadClient(t, clients)
			old := &models.Download{
				GUID: "guid-2289-path", Title: "Old Release", NZBURL: indexerURL + "/old.nzb",
				Status: tc.status, Protocol: "usenet",
			}
			if err := downloads.Create(ctx, old); err != nil {
				t.Fatal(err)
			}
			if err := downloads.SetImportPath(ctx, old.ID, "/downloads/Old Release"); err != nil {
				t.Fatal(err)
			}

			rec := regrabPost(h, `{"guid":"guid-2289-path","nzbUrl":"`+indexerURL+`/new.nzb","title":"New Release"}`)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("grab: got %d: %s", rec.Code, rec.Body.String())
			}
			got, err := downloads.GetByGUID(ctx, "guid-2289-path")
			if err != nil || got == nil {
				t.Fatalf("reload download: %v", err)
			}
			if got.ID != old.ID {
				t.Fatalf("expected the grab to reuse row %d, got %d", old.ID, got.ID)
			}
			if got.ImportPath != "" {
				t.Errorf("the new grab must not inherit the old import_path, got %q", got.ImportPath)
			}
		})
	}
}

// regrabUsers creates two users and returns their ids.
func regrabUsers(t *testing.T, database *sql.DB) (alice, bob int64) {
	t.Helper()
	users := db.NewUserRepo(database)
	a, err := users.Create(context.Background(), "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	b, err := users.Create(context.Background(), "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	return a.ID, b.ID
}

// regrabOwnedBook is regrabBook with the book stamped to owner.
func regrabOwnedBook(t *testing.T, database *sql.DB, books *db.BookRepo, slug string, owner int64) *models.Book {
	t.Helper()
	book := regrabBook(t, database, books, slug)
	if _, err := database.ExecContext(context.Background(), "UPDATE books SET owner_user_id=? WHERE id=?", owner, book.ID); err != nil {
		t.Fatalf("stamp book owner: %v", err)
	}
	book.OwnerUserID = owner
	return book
}
