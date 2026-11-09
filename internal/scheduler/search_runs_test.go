package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/searchruns"
)

func runCtx(store *searchruns.Store, owner, bookID int64) (context.Context, string) {
	id := store.Start(owner, bookID, "book")
	return searchruns.WithRunID(context.Background(), id), id
}

// A search that finds nothing finishes its run with the same outcome the
// "book search finished" line carries (#3133).
func TestSearchAndGrabBook_FinishesItsRunWithTheLoggedOutcome(t *testing.T) {
	s := newEmptyScheduler(t)
	store := searchruns.NewStore(10, searchruns.DefaultTTL)
	s.WithSearchRuns(store)
	ctx, id := runCtx(store, 3, 42)

	if r, _ := store.Get(id, 0); r.State != searchruns.StateQueued {
		t.Fatalf("a registered run starts queued, got %s", r.State)
	}
	s.SearchAndGrabBook(ctx, models.Book{ID: 42, Title: "Dune", MediaType: models.MediaTypeEbook, OwnerUserID: 3})

	r, ok := store.Get(id, 3)
	if !ok {
		t.Fatal("run must still be readable")
	}
	if r.State != searchruns.StateFinished || r.Outcome != "no results" || r.FinishedAt == nil {
		t.Fatalf("want finished/no results, got %+v", r)
	}
	if r.Grabbed != nil {
		t.Fatalf("nothing was grabbed: %+v", r.Grabbed)
	}
}

// The kill switch returns before any search, which used to leave no trace a
// caller could poll. The run must still finish, and say why.
func TestSearchAndGrabBook_FinishesItsRunWhenAutoGrabIsOff(t *testing.T) {
	s := newEmptyScheduler(t)
	store := searchruns.NewStore(10, searchruns.DefaultTTL)
	s.WithSearchRuns(store)
	if err := s.settings.Set(context.Background(), "autoGrab.enabled", "false"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ctx, id := runCtx(store, 0, 1)

	s.SearchAndGrabBook(ctx, models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook})

	r, _ := store.Get(id, 0)
	if r.State != searchruns.StateFinished || r.Outcome != "auto-grab disabled" {
		t.Fatalf("want finished/auto-grab disabled, got %+v", r)
	}
}

// A book that wants no format never reaches the search; the run still ends.
func TestSearchAndGrabBook_FinishesItsRunWhenNoFormatIsNeeded(t *testing.T) {
	s := newEmptyScheduler(t)
	store := searchruns.NewStore(10, searchruns.DefaultTTL)
	s.WithSearchRuns(store)
	ctx, id := runCtx(store, 0, 1)

	s.SearchAndGrabBook(ctx, models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook, EbookFilePath: "/lib/dune.epub"})

	if r, _ := store.Get(id, 0); r.State != searchruns.StateFinished || r.Outcome != searchruns.OutcomeNotSearched {
		t.Fatalf("want finished/%q, got %+v", searchruns.OutcomeNotSearched, r)
	}
}

// A run records the grabbed release and the counts, and a "both" book is one
// run, not one per format.
func TestSearchAndGrabBook_RunRecordsTheGrab(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ebook.nzb" {
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	defer srv.Close()
	host, port := stallServerHostPort(t, srv.URL)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	indexers := db.NewIndexerRepo(database)

	a := &models.Author{ForeignID: "OL-RUN-A", Name: "Zogarth", SortName: "Zogarth", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	book := models.Book{
		ForeignID: "OL-RUN-B", AuthorID: a.ID, Title: "The Primal Hunter", SortTitle: "Primal Hunter",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeBoth,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatal(err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true, Category: "e", CategoryAudiobook: "a",
	}); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{Name: "stub", Type: "newznab", URL: srv.URL, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{
		searcher: &perFormatSearcher{byFormat: map[string][]newznab.SearchResult{
			models.MediaTypeEbook: {{
				GUID: "g-ebook", Title: "Zogarth The Primal Hunter epub", NZBURL: srv.URL + "/ebook.nzb",
				Protocol: "usenet", IndexerID: idx.ID, IndexerName: "stub", Size: 2 << 20,
			}},
		}},
		indexers: indexers, authors: authors, settings: db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database), downloads: db.NewDownloadRepo(database),
		clients: clients, profiles: db.NewMetadataProfileRepo(database),
	}
	store := searchruns.NewStore(10, searchruns.DefaultTTL)
	s.WithSearchRuns(store)
	rctx, id := runCtx(store, 0, book.ID)

	s.SearchAndGrabBook(rctx, book)

	r, _ := store.Get(id, 0)
	if r.State != searchruns.StateFinished || r.Outcome != "grabbed" {
		t.Fatalf("want finished/grabbed, got %+v", r)
	}
	if r.Grabbed == nil || r.Grabbed.Title != "Zogarth The Primal Hunter epub" || r.Grabbed.MediaType != models.MediaTypeEbook || r.Grabbed.Indexer != "stub" {
		t.Fatalf("grabbed release not recorded: %+v", r.Grabbed)
	}
	if r.Indexers != 2 || r.RawResults != 1 || r.Approved < 1 {
		t.Fatalf("counts should merge both format legs (2 indexers, 1 raw result): %+v", r)
	}
	if store.Len() != 1 {
		t.Fatalf("a dual format book is one run, store holds %d", store.Len())
	}
}

// A search whose context carries no run id registers nothing: nobody holds an
// id to poll, and the sweep would otherwise push callers' runs out.
func TestSearchAndGrabBook_WithoutARunIDRegistersNothing(t *testing.T) {
	s := newEmptyScheduler(t)
	store := searchruns.NewStore(10, searchruns.DefaultTTL)
	s.WithSearchRuns(store)
	s.SearchAndGrabBook(context.Background(), models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook})
	if store.Len() != 0 {
		t.Fatalf("want no runs, got %d", store.Len())
	}
}
