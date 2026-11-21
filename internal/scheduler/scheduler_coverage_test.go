package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// stubCalibreSyncer is a no-op CalibreSyncer for wiring tests.
type stubCalibreSyncer struct{ called int }

func (s *stubCalibreSyncer) RunSync(_ context.Context) { s.called++ }

// stubRecommender is a no-op RecommendationEngine.
type stubRecommender struct{ called int }

func (s *stubRecommender) Run(_ context.Context, _ int64) error {
	s.called++
	return nil
}

// stubHCSyncer is a no-op HCListSyncer.
type stubHCSyncer struct{ called int }

func (s *stubHCSyncer) Sync(_ context.Context) error {
	s.called++
	return nil
}

// TestWithHistory checks the repo WithHistory wires is the one the scheduler
// records into (#1846): a stalled download it gives up on leaves a history
// event there. Pinning the stored pointer instead would pass with the
// recording removed and fail on a harmless wrapper.
func TestWithHistory(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	dl := &models.Download{GUID: "history-guid", Title: "Stalled Release", Status: models.DownloadStatusDownloading, Protocol: "torrent"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	s := &Scheduler{downloads: downloads, blocklist: db.NewBlocklistRepo(database)}
	h := db.NewHistoryRepo(database)
	s.WithHistory(h)
	s.handleStalledDownload(ctx, dl, nil, downloader.StallClientReported)

	events, err := h.List(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no history recorded through the repo WithHistory wired")
	}
}

// TestOptionalSyncerJobsRunTheWiredSyncers checks each optional syncer wired
// through its setter is the one its scheduled job calls (#1846): the Calibre
// sync, the recommender and the Hardcover list sync each run once when their
// job fires. Pinning the stored instance instead would still pass if the
// scheduler stopped invoking them.
func TestOptionalSyncerJobsRunTheWiredSyncers(t *testing.T) {
	// The base jobs Start always registers come first, so the optional ones
	// are the entries after them. Counting them from a bare scheduler keeps
	// this test from hardcoding the number.
	bare := &Scheduler{cron: cron.New(cron.WithSeconds())}
	bare.Start()
	base := len(bare.cron.Entries())
	bare.Stop()

	cs, rec, hc := &stubCalibreSyncer{}, &stubRecommender{}, &stubHCSyncer{}
	s := &Scheduler{cron: cron.New(cron.WithSeconds())}
	s.WithCalibreSyncer(cs)
	s.WithRecommender(rec)
	s.WithHardcoverSyncer(hc)
	s.Start()
	defer s.Stop()

	ran := 0
	for _, e := range s.cron.Entries() {
		if int(e.ID) > base {
			e.Job.Run()
			ran++
		}
	}
	if ran != 3 {
		t.Fatalf("ran %d optional jobs, want 3 (one per wired syncer)", ran)
	}
	if cs.called != 1 || rec.called != 1 || hc.called != 1 {
		t.Errorf("calls: calibre %d, recommender %d, hardcover %d; want 1 each", cs.called, rec.called, hc.called)
	}
}

// TestStart_WithAllOptionalSyncers verifies that Start registers the additional
// cron entries for each optional syncer (calibre, recommender, hardcover).
func TestStart_WithAllOptionalSyncers(t *testing.T) {
	s := &Scheduler{
		cron:          cron.New(cron.WithSeconds()),
		calibreSyncer: &stubCalibreSyncer{},
		recommender:   &stubRecommender{},
		hcSyncer:      &stubHCSyncer{},
	}
	s.Start()
	defer s.Stop()

	// 5 base jobs + 3 optional jobs = 8.
	if got, want := len(s.cron.Entries()), 8; got != want {
		t.Errorf("expected %d entries with all optional syncers, got %d", want, got)
	}
}

// TestSearchWanted_AutoGrabDisabled verifies the autoGrab kill-switch short-circuits.
func TestSearchWanted_AutoGrabDisabled(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	_ = settings.Set(ctx, "autoGrab.enabled", "false")

	s := &Scheduler{
		books:    db.NewBookRepo(database),
		settings: settings,
	}
	// Should return without touching books; setting nil searcher would panic otherwise.
	s.searchWanted()
}

// TestRefreshMetadata_SkipsCalibreForeignID verifies that authors whose
// ForeignID has the "calibre:" prefix are skipped before any metadata call.
func TestRefreshMetadata_SkipsCalibreForeignID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := db.NewAuthorRepo(database)
	_ = repo.Create(ctx, &models.Author{
		ForeignID:        "calibre:author:42",
		Name:             "Calibre Author",
		SortName:         "Calibre Author",
		MetadataProvider: "calibre",
		Monitored:        true,
	})

	// meta is nil; hitting GetAuthor would panic, so the calibre-skip guard
	// must run before any provider call.
	s := &Scheduler{authors: repo, meta: nil}
	s.refreshMetadata()
}

// TestRefreshMetadata_SkipsManualForeignID is the same guard for an author made
// by hand (#3840): no provider answers for a "manual:" id, so the reload guard
// must stop it before any provider call.
func TestRefreshMetadata_SkipsManualForeignID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	repo := db.NewAuthorRepo(database)
	_ = repo.Create(context.Background(), &models.Author{
		ForeignID:        "manual:author:dan-abnett",
		Name:             "Dan Abnett",
		SortName:         "Abnett, Dan",
		MetadataProvider: models.ManualMetadataProvider,
		Monitored:        true,
	})

	s := &Scheduler{authors: repo, meta: nil}
	s.refreshMetadata()
}

func TestHasCalibrePrefix(t *testing.T) {
	for id, want := range map[string]bool{
		"calibre:author:1": true, "manual:author:x": true, "MANUAL:author:x": true, "OL1A": false, "": false,
	} {
		if got := hasCalibrePrefix(id); got != want {
			t.Errorf("hasCalibrePrefix(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestHandleStalledDownload_NoBookID exercises the early-exit when a download
// has no associated book.
func TestHandleStalledDownload_NoBookID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	dl := &models.Download{
		GUID:     "orphan-guid",
		Title:    "Orphan Stalled",
		Status:   models.DownloadStatusDownloading,
		Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	s := &Scheduler{
		downloads: downloads,
		blocklist: db.NewBlocklistRepo(database),
		history:   db.NewHistoryRepo(database),
	}
	// BookID is nil → handler sets error, records history, skips re-search.
	s.handleStalledDownload(ctx, dl, nil, downloader.StallClientReported)

	// Confirm the download was marked failed.
	got, err := downloads.GetByGUID(ctx, "orphan-guid")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ErrorMessage == "" {
		t.Error("expected error_message to be set")
	}
}

// TestHandleStalledDownload_AutoGrabDisabled verifies the re-search is skipped
// when autoGrab.enabled=false, even with a valid book.
func TestHandleStalledDownload_AutoGrabDisabled(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	downloads := db.NewDownloadRepo(database)
	settings := db.NewSettingsRepo(database)

	a := &models.Author{ForeignID: "OL-AS", Name: "A", SortName: "A", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := &models.Book{
		ForeignID: "OL-BS", AuthorID: a.ID, Title: "Stalled Book",
		SortTitle: "Stalled Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("book create: %v", err)
	}

	_ = settings.Set(ctx, "autoGrab.enabled", "false")

	dl := &models.Download{
		GUID:     "stalled-guid",
		BookID:   &book.ID,
		Title:    "Stalled Release",
		Status:   models.DownloadStatusDownloading,
		Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("dl create: %v", err)
	}

	s := &Scheduler{
		downloads: downloads,
		blocklist: db.NewBlocklistRepo(database),
		history:   db.NewHistoryRepo(database),
		books:     books,
		settings:  settings,
	}
	s.handleStalledDownload(ctx, dl, nil, downloader.StallClientReported)

	// Download should be marked with an error and book lookup path executed,
	// then the autoGrab=false early-return should have prevented the re-search.
	got, err := downloads.GetByGUID(ctx, "stalled-guid")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ErrorMessage == "" {
		t.Error("expected error_message after stall handling")
	}
}

// TestRefreshMetadata_CalibreAggregatorNotCalled is a sanity check that the
// metadata.Aggregator constructor works and Scheduler wiring matches.
func TestRefreshMetadata_UsesAggregator(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	// Zero authors → no aggregator calls.
	agg := metadata.NewAggregator(&mockMetaProvider{})
	s := &Scheduler{
		authors: db.NewAuthorRepo(database),
		meta:    agg,
	}
	s.refreshMetadata()

	// Use time import so future assertions can reference it if added.
	_ = time.Now()
}

// stubCalibreDeliverer counts passes.
type stubCalibreDeliverer struct{ runs int }

func (s *stubCalibreDeliverer) RunDeliveries(_ context.Context) { s.runs++ }

// TestWithCalibreDeliverer_RegistersAMinuteJob: the delivery job runs every
// minute when a deliverer is wired, which is what delivers a book imported
// while Calibre was closed once it is back (#2832). No deliverer, no job.
func TestWithCalibreDeliverer_RegistersAMinuteJob(t *testing.T) {
	s := &Scheduler{cron: cron.New(cron.WithSeconds())}
	s.WithCalibreDeliverer(nil)
	if n := len(s.cron.Entries()); n != 0 {
		t.Fatalf("nil deliverer registered %d jobs", n)
	}
	d := &stubCalibreDeliverer{}
	s.WithCalibreDeliverer(d)
	entries := s.cron.Entries()
	if len(entries) != 1 || !hasEntryWithDelay(entries, time.Minute) {
		t.Fatalf("entries = %d, want one job every minute", len(entries))
	}
	entries[0].Job.Run()
	if d.runs != 1 {
		t.Fatalf("runs = %d, want the job to run a delivery pass", d.runs)
	}
}
