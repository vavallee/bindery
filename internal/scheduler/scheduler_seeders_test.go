package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func seeds(n int) *int { return &n }

func grabbedAfterSearch(t *testing.T, results []newznab.SearchResult) []string {
	t.Helper()
	ctx := context.Background()
	s, downloads, _, book := freeleechFixture(t, false, results)
	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)
	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	var guids []string
	for _, d := range rows {
		guids = append(guids, d.GUID)
	}
	return guids
}

// The #3953 report: the ranking puts a dead torrent first, and the grab must
// pass over it for the seeded release behind it.
func TestSearchAndGrabFormat_SkipsZeroSeederTorrentForSeededOne(t *testing.T) {
	guids := grabbedAfterSearch(t, []newznab.SearchResult{
		{GUID: "g-dead", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/1", Protocol: "torrent", Seeders: seeds(0), Grabs: 5604},
		{GUID: "g-live", Title: "Ratio Book [EPUB]", NZBURL: "http://127.0.0.1:1/2", Protocol: "torrent", Seeders: seeds(13), Grabs: 16},
	})
	if len(guids) != 1 || guids[0] != "g-live" {
		t.Fatalf("grabbed %v, want only g-live", guids)
	}
}

// Only dead torrents: nothing is grabbed, nothing is parked, and the book
// stays wanted for a later search to find a seeded copy.
func TestSearchAndGrabFormat_OnlyZeroSeederTorrentsLeavesBookWanted(t *testing.T) {
	ctx := context.Background()
	s, downloads, pending, book := freeleechFixture(t, false, []newznab.SearchResult{
		{GUID: "g-dead", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/1", Protocol: "torrent", Seeders: seeds(0)},
	})
	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	if rows, _ := downloads.List(ctx); len(rows) != 0 {
		t.Fatalf("a dead torrent must not be grabbed, got %d download(s)", len(rows))
	}
	if held, _ := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeEbook); len(held) != 0 {
		t.Errorf("a dead torrent must not be parked in pending, got %d", len(held))
	}
}

// A count the indexer did not report is unknown, not zero.
func TestSearchAndGrabFormat_UnknownSeedersStillGrabbed(t *testing.T) {
	guids := grabbedAfterSearch(t, []newznab.SearchResult{
		{GUID: "g-unknown", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/1", Protocol: "torrent"},
	})
	if len(guids) != 1 {
		t.Fatalf("grabbed %v, want the release with no reported seeders", guids)
	}
}

// Usenet has no swarm, so a stray zero never blocks it.
func TestSearchAndGrabFormat_UsenetIgnoresSeeders(t *testing.T) {
	ctx := context.Background()
	s, downloads, _, book := freeleechFixture(t, false, []newznab.SearchResult{
		{GUID: "g-nzb", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/1", Protocol: "usenet", Seeders: seeds(0)},
	})
	// freeleechFixture only registers a torrent client; add the usenet one.
	if err := s.clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: "127.0.0.1", Port: 1, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}
	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)
	if rows, _ := downloads.List(ctx); len(rows) != 1 {
		t.Fatalf("a usenet release must be grabbed whatever Seeders says, got %d download(s)", len(rows))
	}
}

// The upgrade path shares the specification set: a dead EPUB does not replace
// a MOBI, a seeded one does.
func TestSearchWanted_UpgradeSkipsZeroSeederTorrent(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.mobi", []newznab.SearchResult{
		{GUID: "g-epub-dead", Title: "Upgrade Book EPUB", NZBURL: "http://127.0.0.1:1/d", Protocol: "torrent", Seeders: seeds(0)},
		{GUID: "g-epub-live", Title: "Upgrade Book EPUB 2", NZBURL: "http://127.0.0.1:1/l", Protocol: "torrent", Seeders: seeds(4)},
	})
	f.s.searchWanted()
	guids := f.grabbedGUIDs(t)
	if len(guids) != 1 || guids[0] != "g-epub-live" {
		t.Fatalf("grabbed %v, want only g-epub-live", guids)
	}
}
