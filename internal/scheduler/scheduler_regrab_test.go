package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

const regrabGUID = "g-2289"

type regrabFixture struct {
	s         *Scheduler
	database  *sql.DB
	downloads *db.DownloadRepo
	book      models.Book
	alice     int64
	bob       int64
	// adds counts every request the SABnzbd stub receives, so zero proves
	// nothing was sent.
	adds *atomic.Int32
}

// newRegrabFixture builds a DB-backed scheduler whose one wanted book belongs
// to bob and whose searcher always returns the same release. The usenet client
// is a SABnzbd stub, so an approved release goes all the way to downloading.
func newRegrabFixture(t *testing.T) *regrabFixture {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)
	adds := &atomic.Int32{}
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		adds.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-2289"}})
	}))
	t.Cleanup(sab.Close)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	downloads := db.NewDownloadRepo(database)
	indexers := db.NewIndexerRepo(database)

	a := &models.Author{
		ForeignID: "OL-REGRAB-A", Name: "Regrab Author", SortName: "Author, Regrab",
		MetadataProvider: "ol", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-REGRAB-B", AuthorID: a.ID, Title: "Regrab Book",
		SortTitle: "Regrab Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeEbook, OwnerUserID: bob.ID,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}

	u, err := url.Parse(sab.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: u.Hostname(), Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}
	idx := &models.Indexer{Name: "stub", Type: "newznab", URL: indexerSrv.URL, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}

	s := &Scheduler{
		searcher: &fixedResultsSearcher{results: []newznab.SearchResult{{
			GUID: regrabGUID, Title: "Regrab Book.epub", NZBURL: indexerSrv.URL + "/new.nzb",
			Protocol: "usenet", IndexerID: idx.ID,
		}}},
		indexers:  indexers,
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloads,
		clients:   clients,
		profiles:  db.NewMetadataProfileRepo(database),
	}
	return &regrabFixture{s: s, database: database, downloads: downloads, book: book,
		alice: alice.ID, bob: bob.ID, adds: adds}
}

// backdate moves one of a download's timestamp columns into the past. The
// scheduler's re-grab cooldown measures the row's last activity and there is
// no repo setter for added_at, so the tests write the column directly.
func (f *regrabFixture) backdate(t *testing.T, id int64, column string, when time.Time) {
	t.Helper()
	if _, err := f.database.Exec("UPDATE downloads SET "+column+"=? WHERE id=?", when.UTC(), id); err != nil {
		t.Fatalf("backdate %s: %v", column, err)
	}
}

// TestSearchAndGrabFormat_ReusesOrphanedImport is the scheduler half of #2289.
// A book was deleted after its release imported, then added back. The old row
// still holds the GUID with no book, and the scheduler treated any row for the
// GUID as "already grabbed", so automatic search stayed silent for good when
// that release ranked first. It must now reuse the row the way a manual grab
// does: linked to the re-added book, owned by that book's owner, and without
// the old grab's import_path.
func TestSearchAndGrabFormat_ReusesOrphanedImport(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	orphan := &models.Download{
		GUID: regrabGUID, OwnerUserID: f.alice, Title: "Old Release",
		NZBURL: "http://old.example/old.nzb", Status: models.StateImported, Protocol: "usenet",
	}
	if err := f.downloads.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err := f.downloads.SetImportPath(ctx, orphan.ID, "/downloads/Old Release"); err != nil {
		t.Fatal(err)
	}

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	if n := f.adds.Load(); n != 1 {
		t.Fatalf("#2289 regression: automatic search must grab a release whose earlier import lost its book; the client got %d requests", n)
	}
	rows, err := f.downloads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the orphaned row reused rather than a second row, got %d rows", len(rows))
	}
	got := rows[0]
	if got.ID != orphan.ID {
		t.Errorf("expected row %d reused, got %d", orphan.ID, got.ID)
	}
	if got.BookID == nil || *got.BookID != f.book.ID {
		t.Errorf("expected the reused row linked to book %d, got %v", f.book.ID, got.BookID)
	}
	if got.Status != models.StateDownloading {
		t.Errorf("expected the reused row downloading, got %q", got.Status)
	}
	if got.Title != "Regrab Book.epub" {
		t.Errorf("expected the release fields refreshed, got title %q", got.Title)
	}
	if got.ImportPath != "" {
		t.Errorf("expected the old import_path cleared, got %q", got.ImportPath)
	}
	owner, _, err := f.downloads.GetOwnerByID(ctx, orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner != f.bob {
		t.Errorf("expected the reused row owned by the book's owner (%d), got %d", f.bob, owner)
	}
}

// TestSearchAndGrabFormat_ReusesStaleFailedRow is #2710. Twenty five of the
// reporter's rows had failed months earlier on a loopback URL refusal and on
// indexer 429 and 500 responses, all long since fixed, and every automatic
// re-grab of those releases was dropped on the GUID alone. A finished attempt
// that has been idle past the cooldown must be reused, the way a manual grab
// reuses it, and the reused row must describe the new grab and nothing of the
// old one.
func TestSearchAndGrabFormat_ReusesStaleFailedRow(t *testing.T) {
	for _, status := range []models.DownloadState{models.StateFailed, models.StateImportBlocked} {
		t.Run(string(status), func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
			dead := &models.Download{
				GUID: regrabGUID, OwnerUserID: f.alice, Title: "Old Release",
				NZBURL: "http://old.example/old.nzb", Status: status, Protocol: "usenet",
				ErrorMessage: "fetch torrent: url not allowed: points to loopback address",
			}
			if err := f.downloads.Create(ctx, dead); err != nil {
				t.Fatal(err)
			}
			if err := f.downloads.SetImportPath(ctx, dead.ID, "/downloads/Old Release"); err != nil {
				t.Fatal(err)
			}
			// Failed three months ago, like the rows in the report.
			f.backdate(t, dead.ID, "added_at", time.Now().Add(-90*24*time.Hour))

			f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

			if n := f.adds.Load(); n != 1 {
				t.Fatalf("#2710: a release whose only row failed months ago must be grabbed again; the client got %d requests", n)
			}
			rows, err := f.downloads.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("expected the dead row reused rather than a second row, got %d rows", len(rows))
			}
			got := rows[0]
			if got.ID != dead.ID {
				t.Errorf("expected row %d reused, got %d", dead.ID, got.ID)
			}
			if got.Status != models.StateDownloading {
				t.Errorf("expected the reused row downloading, got %q", got.Status)
			}
			if got.BookID == nil || *got.BookID != f.book.ID {
				t.Errorf("expected the reused row linked to book %d, got %v", f.book.ID, got.BookID)
			}
			if got.Title != "Regrab Book.epub" || got.NZBURL == "http://old.example/old.nzb" {
				t.Errorf("expected the release fields refreshed, got title %q url %q", got.Title, got.NZBURL)
			}
			if got.ErrorMessage != "" {
				t.Errorf("expected the old failure message cleared, got %q", got.ErrorMessage)
			}
			if got.ImportPath != "" {
				t.Errorf("expected the old import_path cleared, got %q", got.ImportPath)
			}
			if got.SABnzbdNzoID == nil || *got.SABnzbdNzoID != "nzo-2289" {
				t.Errorf("expected the new client id recorded, got %v", got.SABnzbdNzoID)
			}
			owner, _, err := f.downloads.GetOwnerByID(ctx, dead.ID)
			if err != nil {
				t.Fatal(err)
			}
			if owner != f.bob {
				t.Errorf("expected the reused row owned by the book's owner (%d), got %d", f.bob, owner)
			}
		})
	}
}

// TestSearchAndGrabFormat_StillSkipsKnownRelease pins what the scheduler must
// keep refusing, and that every refusal says so. Live work is a row that is in
// flight or imported into a book that still exists. A row that died seconds
// ago is refused too, for a different reason: a release that fails at the
// client fails again the moment it is re-sent, so the cooldown keeps the sweep
// from looping on it (#2710).
func TestSearchAndGrabFormat_StillSkipsKnownRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   models.DownloadState
		withBook bool
		reason   string
	}{
		{"failed seconds ago", models.StateFailed, false, "failed too recently"},
		{"importBlocked seconds ago", models.StateImportBlocked, false, "failed too recently"},
		{"imported with its book", models.StateImported, true, "already grabbed"},
		{"downloading without a book", models.StateDownloading, false, "already grabbed"},
		{"import failed, still being retried", models.StateImportFailed, false, "already grabbed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
			logs := captureLogs(t)
			dl := &models.Download{
				GUID: regrabGUID, Title: "Old Release", NZBURL: "http://old.example/old.nzb",
				Status: tc.status, Protocol: "usenet",
			}
			if tc.withBook {
				dl.BookID = &f.book.ID
			}
			if err := f.downloads.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}

			f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

			if n := f.adds.Load(); n != 0 {
				t.Errorf("a %s row must not be grabbed again by the scheduler; the client got %d requests", tc.name, n)
			}
			got, err := f.downloads.GetByID(ctx, dl.ID)
			if err != nil || got == nil {
				t.Fatalf("reload download: %v", err)
			}
			if got.Status != tc.status || got.Title != "Old Release" {
				t.Errorf("expected the row untouched, got status=%q title=%q", got.Status, got.Title)
			}
			// #2710: the skip used to be silent, after an "auto-grabbing book"
			// line that read as though the grab had gone ahead. The reporter
			// asked for the GUID and the blocking row's status.
			out := logs.String()
			for _, want := range []string{regrabGUID, string(tc.status), tc.reason} {
				if !strings.Contains(out, want) {
					t.Errorf("the skip must be visible in the logs: %q missing from\n%s", want, out)
				}
			}
			if !strings.Contains(out, `outcome="`+tc.reason+" ("+string(tc.status)+`)"`) {
				t.Errorf("the search outcome must name the reason and the blocking status, got\n%s", out)
			}
		})
	}
}
