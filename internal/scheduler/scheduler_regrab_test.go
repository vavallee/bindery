package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

const regrabGUID = "g-2289"

type regrabFixture struct {
	s         *Scheduler
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
		EnabledForBooks: true, EnabledForAudiobooks: true,
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
	return &regrabFixture{s: s, downloads: downloads, book: book, alice: alice.ID, bob: bob.ID, adds: adds}
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

// TestSearchAndGrabFormat_StillSkipsKnownRelease pins what the scheduler must
// keep refusing. A failed or blocked row is a release that already went wrong
// once; a user clicking Grab may try it again, but the scheduler would pick it
// on every sweep and loop on it. An imported row with its book, and anything
// in flight, is live work.
func TestSearchAndGrabFormat_StillSkipsKnownRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   models.DownloadState
		withBook bool
	}{
		{"failed", models.StateFailed, false},
		{"importBlocked", models.StateImportBlocked, false},
		{"imported with its book", models.StateImported, true},
		{"downloading without a book", models.StateDownloading, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
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
		})
	}
}
