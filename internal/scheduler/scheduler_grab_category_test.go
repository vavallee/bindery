package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// perFormatSearcher answers each format leg with its own releases, the way a
// real search of the 7xxx and 3xxx category trees would.
type perFormatSearcher struct {
	byFormat map[string][]newznab.SearchResult
}

func (s *perFormatSearcher) SearchBook(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) []newznab.SearchResult {
	return s.byFormat[c.MediaType]
}

// TestSearchAndGrabBook_DualFormatUsesEachFormatsCategory pins the automatic
// half of #2933. The report was against the interactive combined search, and
// asked whether the automatic search files a dual-format book's audiobook
// under the ebook category too. It does not: each format is searched and sent
// on its own leg with a concrete media type, and this keeps it that way.
func TestSearchAndGrabBook_DualFormatUsesEachFormatsCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	var mu sync.Mutex
	sent := map[string]string{} // nzbname -> category
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".nzb") {
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
			return
		}
		if r.URL.Query().Get("mode") == "addfile" {
			mu.Lock()
			sent[r.URL.Query().Get("nzbname")] = r.URL.Query().Get("cat")
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-" + r.URL.Query().Get("nzbname")}})
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
	downloads := db.NewDownloadRepo(database)
	indexers := db.NewIndexerRepo(database)

	a := &models.Author{
		ForeignID: "OL-CAT-A", Name: "Zogarth", SortName: "Zogarth",
		MetadataProvider: "ol", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-CAT-B", AuthorID: a.ID, Title: "The Primal Hunter",
		SortTitle: "Primal Hunter", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeBoth,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
		Category: "readarr-ebook", CategoryAudiobook: "readarr-audio",
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}
	idx := &models.Indexer{Name: "stub", Type: "newznab", URL: srv.URL, Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}

	searcher := &perFormatSearcher{byFormat: map[string][]newznab.SearchResult{
		models.MediaTypeEbook: {{
			GUID: "g-ebook", Title: "Zogarth The Primal Hunter epub", NZBURL: srv.URL + "/ebook.nzb",
			Protocol: "usenet", IndexerID: idx.ID, Size: 2 << 20,
		}},
		models.MediaTypeAudiobook: {{
			GUID: "g-audio", Title: "Zogarth The Primal Hunter m4b", NZBURL: srv.URL + "/audio.nzb",
			Protocol: "usenet", IndexerID: idx.ID, Size: 800 << 20,
		}},
	}}
	s := &Scheduler{
		searcher:  searcher,
		indexers:  indexers,
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloads,
		clients:   clients,
		profiles:  db.NewMetadataProfileRepo(database),
	}

	s.SearchAndGrabBook(ctx, book)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		names := make([]string, 0, len(sent))
		for k := range sent {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Fatalf("expected both formats dispatched, got %d: %v", len(sent), names)
	}
	if got := sent["Zogarth The Primal Hunter m4b"]; got != "readarr-audio" {
		t.Errorf("audiobook leg category = %q, want readarr-audio", got)
	}
	if got := sent["Zogarth The Primal Hunter epub"]; got != "readarr-ebook" {
		t.Errorf("ebook leg category = %q, want readarr-ebook", got)
	}
}
