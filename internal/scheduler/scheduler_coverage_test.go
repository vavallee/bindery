package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
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

// TestWithHistory verifies the optional history repo is assigned.
func TestWithHistory(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	s := &Scheduler{}
	h := db.NewHistoryRepo(database)
	s.WithHistory(h)
	if s.history != h {
		t.Fatal("WithHistory did not assign history repo")
	}
}

// TestWithCalibreSyncer verifies the calibre syncer is stored.
func TestWithCalibreSyncer(t *testing.T) {
	s := &Scheduler{}
	cs := &stubCalibreSyncer{}
	s.WithCalibreSyncer(cs)
	if s.calibreSyncer != cs {
		t.Fatal("WithCalibreSyncer did not assign")
	}
}

// TestWithRecommender verifies the recommender engine is stored.
func TestWithRecommender(t *testing.T) {
	s := &Scheduler{}
	r := &stubRecommender{}
	s.WithRecommender(r)
	if s.recommender != r {
		t.Fatal("WithRecommender did not assign")
	}
}

// TestWithHardcoverSyncer verifies the hardcover syncer is stored.
func TestWithHardcoverSyncer(t *testing.T) {
	s := &Scheduler{}
	hc := &stubHCSyncer{}
	s.WithHardcoverSyncer(hc)
	if s.hcSyncer != hc {
		t.Fatal("WithHardcoverSyncer did not assign")
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

// TestSearchWanted_AutoGrabEnabledExplicit verifies the path where the setting
// exists but is set to "true" — must continue past the kill-switch check.
func TestSearchWanted_AutoGrabEnabledExplicit(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	_ = settings.Set(ctx, "autoGrab.enabled", "true")

	s := &Scheduler{
		books:    db.NewBookRepo(database),
		settings: settings,
	}
	// No wanted books → early-return on empty result; exercises the "enabled" branch.
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

// TestCheckStalledDownloads_NoActive verifies the early-return when there are
// no active downloads.
func TestCheckStalledDownloads_NoActive(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	s := &Scheduler{
		downloads: db.NewDownloadRepo(database),
		settings:  db.NewSettingsRepo(database),
	}
	s.checkStalledDownloads(context.Background())
}

// TestCheckStalledDownloads_WithTimeoutSetting verifies the configurable
// stall.timeout_minutes setting is parsed.
func TestCheckStalledDownloads_WithTimeoutSetting(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	_ = settings.Set(ctx, "stall.timeout_minutes", "30")

	s := &Scheduler{
		downloads: db.NewDownloadRepo(database),
		settings:  settings,
	}
	s.checkStalledDownloads(ctx)
}

// TestCheckStalledDownloads_InvalidTimeoutSetting verifies that a bad setting
// value falls back to the default without panicking.
func TestCheckStalledDownloads_InvalidTimeoutSetting(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	_ = settings.Set(ctx, "stall.timeout_minutes", "not-a-number")

	s := &Scheduler{
		downloads: db.NewDownloadRepo(database),
		settings:  settings,
	}
	s.checkStalledDownloads(ctx)
}

// TestCheckStalledDownloads_NotYetOldEnough seeds a downloading record with a
// very recent grabbed_at timestamp so it is skipped by the cutoff check.
func TestCheckStalledDownloads_NotYetOldEnough(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)

	// Create a downloading record. Create sets AddedAt but not GrabbedAt; we
	// then UpdateStatus to "downloading" which sets grabbed_at=now.
	dl := &models.Download{
		GUID:     "recent-guid",
		Title:    "Recent",
		Status:   models.StateGrabbed,
		Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusDownloading); err != nil {
		t.Fatalf("update: %v", err)
	}

	s := &Scheduler{
		downloads: downloads,
		settings:  db.NewSettingsRepo(database),
		clients:   db.NewDownloadClientRepo(database),
	}
	// grabbed_at is "now", far after the cutoff — the record is skipped,
	// so no per-client stall check runs and we exit cleanly.
	s.checkStalledDownloads(ctx)
}

// TestCheckStalledDownloads_OldEnough_ReachesClientLookup seeds a downloading
// record with a backdated grabbed_at so the cutoff check passes, forcing the
// per-client grouping and GetByID call. The client is disabled, so the
// per-client loop hits the "continue" branch without any network I/O.
func TestCheckStalledDownloads_OldEnough_ReachesClientLookup(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)

	// Create a disabled client so GetByID returns, but the !client.Enabled
	// guard skips the network call to GetStalledIDs.
	client := &models.DownloadClient{
		Name: "test-qb", Type: "qbittorrent", Host: "localhost", Port: 8080,
		Enabled: false,
	}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatalf("client create: %v", err)
	}

	dl := &models.Download{
		GUID:             "old-guid",
		DownloadClientID: &client.ID,
		Title:            "Old Download",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("dl create: %v", err)
	}
	if err := downloads.UpdateStatus(ctx, dl.ID, models.StateDownloading); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Backdate grabbed_at so the record is older than the stall cutoff.
	old := time.Now().UTC().Add(-24 * time.Hour)
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET grabbed_at=? WHERE id=?", old, dl.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	torrentID := "abc123"
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET torrent_id=? WHERE id=?", torrentID, dl.ID); err != nil {
		t.Fatalf("set torrent id: %v", err)
	}

	s := &Scheduler{
		downloads: downloads,
		clients:   clients,
		settings:  db.NewSettingsRepo(database),
	}
	s.checkStalledDownloads(ctx)
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
	s.handleStalledDownload(ctx, dl)

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
	s.handleStalledDownload(ctx, dl)

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

// TestHandleStalledDownload_NilHistoryAndBlocklist verifies the handler is
// safe when optional repos are nil.
func TestHandleStalledDownload_NilHistoryAndBlocklist(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	dl := &models.Download{
		GUID:     "bare-guid",
		Title:    "Bare",
		Status:   models.DownloadStatusDownloading,
		Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	s := &Scheduler{
		downloads: downloads,
		// history nil, blocklist nil, books nil, settings nil.
	}
	s.handleStalledDownload(ctx, dl) // must not panic; BookID nil → returns after SetError
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
