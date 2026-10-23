package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// pinSAB is a fake SABnzbd that also serves any *.nzb path and counts addfile
// calls, so a test can say which of two clients a grab reached.
type pinSAB struct {
	mu   sync.Mutex
	adds int
	srv  *httptest.Server
}

func newPinSAB(t *testing.T) *pinSAB {
	t.Helper()
	p := &pinSAB{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".nzb") {
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
			return
		}
		if r.URL.Query().Get("mode") == "addfile" {
			p.mu.Lock()
			p.adds++
			p.mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pinSAB) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adds
}

type schedPinFixture struct {
	s         *Scheduler
	ctx       context.Context
	clients   *db.DownloadClientRepo
	indexers  *db.IndexerRepo
	downloads *db.DownloadRepo
	history   *db.HistoryRepo
	database  *sql.DB
	book      models.Book
	pub, priv *pinSAB
	pubC      *models.DownloadClient
	privC     *models.DownloadClient
	idx       *models.Indexer
}

func newSchedPinFixture(t *testing.T) *schedPinFixture {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	pub, priv, rel := newPinSAB(t), newPinSAB(t), newPinSAB(t)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	clients := db.NewDownloadClientRepo(database)
	downloads := db.NewDownloadRepo(database)
	indexers := db.NewIndexerRepo(database)
	history := db.NewHistoryRepo(database)

	a := &models.Author{ForeignID: "OL-PIN-A", Name: "Pin Author", SortName: "Author, Pin", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-PIN-B", AuthorID: a.ID, Title: "Pinned Book", SortTitle: "Pinned Book",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}

	pubHost, pubPort := stallServerHostPort(t, pub.srv.URL)
	pubC := &models.DownloadClient{Name: "sab-public", Type: "sabnzbd", Host: pubHost, Port: pubPort, Enabled: true, Priority: 1}
	if err := clients.Create(ctx, pubC); err != nil {
		t.Fatalf("client create: %v", err)
	}
	privHost, privPort := stallServerHostPort(t, priv.srv.URL)
	privC := &models.DownloadClient{Name: "sab-private", Type: "sabnzbd", Host: privHost, Port: privPort, Enabled: true, Priority: 50}
	if err := clients.Create(ctx, privC); err != nil {
		t.Fatalf("client create: %v", err)
	}
	idx := &models.Indexer{Name: "TrackerX", Type: "newznab", URL: rel.srv.URL, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}

	searcher := &perFormatSearcher{byFormat: map[string][]newznab.SearchResult{
		models.MediaTypeEbook: {{
			GUID: "g-pin", Title: "Pin Author Pinned Book epub", NZBURL: rel.srv.URL + "/pin.nzb",
			Protocol: "usenet", IndexerID: idx.ID, IndexerName: "TrackerX", Size: 2 << 20,
		}},
	}}
	s := &Scheduler{
		searcher:  searcher,
		indexers:  indexers,
		authors:   authors,
		books:     books,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloads,
		clients:   clients,
		history:   history,
		profiles:  db.NewMetadataProfileRepo(database),
	}
	return &schedPinFixture{
		s: s, database: database, ctx: ctx, clients: clients, indexers: indexers, downloads: downloads, history: history,
		book: book, pub: pub, priv: priv, pubC: pubC, privC: privC, idx: idx,
	}
}

func (f *schedPinFixture) pin(t *testing.T, clientID int64) {
	t.Helper()
	f.idx.DownloadClientID = &clientID
	if err := f.indexers.Update(f.ctx, f.idx); err != nil {
		t.Fatalf("pin: %v", err)
	}
}

// TestAutoGrab_UnpinnedIndexerRoutesAsBefore is the regression guard.
func TestAutoGrab_UnpinnedIndexerRoutesAsBefore(t *testing.T) {
	f := newSchedPinFixture(t)
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if f.pub.count() != 1 || f.priv.count() != 0 {
		t.Fatalf("unpinned: public=%d private=%d, want 1 and 0", f.pub.count(), f.priv.count())
	}
}

// TestAutoGrab_PinnedIndexerGoesToItsClient: the pin beats priority, and the
// download row records the pinned client.
func TestAutoGrab_PinnedIndexerGoesToItsClient(t *testing.T) {
	f := newSchedPinFixture(t)
	f.pin(t, f.privC.ID)
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("pinned: public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
	dl, err := f.downloads.GetByGUID(f.ctx, "g-pin")
	if err != nil || dl == nil {
		t.Fatalf("download row: %v", err)
	}
	if dl.DownloadClientID == nil || *dl.DownloadClientID != f.privC.ID {
		t.Errorf("DownloadClientID = %v, want %d", dl.DownloadClientID, f.privC.ID)
	}
}

// TestAutoGrab_PinnedClientDisabledFailsWithoutFallback: nothing reaches the
// other client, and History says why, naming indexer and client.
func TestAutoGrab_PinnedClientDisabledFailsWithoutFallback(t *testing.T) {
	f := newSchedPinFixture(t)
	f.pin(t, f.privC.ID)
	f.privC.Enabled = false
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("a grab with its pinned client disabled reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-pin"); dl != nil {
		t.Errorf("a download row was created for a grab that never happened: %+v", dl)
	}
	events, err := f.history.ListByBook(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType == models.HistoryEventDownloadFailed && strings.Contains(e.Data, "TrackerX") && strings.Contains(e.Data, "disabled") {
			found = true
		}
	}
	if !found {
		t.Errorf("no downloadFailed history event naming the indexer and the disabled client: %+v", events)
	}
}

// TestAutoGrab_PinnedClientWrongProtocolFails: a client whose type was changed
// to the other protocol after the pin is refused at grab time.
func TestAutoGrab_PinnedClientWrongProtocolFails(t *testing.T) {
	f := newSchedPinFixture(t)
	f.pin(t, f.privC.ID)
	f.privC.Type = "qbittorrent"
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
}
