package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// TestScheduler_ReadErrorsFailSafe drives every read on a closed database and
// checks each helper fails in the direction its doc comment promises: the
// kill switch fails open, filters fall back to "none", and the sweep and
// stall paths stop rather than acting on missing state.
func TestScheduler_ReadErrorsFailSafe(t *testing.T) {
	database := closedDB(t)
	ctx := context.Background()
	logs := captureLogs(t)

	s := &Scheduler{
		searcher:  &fixedResultsSearcher{},
		authors:   db.NewAuthorRepo(database),
		books:     db.NewBookRepo(database),
		indexers:  db.NewIndexerRepo(database),
		downloads: db.NewDownloadRepo(database),
		clients:   db.NewDownloadClientRepo(database),
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		pending:   db.NewPendingReleaseRepo(database),
		history:   db.NewHistoryRepo(database),
	}

	if !s.autoGrabEnabled(ctx) {
		t.Error("autoGrabEnabled must fail open on a settings read error")
	}
	if got := s.loadPreferredLanguage(ctx); got != "" {
		t.Errorf("loadPreferredLanguage on a read error: got %q, want no filter", got)
	}
	if sweep := s.newSweepContext(ctx); sweep != nil {
		t.Errorf("newSweepContext must return nil when the indexer list fails, got %+v", sweep)
	}
	if q := s.wantedSearchQueue(ctx); q != nil {
		t.Errorf("wantedSearchQueue on a read error: got %v, want nil", q)
	}
	if m := s.inFlightFormatsByBook(ctx); len(m) != 0 {
		t.Errorf("inFlightFormatsByBook on a read error: got %v, want empty", m)
	}
	if got := s.checkPendingReleases(ctx, models.Book{ID: 1}, models.MediaTypeEbook, decision.New()); got != nil {
		t.Errorf("checkPendingReleases on a read error: got %+v, want nil", got)
	}

	s.storePending(ctx, 1, models.MediaTypeEbook, newznab.SearchResult{GUID: "g-store", Title: "T.epub", IndexerID: 3}, "delay not met")
	s.searchAndGrabFormat(ctx, models.Book{ID: 1, Title: "Closed"}, models.MediaTypeEbook, nil)
	s.searchWanted()
	s.refreshMetadata()
	s.checkStalledDownloads(ctx)

	bookID, indexerID := int64(5), int64(6)
	s.handleStalledDownload(ctx, &models.Download{ID: 9, GUID: "g-stall", BookID: &bookID, IndexerID: &indexerID},
		nil, downloader.StallClientReported)
	s.bgWg.Wait()

	out := logs.String()
	for _, want := range []string{
		"failed to store pending release",
		`outcome="indexer list failed"`,
		"failed to load auto-grab setting",
		"failed to list wanted books",
		"failed to list authors",
		"stall: failed to set error",
		"stall: failed to add to blocklist",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log %q on a read error; logs:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stall: triggering re-search") {
		t.Error("a stall whose book cannot be loaded must not start a re-search")
	}
}

// TestHandleStalledDownload_StallKind_DecidesBlocklistAndReSearches covers the
// whole stall handler on a working database: the download is failed, the
// stall and requeue history rows are written, a client reported stall
// blocklists the release while a no-metadata one does not, and the re-search
// it starts skips the stalled release and grabs the next one.
func TestHandleStalledDownload_StallKind_DecidesBlocklistAndReSearches(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	history := db.NewHistoryRepo(f.database)
	blocklist := db.NewBlocklistRepo(f.database)
	f.s.WithHistory(history)
	f.s.blocklist = blocklist
	f.s.books = db.NewBookRepo(f.database)

	idxs, err := f.s.indexers.List(ctx)
	if err != nil || len(idxs) != 1 {
		t.Fatalf("expected the fixture indexer, got %v %v", idxs, err)
	}
	idxID := idxs[0].ID

	mk := func(guid string) *models.Download {
		dl := &models.Download{
			GUID: guid, BookID: &f.book.ID, IndexerID: &idxID, Title: guid,
			NZBURL: "http://old.example/" + guid, Status: models.StateDownloading, Protocol: "usenet",
		}
		if err := f.downloads.Create(ctx, dl); err != nil {
			t.Fatal(err)
		}
		return dl
	}

	reported := mk("g-reported")
	f.s.handleStalledDownload(ctx, reported, nil, downloader.StallClientReported)
	f.s.bgWg.Wait()

	if n := f.adds.Load(); n != 1 {
		t.Fatalf("expected the re-search to grab the replacement release once, client got %d requests", n)
	}
	got, err := f.downloads.GetByGUID(ctx, "g-reported")
	if err != nil || got == nil {
		t.Fatalf("GetByGUID: %v %v", got, err)
	}
	if got.Status != models.StateFailed || got.ErrorMessage == "" {
		t.Errorf("expected the stalled download failed with a reason, got status %q msg %q", got.Status, got.ErrorMessage)
	}
	replacement, err := f.downloads.GetByGUID(ctx, regrabGUID)
	if err != nil || replacement == nil || replacement.Status != models.StateDownloading {
		t.Fatalf("expected the replacement release downloading, got %+v %v", replacement, err)
	}

	noMeta := mk("g-nometa")
	f.s.handleStalledDownload(ctx, noMeta, nil, downloader.StallNoMetadata)
	f.s.bgWg.Wait()
	if n := f.adds.Load(); n != 1 {
		t.Errorf("the replacement is already downloading, so the second re-search must not send again; client got %d", n)
	}

	entries, err := blocklist.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocked := map[string]bool{}
	for _, e := range entries {
		blocked[e.GUID] = true
	}
	if !blocked["g-reported"] {
		t.Error("a client reported stall must blocklist the release")
	}
	if blocked["g-nometa"] {
		t.Error("a no-metadata stall must not blocklist the release")
	}

	events, err := history.ListByBook(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, e := range events {
		count[e.EventType]++
	}
	if count[models.HistoryEventDownloadStalled] != 2 {
		t.Errorf("expected 2 stall history rows, got %d (%v)", count[models.HistoryEventDownloadStalled], count)
	}
	if count[models.HistoryEventDownloadRequeued] != 2 {
		t.Errorf("expected 2 requeue history rows, got %d (%v)", count[models.HistoryEventDownloadRequeued], count)
	}
	if count[models.HistoryEventGrabbed] != 1 {
		t.Errorf("expected 1 grabbed history row from the re-search, got %d (%v)", count[models.HistoryEventGrabbed], count)
	}

	// StallNone is rejected outright: nothing is failed.
	none := mk("g-none")
	f.s.handleStalledDownload(ctx, none, nil, downloader.StallNone)
	if got, _ := f.downloads.GetByGUID(ctx, "g-none"); got == nil || got.Status != models.StateDownloading {
		t.Errorf("StallNone must leave the download alone, got %+v", got)
	}
}

// TestSearchAndGrabFormat_GrabSideEffects covers a successful automatic grab
// with every optional collaborator attached: the author's alias reaches the
// search criteria, the grab is written to history with its media type,
// EventGrabbed fires, and only the grabbed format's pending entries are
// cleared.
func TestSearchAndGrabFormat_GrabSideEffects(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	history := db.NewHistoryRepo(f.database)
	pending := db.NewPendingReleaseRepo(f.database)
	aliases := db.NewAuthorAliasRepo(f.database)
	spy := &spyNotifier{}
	f.s.WithHistory(history)
	f.s.WithPendingReleases(pending)
	f.s.WithAliases(aliases)
	f.s.WithEditions(db.NewEditionRepo(f.database))
	f.s.WithNotifier(spy)

	if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: f.book.AuthorID, Name: "R. Author"}); err != nil {
		t.Fatal(err)
	}
	for _, pr := range []models.PendingRelease{
		{BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: "held ebook", GUID: "p-ebook", Protocol: "usenet", ReleaseJSON: "{}"},
		{BookID: f.book.ID, MediaType: models.MediaTypeAudiobook, Title: "held audio", GUID: "p-audio", Protocol: "usenet", ReleaseJSON: "{}"},
	} {
		pr := pr
		if err := pending.Upsert(ctx, &pr); err != nil {
			t.Fatal(err)
		}
	}

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

	if n := f.adds.Load(); n != 1 {
		t.Fatalf("expected one send to the client, got %d", n)
	}
	crit := f.s.searcher.(*fixedResultsSearcher).lastCrit
	if len(crit.AuthorAliases) != 1 || crit.AuthorAliases[0] != "R. Author" {
		t.Errorf("expected the author alias on the search criteria, got %v", crit.AuthorAliases)
	}

	events, err := history.ListByType(ctx, models.HistoryEventGrabbed)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one grabbed history row, got %d", len(events))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("history data not JSON: %v", err)
	}
	if data["guid"] != regrabGUID || data["mediaType"] != models.MediaTypeEbook {
		t.Errorf("unexpected history payload %v", data)
	}

	call := spy.lookup(notifierEventGrabbed)
	if call == nil || call.payload["title"] != "Regrab Book.epub" {
		t.Errorf("expected EventGrabbed with the release title, got %+v", call)
	}

	left, err := pending.ListByBook(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].GUID != "p-audio" {
		t.Errorf("expected only the audiobook pending entry to survive an ebook grab, got %+v", left)
	}

	rows, err := f.downloads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SABnzbdNzoID == nil || *rows[0].SABnzbdNzoID != "nzo-2289" {
		t.Errorf("expected the SABnzbd nzo id stored on the download, got %+v", rows)
	}
}

// TestSearchAndGrabFormat_AbortPaths covers two grabs that must stop before
// anything is sent: a release whose protocol has no enabled client (no
// cross-protocol fallback), and a dead-row reuse whose claim errors.
func TestSearchAndGrabFormat_AbortPaths(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	logs := captureLogs(t)
	searcher := f.s.searcher.(*fixedResultsSearcher)
	usenet := searcher.results

	torrent := usenet[0]
	torrent.GUID = "g-torrent"
	torrent.Protocol = "torrent"
	searcher.results = []newznab.SearchResult{torrent}
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 0 {
		t.Fatalf("a torrent release must not go to the usenet client, got %d sends", n)
	}
	if rows, _ := f.downloads.List(ctx); len(rows) != 0 {
		t.Fatalf("no download row may be created without a client, got %d", len(rows))
	}
	if !strings.Contains(logs.String(), `outcome="no download client for protocol"`) {
		t.Errorf("expected the no-client outcome logged; logs:\n%s", logs.String())
	}

	searcher.results = usenet
	prev := claimDeadRowForAutoGrab
	claimDeadRowForAutoGrab = func(context.Context, *db.DownloadRepo, *models.Download, time.Time) (bool, error) {
		return false, errors.New("database is locked")
	}
	t.Cleanup(func() { claimDeadRowForAutoGrab = prev })
	dead := &models.Download{
		GUID: regrabGUID, Title: "Old Release", NZBURL: "http://old.example/old.nzb",
		Status: models.StateFailed, Protocol: "usenet",
	}
	if err := f.downloads.Create(ctx, dead); err != nil {
		t.Fatal(err)
	}
	f.backdate(t, dead.ID, "dead_at", time.Now().Add(-90*24*time.Hour))

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 0 {
		t.Errorf("a failed claim must send nothing, got %d sends", n)
	}
	if got, _ := f.downloads.GetByGUID(ctx, regrabGUID); got == nil || got.Status != models.StateFailed || got.Title != "Old Release" {
		t.Errorf("a failed claim must leave the dead row untouched, got %+v", got)
	}
	if !strings.Contains(logs.String(), `outcome="download record failed"`) {
		t.Errorf("expected the download record failure logged; logs:\n%s", logs.String())
	}
}

// TestCheckPendingReleases_SkipsCorruptAndReturnsApproved: a pending row whose
// stored JSON is unreadable is skipped rather than aborting the scan, and the
// first approved release is returned and removed so it is not grabbed twice.
func TestCheckPendingReleases_SkipsCorruptAndReturnsApproved(t *testing.T) {
	f := newRegrabFixture(t)
	ctx := context.Background()
	pending := db.NewPendingReleaseRepo(f.database)
	f.s.WithPendingReleases(pending)

	good, err := json.Marshal(newznab.SearchResult{GUID: "p-good", Title: "Good.epub", Protocol: "usenet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range []models.PendingRelease{
		{BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: "Corrupt", GUID: "p-bad", Protocol: "usenet", ReleaseJSON: "{not json"},
		{BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: "Good.epub", GUID: "p-good", Protocol: "usenet", ReleaseJSON: string(good)},
	} {
		pr := pr
		if err := pending.Upsert(ctx, &pr); err != nil {
			t.Fatal(err)
		}
	}

	got := f.s.checkPendingReleases(ctx, f.book, models.MediaTypeEbook, decision.New())
	if got == nil || got.GUID != "p-good" {
		t.Fatalf("expected the readable pending release approved, got %+v", got)
	}
	left, err := pending.ListByBook(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].GUID != "p-bad" {
		t.Errorf("expected the approved entry deleted and the corrupt one kept, got %+v", left)
	}
	// Another format's pending list is independent.
	if got := f.s.checkPendingReleases(ctx, f.book, models.MediaTypeAudiobook, decision.New()); got != nil {
		t.Errorf("expected nothing pending for the audiobook format, got %+v", got)
	}
}

// TestWithStalledRelease_EmptyGUIDLeavesContext: an empty GUID marks nothing,
// so the re-search skips no release.
func TestWithStalledRelease_EmptyGUIDLeavesContext(t *testing.T) {
	ctx := context.Background()
	if got := withStalledRelease(ctx, ""); got != ctx {
		t.Error("an empty GUID must return the context unchanged")
	}
	if got := stalledReleaseFrom(withStalledRelease(ctx, "g-1")); got != "g-1" {
		t.Errorf("expected the GUID carried on the context, got %q", got)
	}
}
