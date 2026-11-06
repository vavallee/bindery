package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// countingSearcher answers every search with the same results and counts how
// many searches it was asked for.
type countingSearcher struct {
	results []newznab.SearchResult
	calls   int
}

func (s *countingSearcher) SearchBook(_ context.Context, _ []models.Indexer, _ indexer.MatchCriteria) []newznab.SearchResult {
	s.calls++
	return s.results
}

// upgradeSweepFixture is an imported ebook whose only file is a MOBI, for an
// author whose profile aims for EPUB (#2420).
type upgradeSweepFixture struct {
	s         *Scheduler
	searcher  *countingSearcher
	downloads *db.DownloadRepo
	books     *db.BookRepo
	book      models.Book
}

func newUpgradeSweepFixture(t *testing.T, upgradeAllowed bool, ownedFile string, results []newznab.SearchResult) *upgradeSweepFixture {
	t.Helper()
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
	profiles := db.NewQualityProfileRepo(database)

	profile := &models.QualityProfile{
		Name: "epub first", UpgradeAllowed: upgradeAllowed, Cutoff: "epub",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}, {Quality: "mobi", Allowed: true}},
	}
	if err := profiles.Create(ctx, profile); err != nil {
		t.Fatalf("profile create: %v", err)
	}
	a := &models.Author{
		ForeignID: "OL-UP-A", Name: "Upgrade Author", SortName: "Author, Upgrade",
		MetadataProvider: "ol", Monitored: true, QualityProfileID: &profile.ID,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-UP-B", AuthorID: a.ID, Title: "Upgrade Book", SortTitle: "Upgrade Book",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol",
		Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, filepath.Join(t.TempDir(), ownedFile)); err != nil {
		t.Fatalf("add file: %v", err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "qbit", Type: "qbittorrent", Host: "127.0.0.1", Port: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}
	idx := &models.Indexer{Name: "tracker", Type: "torznab", URL: "http://127.0.0.1:1", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}
	for i := range results {
		results[i].IndexerID = idx.ID
	}
	searcher := &countingSearcher{results: results}
	s := &Scheduler{
		searcher:        searcher,
		indexers:        indexers,
		authors:         authors,
		books:           books,
		settings:        db.NewSettingsRepo(database),
		blocklist:       db.NewBlocklistRepo(database),
		downloads:       downloads,
		clients:         clients,
		profiles:        db.NewMetadataProfileRepo(database),
		qualityProfiles: profiles,
	}
	got, err := books.GetByID(ctx, book.ID)
	if err != nil || got == nil {
		t.Fatalf("reload book: %v", err)
	}
	if got.Status != models.BookStatusImported {
		t.Fatalf("book status = %q, want imported", got.Status)
	}
	return &upgradeSweepFixture{s: s, searcher: searcher, downloads: downloads, books: books, book: *got}
}

func epubAndMobiReleases() []newznab.SearchResult {
	return []newznab.SearchResult{
		{GUID: "g-mobi", Title: "Upgrade Book MOBI", NZBURL: "http://127.0.0.1:1/m", Protocol: "torrent"},
		{GUID: "g-epub", Title: "Upgrade Book EPUB", NZBURL: "http://127.0.0.1:1/e", Protocol: "torrent"},
	}
}

func (f *upgradeSweepFixture) grabbedGUIDs(t *testing.T) []string {
	t.Helper()
	rows, err := f.downloads.List(context.Background())
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	var guids []string
	for _, d := range rows {
		guids = append(guids, d.GUID)
	}
	return guids
}

// The acceptance case: a MOBI-only book under an EPUB cutoff is searched once
// per cycle and the EPUB, not the MOBI it already has, is grabbed.
func TestSearchWanted_UpgradesAMobiOnlyBookToEpub(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.mobi", epubAndMobiReleases())

	f.s.searchWanted()

	if f.searcher.calls != 1 {
		t.Fatalf("searches in one cycle = %d, want 1", f.searcher.calls)
	}
	guids := f.grabbedGUIDs(t)
	if len(guids) != 1 || guids[0] != "g-epub" {
		t.Fatalf("grabbed %v, want only g-epub", guids)
	}
}

// A release that is no better than the owned file is not an upgrade, however
// the search ranks it.
func TestSearchWanted_UpgradeIgnoresReleasesThatDoNotBeatTheOwnedFile(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.mobi", []newznab.SearchResult{
		{GUID: "g-mobi", Title: "Upgrade Book MOBI", NZBURL: "http://127.0.0.1:1/m", Protocol: "torrent"},
	})

	f.s.searchWanted()

	if f.searcher.calls != 1 {
		t.Fatalf("searches = %d, want 1", f.searcher.calls)
	}
	if guids := f.grabbedGUIDs(t); len(guids) != 0 {
		t.Fatalf("grabbed %v, want nothing", guids)
	}
}

// Default for every existing profile: upgrades off means no search at all.
func TestSearchWanted_NoUpgradeSearchWhenTheProfileDoesNotAllowIt(t *testing.T) {
	f := newUpgradeSweepFixture(t, false, "Upgrade Book.mobi", epubAndMobiReleases())

	f.s.searchWanted()

	if f.searcher.calls != 0 {
		t.Fatalf("searches = %d, want 0 with upgrades off", f.searcher.calls)
	}
}

// A book already at the cutoff is left alone.
func TestSearchWanted_NoUpgradeSearchAtTheCutoff(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.epub", epubAndMobiReleases())

	f.s.searchWanted()

	if f.searcher.calls != 0 {
		t.Fatalf("searches = %d, want 0 for a book at the cutoff", f.searcher.calls)
	}
}

// An eligible book with no better release is searched once, then left alone
// until the cooldown ends, so it is not searched every sweep.
func TestSearchWanted_UpgradeSearchHasAPerBookCooldown(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.mobi", []newznab.SearchResult{
		{GUID: "g-mobi", Title: "Upgrade Book MOBI", NZBURL: "http://127.0.0.1:1/m", Protocol: "torrent"},
	})

	f.s.searchWanted()
	f.s.searchWanted()
	if f.searcher.calls != 1 {
		t.Fatalf("searches over two sweeps = %d, want 1", f.searcher.calls)
	}

	f.s.upgradeSearchedMu.Lock()
	f.s.upgradeSearched[f.book.ID] = time.Now().Add(-upgradeSearchCooldown - time.Minute)
	f.s.upgradeSearchedMu.Unlock()
	f.s.searchWanted()
	if f.searcher.calls != 2 {
		t.Fatalf("searches after the cooldown = %d, want 2", f.searcher.calls)
	}
}

// No profile allowing upgrades means the queue is empty without reading books.
func TestUpgradeSearchQueue_EmptyWhenNoProfileAllowsUpgrades(t *testing.T) {
	f := newUpgradeSweepFixture(t, false, "Upgrade Book.mobi", epubAndMobiReleases())
	if q := f.s.upgradeSearchQueue(context.Background()); len(q) != 0 {
		t.Fatalf("queue = %d, want 0", len(q))
	}
}

// A grab already working toward an import is not searched again.
func TestUpgradeSearchQueue_SkipsABookWithAGrabInFlight(t *testing.T) {
	f := newUpgradeSweepFixture(t, true, "Upgrade Book.mobi", epubAndMobiReleases())
	if q := f.s.upgradeSearchQueue(context.Background()); len(q) != 1 {
		t.Fatalf("queue = %d, want 1", len(q))
	}
	dl := &models.Download{
		GUID: "g-inflight", BookID: &f.book.ID, Title: "Upgrade Book EPUB",
		Quality: "epub", Status: models.StateGrabbed, Protocol: "torrent",
	}
	if err := f.downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	if q := f.s.upgradeSearchQueue(context.Background()); len(q) != 0 {
		t.Fatalf("queue = %d with a grab in flight, want 0", len(q))
	}
}
