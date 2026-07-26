package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// freeleechFixture builds a DB-backed scheduler with one torrent indexer whose
// freeleech-only policy is set by the caller, plus an enabled torrent client so
// an approved release proceeds as far as a download record. The client points
// at 127.0.0.1:1 so the send fails fast; the download row is created first,
// which is the observable.
func freeleechFixture(t *testing.T, freeleechOnly bool, results []newznab.SearchResult) (
	*Scheduler, *db.DownloadRepo, *db.PendingReleaseRepo, models.Book,
) {
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
	pending := db.NewPendingReleaseRepo(database)

	a := &models.Author{
		ForeignID: "OL-FL-A", Name: "Ratio Author", SortName: "Author, Ratio",
		MetadataProvider: "ol", Monitored: true,
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := models.Book{
		ForeignID: "OL-FL-B", AuthorID: a.ID, Title: "Ratio Book",
		SortTitle: "Ratio Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, &book); err != nil {
		t.Fatalf("book create: %v", err)
	}
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "qbit", Type: "qbittorrent", Host: "127.0.0.1", Port: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("client create: %v", err)
	}

	idx := &models.Indexer{
		Name: "private-tracker", Type: "torznab", URL: "http://127.0.0.1:1",
		Enabled: true, FreeleechOnly: freeleechOnly,
	}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("indexer create: %v", err)
	}
	for i := range results {
		results[i].IndexerID = idx.ID
	}

	s := &Scheduler{
		searcher:  &fixedResultsSearcher{results: results},
		indexers:  indexers,
		authors:   authors,
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		downloads: downloads,
		clients:   clients,
		profiles:  db.NewMetadataProfileRepo(database),
		pending:   pending,
	}
	return s, downloads, pending, book
}

func ratio(v float64) *float64 { return &v }

// TestSearchAndGrabFormat_HoldsNonFreeleechForApproval is the core of cleb's
// request: on an indexer restricted to freeleech, a ratio-costing release must
// NOT be auto-grabbed, and must NOT be silently dropped either — it lands in
// pending_releases so the user can approve it by hand.
func TestSearchAndGrabFormat_HoldsNonFreeleechForApproval(t *testing.T) {
	ctx := context.Background()
	s, downloads, pending, book := freeleechFixture(t, true, []newznab.SearchResult{{
		GUID: "g-normal", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/1",
		Protocol: "torrent", DownloadVolumeFactor: ratio(1),
	}})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a ratio-costing release must not be auto-grabbed, got %d download(s)", len(rows))
	}

	held, err := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeEbook)
	if err != nil {
		t.Fatalf("pending list: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("expected the release to be held for manual approval, got %d pending entr(ies)", len(held))
	}
	if !strings.Contains(held[0].Reason, decision.RejectionFreeleechHold) {
		t.Errorf("pending reason = %q, want it to carry the %q sentinel so the UI explains why",
			held[0].Reason, decision.RejectionFreeleechHold)
	}
}

// TestSearchAndGrabFormat_GrabsFreeleech is the control: a freeleech release
// costs no ratio and is auto-grabbed as normal.
func TestSearchAndGrabFormat_GrabsFreeleech(t *testing.T) {
	ctx := context.Background()
	s, downloads, pending, book := freeleechFixture(t, true, []newznab.SearchResult{{
		GUID: "g-free", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/2",
		Protocol: "torrent", DownloadVolumeFactor: ratio(0),
	}})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("a freeleech release must be grabbed normally, got %d download(s)", len(rows))
	}
	held, _ := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeEbook)
	if len(held) != 0 {
		t.Errorf("a grabbed freeleech release must not also be left pending, got %d", len(held))
	}
}

// TestSearchAndGrabFormat_PolicyOffGrabsNonFreeleech confirms the feature is
// strictly opt-in: with the policy off, the same ratio-costing release is
// grabbed exactly as before.
func TestSearchAndGrabFormat_PolicyOffGrabsNonFreeleech(t *testing.T) {
	ctx := context.Background()
	s, downloads, _, book := freeleechFixture(t, false, []newznab.SearchResult{{
		GUID: "g-normal-off", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/3",
		Protocol: "torrent", DownloadVolumeFactor: ratio(1),
	}})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("with the policy off nothing should change, got %d download(s)", len(rows))
	}
}

// TestSearchAndGrabFormat_HeldReleaseStaysHeldOnResweep guards the trap in
// checkPendingReleases: it re-evaluates pending entries every sweep and grabs
// anything that starts passing. A ratio hold must keep failing, or the release
// the user was asked to approve gets auto-grabbed on the next cycle anyway.
func TestSearchAndGrabFormat_HeldReleaseStaysHeldOnResweep(t *testing.T) {
	ctx := context.Background()
	s, downloads, pending, book := freeleechFixture(t, true, []newznab.SearchResult{{
		GUID: "g-normal", Title: "Ratio Book.epub", NZBURL: "http://127.0.0.1:1/4",
		Protocol: "torrent", DownloadVolumeFactor: ratio(1),
	}})

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)
	// Second sweep: the pending entry is re-evaluated with the same specs.
	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)

	rows, err := downloads.List(ctx)
	if err != nil {
		t.Fatalf("downloads list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a held release must not be auto-grabbed off the pending queue on a later sweep, got %d download(s)", len(rows))
	}
	held, _ := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeEbook)
	if len(held) != 1 {
		t.Errorf("the release should still be waiting for manual approval, got %d pending entr(ies)", len(held))
	}
}

// TestFreeleechOnlyIndexerIDs covers the set builder, including the nil return
// that keeps the specification out of the decision path entirely when nobody
// uses the policy.
func TestFreeleechOnlyIndexerIDs(t *testing.T) {
	if got := freeleechOnlyIndexerIDs(nil); got != nil {
		t.Errorf("no indexers should yield a nil set, got %v", got)
	}
	if got := freeleechOnlyIndexerIDs([]models.Indexer{{ID: 1}, {ID: 2}}); got != nil {
		t.Errorf("no flagged indexers should yield a nil set, got %v", got)
	}
	got := freeleechOnlyIndexerIDs([]models.Indexer{
		{ID: 1}, {ID: 2, FreeleechOnly: true}, {ID: 3, FreeleechOnly: true},
	})
	if len(got) != 2 || !got[2] || !got[3] || got[1] {
		t.Errorf("expected exactly indexers 2 and 3 flagged, got %v", got)
	}
}
