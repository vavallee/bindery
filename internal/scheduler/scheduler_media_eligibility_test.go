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

// eligibilityFixture builds a DB-backed scheduler with one wanted book, one
// enabled usenet indexer, and the SABnzbd clients the caller supplies. Each
// stub SAB server counts its own requests and reports success or failure per
// clientOK, so a test can tell which client the scheduler actually used.
func eligibilityFixture(t *testing.T, clientSpecs []struct {
	name                 string
	priority             int
	enabledForBooks      bool
	enabledForAudiobooks bool
	ok                   bool
}) (s *Scheduler, downloads *db.DownloadRepo, book models.Book, calls map[string]*atomic.Int32) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	downloadsRepo := db.NewDownloadRepo(database)
	indexers := db.NewIndexerRepo(database)

	a := &models.Author{
		ForeignID: "OL-ELIG-A", Name: "Eligibility Author", SortName: "Author, Eligibility",
		MetadataProvider: "ol", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	b := models.Book{
		ForeignID: "OL-ELIG-B", AuthorID: a.ID, Title: "Eligibility Book",
		SortTitle: "Eligibility Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeAudiobook,
	}
	if err := books.Create(ctx, &b); err != nil {
		t.Fatalf("book create: %v", err)
	}

	calls = make(map[string]*atomic.Int32)
	for _, spec := range clientSpecs {
		count := &atomic.Int32{}
		calls[spec.name] = count
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			count.Add(1)
			if !spec.ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-" + spec.name}})
		}))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal(err)
		}
		if err := clients.Create(ctx, &models.DownloadClient{
			Name: spec.name, Type: "sabnzbd", Host: u.Hostname(), Port: port,
			Priority: spec.priority, Enabled: true,
			EnabledForBooks:      spec.enabledForBooks,
			EnabledForAudiobooks: spec.enabledForAudiobooks,
		}); err != nil {
			t.Fatalf("client create %s: %v", spec.name, err)
		}
	}

	idx := &models.Indexer{Name: "stub", Type: "newznab", URL: indexerSrv.URL, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}

	s = &Scheduler{
		searcher: &fixedResultsSearcher{results: []newznab.SearchResult{{
			GUID: "g-eligibility", Title: "Eligibility Book.m4b", NZBURL: indexerSrv.URL + "/new.nzb",
			Protocol: "usenet", IndexerID: idx.ID,
		}}},
		indexers:  indexers,
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloadsRepo,
		clients:   clients,
		profiles:  db.NewMetadataProfileRepo(database),
	}
	return s, downloadsRepo, b, calls
}

// TestSearchAndGrabFormat_SkipsClientIneligibleForMediaType verifies that an
// audiobook auto-grab never reaches a client that opted out of audiobooks,
// even though it is otherwise enabled.
func TestSearchAndGrabFormat_SkipsClientIneligibleForMediaType(t *testing.T) {
	s, downloads, book, calls := eligibilityFixture(t, []struct {
		name                 string
		priority             int
		enabledForBooks      bool
		enabledForAudiobooks bool
		ok                   bool
	}{
		{name: "book-only", priority: 1, enabledForBooks: true, enabledForAudiobooks: false, ok: true},
		{name: "audio-eligible", priority: 2, enabledForBooks: true, enabledForAudiobooks: true, ok: true},
	})

	s.searchAndGrabFormat(context.Background(), book, models.MediaTypeAudiobook, nil)

	if calls["book-only"].Load() != 0 {
		t.Errorf("expected the book-only client never to be contacted, got %d calls", calls["book-only"].Load())
	}
	if calls["audio-eligible"].Load() != 1 {
		t.Errorf("expected the audiobook-eligible client to receive the grab, got %d calls", calls["audio-eligible"].Load())
	}
	rows, err := downloads.List(context.Background())
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != models.StateDownloading {
		t.Fatalf("expected one downloading row, got %+v", rows)
	}
}

// TestSearchAndGrabFormat_DoesNotRetryAmbiguousSendFailure ensures a failed
// client response cannot cause the same release to be submitted elsewhere.
func TestSearchAndGrabFormat_DoesNotRetryAmbiguousSendFailure(t *testing.T) {
	s, downloads, book, calls := eligibilityFixture(t, []struct {
		name                 string
		priority             int
		enabledForBooks      bool
		enabledForAudiobooks bool
		ok                   bool
	}{
		{name: "failing", priority: 1, enabledForBooks: true, enabledForAudiobooks: true, ok: false},
		{name: "working", priority: 2, enabledForBooks: true, enabledForAudiobooks: true, ok: true},
	})

	s.searchAndGrabFormat(context.Background(), book, models.MediaTypeAudiobook, nil)

	if calls["failing"].Load() != 1 || calls["working"].Load() != 0 {
		t.Fatalf("expected only the highest-ranked client to be attempted, got failing=%d working=%d", calls["failing"].Load(), calls["working"].Load())
	}
	rows, err := downloads.List(context.Background())
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != models.StateFailed {
		t.Fatalf("expected one errored row after the selected client's failure, got %+v", rows)
	}
}

// TestSearchAndGrabFormat_NoEligibleClientForMediaType verifies that an
// audiobook auto-grab with only book-eligible clients configured produces no
// download record, rather than being silently sent somewhere wrong.
func TestSearchAndGrabFormat_NoEligibleClientForMediaType(t *testing.T) {
	s, downloads, book, calls := eligibilityFixture(t, []struct {
		name                 string
		priority             int
		enabledForBooks      bool
		enabledForAudiobooks bool
		ok                   bool
	}{
		{name: "book-only", priority: 1, enabledForBooks: true, enabledForAudiobooks: false, ok: true},
	})

	s.searchAndGrabFormat(context.Background(), book, models.MediaTypeAudiobook, nil)

	if calls["book-only"].Load() != 0 {
		t.Errorf("expected no client to be contacted, got %d calls", calls["book-only"].Load())
	}
	rows, err := downloads.List(context.Background())
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no download record when no client is eligible, got %+v", rows)
	}
}

func TestSearchAndGrabFormat_SkipsProtocolWithoutEligibleClient(t *testing.T) {
	s, downloads, book, calls := eligibilityFixture(t, []struct {
		name                 string
		priority             int
		enabledForBooks      bool
		enabledForAudiobooks bool
		ok                   bool
	}{
		{name: "usenet-book-only", priority: 1, enabledForBooks: true, enabledForAudiobooks: false, ok: true},
	})

	transmissionCalls := 0
	transmissionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		transmissionCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    "success",
			"arguments": map[string]any{"torrent-added": map[string]any{"id": 7, "name": "Audiobook"}},
		})
	}))
	defer transmissionServer.Close()
	serverURL, err := url.Parse(transmissionServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(serverURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	torrentClient := &models.DownloadClient{
		Name: "torrent-audio", Type: "transmission", Host: serverURL.Hostname(), Port: port,
		Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true,
	}
	if err := s.clients.Create(ctx, torrentClient); err != nil {
		t.Fatalf("create torrent client: %v", err)
	}
	indexers, err := s.indexers.List(ctx)
	if err != nil || len(indexers) == 0 {
		t.Fatalf("list indexers: %v", err)
	}
	s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{
		{GUID: "g-usenet", Title: "Eligibility Book.m4b", NZBURL: "https://example.test/a.nzb", Protocol: "usenet", IndexerID: indexers[0].ID},
		{GUID: "g-torrent", Title: "Eligibility Book.m4b", NZBURL: "magnet:?xt=urn:btih:audio", Protocol: "torrent", IndexerID: indexers[0].ID},
	}}

	s.searchAndGrabFormat(ctx, book, models.MediaTypeAudiobook, nil)

	if calls["usenet-book-only"].Load() != 0 {
		t.Errorf("ineligible usenet client received %d calls", calls["usenet-book-only"].Load())
	}
	if transmissionCalls != 1 {
		t.Errorf("eligible torrent client calls = %d, want 1", transmissionCalls)
	}
	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 || rows[0].Protocol != "torrent" || rows[0].Status != models.StateDownloading {
		t.Fatalf("expected the eligible torrent result to be grabbed, got %+v", rows)
	}
}
