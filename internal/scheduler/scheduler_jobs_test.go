package scheduler

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// mockMetaProvider implements metadata.Provider for scheduler tests.
// It always returns the configured author/book without any network calls.
type mockMetaProvider struct {
	author *models.Author
	book   *models.Book
}

func (m *mockMetaProvider) Name() string { return "mock" }
func (m *mockMetaProvider) SearchAuthors(_ context.Context, _ string) ([]models.Author, error) {
	if m.author != nil {
		return []models.Author{*m.author}, nil
	}
	return nil, nil
}
func (m *mockMetaProvider) SearchBooks(_ context.Context, _ string) ([]models.Book, error) {
	if m.book != nil {
		return []models.Book{*m.book}, nil
	}
	return nil, nil
}
func (m *mockMetaProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return m.author, nil
}
func (m *mockMetaProvider) GetBook(_ context.Context, _ string) (*models.Book, error) {
	return m.book, nil
}
func (m *mockMetaProvider) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return nil, nil
}
func (m *mockMetaProvider) GetBookByISBN(_ context.Context, _ string) (*models.Book, error) {
	return m.book, nil
}

// TestNew_Construction verifies that New() populates all fields correctly.
func TestNew_Construction(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	indexers := db.NewIndexerRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	settings := db.NewSettingsRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	s := New(context.Background(), nil, nil, nil, authors, books, indexers, downloads, clients, settings, blocklist)
	if s == nil {
		t.Fatal("New returned nil")
		return
	}
	if s.cron == nil {
		t.Fatal("cron field not initialized")
	}
	if s.appCtx == nil {
		t.Fatal("appCtx field not initialized")
	}
	if s.authors != authors {
		t.Error("authors repo not assigned")
	}
	if s.books != books {
		t.Error("books repo not assigned")
	}
}

// TestStart_RegistersBaseJobs verifies that Start() registers the expected
// number of cron entries and that Stop() completes cleanly. No jobs fire
// during this test because all intervals are ≥15 s and we stop immediately.
func TestStart_RegistersBaseJobs(t *testing.T) {
	s := &Scheduler{
		cron: cron.New(cron.WithSeconds()),
		// All other fields are nil; closures capture them but are not called
		// before Stop() returns since intervals are 15 s or longer.
	}
	s.Start()
	entries := s.cron.Entries()
	s.Stop()

	// Base jobs: check-downloads (15s), stall-detection (5m), search-wanted (12h),
	// refresh-metadata (24h), scan-library (6h).
	const wantJobs = 5
	if len(entries) != wantJobs {
		t.Errorf("expected %d cron entries after Start(), got %d", wantJobs, len(entries))
	}
}

// hasEntryWithDelay reports whether any cron entry runs on a
// ConstantDelaySchedule (@every) with exactly the given delay. Used to assert
// the search-wanted job is registered at the resolved interval.
func hasEntryWithDelay(entries []cron.Entry, d time.Duration) bool {
	for _, e := range entries {
		if cds, ok := e.Schedule.(cron.ConstantDelaySchedule); ok && cds.Delay == d {
			return true
		}
	}
	return false
}

// schedulerWithSearchInterval builds a Scheduler backed by an in-memory DB and
// seeds (or omits) the search.interval setting. value=="" leaves the setting
// unset. A nil settings repo is requested with seedSettings=false.
func schedulerWithSearchInterval(t *testing.T, seedSettings bool, value string) *Scheduler {
	t.Helper()
	s := &Scheduler{cron: cron.New(cron.WithSeconds())}
	if seedSettings {
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		repo := db.NewSettingsRepo(database)
		if value != "" {
			if err := repo.Set(context.Background(), "search.interval", value); err != nil {
				t.Fatalf("seed search.interval: %v", err)
			}
		}
		s.settings = repo
	}
	return s
}

// TestStart_SearchWantedUsesConfiguredInterval verifies the search-wanted cron
// entry honours a valid configured search.interval rather than the default.
func TestStart_SearchWantedUsesConfiguredInterval(t *testing.T) {
	s := schedulerWithSearchInterval(t, true, "48h")
	s.Start()
	entries := s.cron.Entries()
	s.Stop()

	if !hasEntryWithDelay(entries, 48*time.Hour) {
		t.Error("expected a cron entry at the configured 48h interval, found none")
	}
}

// TestStart_SearchWantedFallsBackToDefault verifies the search-wanted entry
// uses defaultSearchInterval when settings is nil and when the setting is unset.
func TestStart_SearchWantedFallsBackToDefault(t *testing.T) {
	cases := []struct {
		name  string
		seed  bool
		value string
	}{
		{"nil settings repo", false, ""},
		{"empty/unset setting", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := schedulerWithSearchInterval(t, tc.seed, tc.value)
			s.Start()
			entries := s.cron.Entries()
			s.Stop()
			if !hasEntryWithDelay(entries, defaultSearchInterval) {
				t.Errorf("expected search-wanted entry at default %s, found none", defaultSearchInterval)
			}
		})
	}
}

// TestStart_SearchWantedRejectsOutOfBounds verifies garbage and out-of-range
// search.interval values fall back to the default (mirroring the API bounds).
func TestStart_SearchWantedRejectsOutOfBounds(t *testing.T) {
	for _, value := range []string{"not-a-duration", "30m", "200h"} {
		t.Run(value, func(t *testing.T) {
			s := schedulerWithSearchInterval(t, true, value)
			s.Start()
			entries := s.cron.Entries()
			s.Stop()
			if !hasEntryWithDelay(entries, defaultSearchInterval) {
				t.Errorf("value %q should fall back to default %s", value, defaultSearchInterval)
			}
		})
	}
}

// TestResolveSearchInterval covers the helper directly: unset/nil falls back,
// valid in-range values pass through, and out-of-bounds/unparseable values fall
// back to the default.
func TestResolveSearchInterval(t *testing.T) {
	if got := schedulerWithSearchInterval(t, false, "").resolveSearchInterval(); got != defaultSearchInterval {
		t.Errorf("nil settings: got %s, want %s", got, defaultSearchInterval)
	}
	if got := schedulerWithSearchInterval(t, true, "").resolveSearchInterval(); got != defaultSearchInterval {
		t.Errorf("unset setting: got %s, want %s", got, defaultSearchInterval)
	}
	if got := schedulerWithSearchInterval(t, true, "6h").resolveSearchInterval(); got != 6*time.Hour {
		t.Errorf("valid 6h: got %s, want 6h", got)
	}
	for _, bad := range []string{"30m", "200h", "garbage"} {
		if got := schedulerWithSearchInterval(t, true, bad).resolveSearchInterval(); got != defaultSearchInterval {
			t.Errorf("invalid %q: got %s, want default %s", bad, got, defaultSearchInterval)
		}
	}
}

// TestStop_IdempotentAfterStart confirms that calling Stop right after Start
// does not hang or panic.
func TestStop_IdempotentAfterStart(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s := &Scheduler{
			cron: cron.New(cron.WithSeconds()),
		}
		s.Start()
		s.Stop()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5 seconds")
	}
}

// TestStart_EntrySchedules verifies that each cron entry has a valid next-fire
// time in the future (i.e. all four AddFunc calls completed successfully).
func TestStart_EntrySchedules(t *testing.T) {
	s := &Scheduler{
		cron: cron.New(cron.WithSeconds()),
	}
	s.Start()
	entries := s.cron.Entries()
	now := time.Now()
	for i, e := range entries {
		if e.Next.IsZero() || e.Next.Before(now) {
			t.Errorf("entry %d has invalid next-run time: %v", i, e.Next)
		}
	}
	s.Stop()
}

// TestSearchWanted_EmptyDB exercises the early-return path when there are no
// wanted books. Safe to call with nil scanner/searcher/meta because the loop
// is skipped.
func TestSearchWanted_EmptyDB(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	s := &Scheduler{
		books: db.NewBookRepo(database),
	}
	// Must not panic; returns immediately when no wanted books.
	s.searchWanted()
}

func TestRunBoundedBookTasks_LimitsConcurrency(t *testing.T) {
	books := make([]models.Book, 6)
	for i := range books {
		books[i] = models.Book{Title: "Book " + strconv.Itoa(i)}
	}

	var mu sync.Mutex
	var calls, active, maxActive int
	concurrency.RunBounded(context.Background(), books, 2, func(_ context.Context, _ models.Book) {
		mu.Lock()
		calls++
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		active--
		mu.Unlock()
	})

	if calls != len(books) {
		t.Fatalf("calls = %d, want %d", calls, len(books))
	}
	if maxActive > 2 {
		t.Fatalf("maxActive = %d, want <= 2", maxActive)
	}
	if maxActive < 2 {
		t.Fatalf("expected parallel execution, maxActive = %d", maxActive)
	}
}

// TestSearchWanted_ErrorPath exercises the error-return when the books repo
// is in a broken state. We use a nil pointer to trigger a panic, so instead
// we call the method on a scheduler with a valid (but empty) books repo that
// is asked for a status that returns zero rows. The goal is to hit the
// "len(wanted) == 0" branch.
func TestSearchWanted_NoBooksInStatus(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	s := &Scheduler{
		books: db.NewBookRepo(database),
	}
	// Seeding a book with non-wanted status means the wanted query returns empty.
	authRepo := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "ol", Monitored: true}
	_ = authRepo.Create(context.Background(), a)
	_ = s.books.Create(context.Background(), &models.Book{
		ForeignID: "OL1W", AuthorID: a.ID, Title: "Imported Book",
		SortTitle: "Imported Book", Status: models.BookStatusImported,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	})

	s.searchWanted() // no panic; exits early because no "wanted" books
}

// TestWantedSearchQueue_SkipsInFlightGrabs is the regression guard for the
// double-grab bug: a Wanted book with a grab already downloading must be left
// out of the search queue (else the next sweep grabs a second release for it),
// while a Wanted book whose only download died (import-failed) stays IN the
// queue so a different release can be tried.
func TestWantedSearchQueue_SkipsInFlightGrabs(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)

	a := &models.Author{ForeignID: "OL9A", Name: "A", SortName: "A", MetadataProvider: "ol", Monitored: true}
	if err := authRepo.Create(ctx, a); err != nil {
		t.Fatalf("create author: %v", err)
	}
	mkBook := func(fid, title string) *models.Book {
		b := &models.Book{
			ForeignID: fid, AuthorID: a.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("create book %s: %v", title, err)
		}
		return b
	}
	inFlight := mkBook("OL-INFLIGHT", "Being Downloaded")
	dead := mkBook("OL-DEAD", "Import Failed")
	fresh := mkBook("OL-FRESH", "Never Grabbed")

	mkDownload := func(b *models.Book, st models.DownloadState) {
		if err := dlRepo.Create(ctx, &models.Download{
			GUID: "guid-" + b.ForeignID, BookID: &b.ID, Title: b.Title, Status: st, Protocol: "torrent",
		}); err != nil {
			t.Fatalf("create download for %s: %v", b.Title, err)
		}
	}
	mkDownload(inFlight, models.StateDownloading)
	mkDownload(dead, models.StateImportFailed)

	s := &Scheduler{books: bookRepo, downloads: dlRepo}
	queue := s.wantedSearchQueue(ctx)

	got := make(map[int64]bool, len(queue))
	for _, b := range queue {
		got[b.ID] = true
	}
	if got[inFlight.ID] {
		t.Errorf("book with a downloading grab was queued for re-search (double-grab bug)")
	}
	if !got[dead.ID] {
		t.Errorf("book whose import failed must stay searchable (recovery path)")
	}
	if !got[fresh.ID] {
		t.Errorf("never-grabbed wanted book must be searchable")
	}
}

// TestRefreshMetadata_EmptyDB exercises the no-authors early-return path.
func TestRefreshMetadata_EmptyDB(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	s := &Scheduler{
		authors: db.NewAuthorRepo(database),
	}
	s.refreshMetadata() // no panic; loop body not entered
}

// TestRefreshMetadata_UnmonitoredAuthor verifies that unmonitored authors are
// skipped without calling the metadata provider.
func TestRefreshMetadata_UnmonitoredAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	repo := db.NewAuthorRepo(database)
	ctx := context.Background()
	_ = repo.Create(ctx, &models.Author{
		ForeignID:        "OL999A",
		Name:             "Unmonitored",
		SortName:         "Unmonitored",
		MetadataProvider: "openlibrary",
		Monitored:        false,
	})

	// meta is nil; if the monitored check didn't guard it, this would panic.
	s := &Scheduler{
		authors: repo,
		meta:    nil,
	}
	s.refreshMetadata() // must not panic: skips unmonitored author
}

// TestSearchAndGrabBook_NoIndexers exercises SearchAndGrabBook through the
// "no results → return early" path by using a real (empty) indexer repo.
// The searcher returns empty results for an empty indexer list, so this is safe
// with a real Searcher and empty DB.
func TestSearchAndGrabBook_NoIndexers(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	s := &Scheduler{
		searcher:  indexer.NewSearcher(),
		indexers:  db.NewIndexerRepo(database),
		authors:   db.NewAuthorRepo(database),
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
	}

	// No indexers in DB → search yields no results → function returns at
	// "if len(results) == 0" without hitting download-client code.
	s.SearchAndGrabBook(ctx, models.Book{Title: "Dune", MediaType: models.MediaTypeEbook})
}

// TestSearchAndGrabBook_WithLanguageSetting exercises the settings lookup path.
func TestSearchAndGrabBook_WithLanguageSetting(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	settingsRepo := db.NewSettingsRepo(database)
	_ = settingsRepo.Set(ctx, "search.preferredLanguage", "fre")

	s := &Scheduler{
		searcher:  indexer.NewSearcher(),
		indexers:  db.NewIndexerRepo(database),
		authors:   db.NewAuthorRepo(database),
		settings:  settingsRepo,
		blocklist: db.NewBlocklistRepo(database),
	}

	// Language "fre" is loaded from settings; still no results with empty DB.
	s.SearchAndGrabBook(ctx, models.Book{Title: "Le Petit Prince"})
}

// spyNotifier records Send calls so emit-site tests can assert the
// notification was published with the expected event type and payload.
type spyNotifier struct {
	mu    sync.Mutex
	calls []spyCall
}

type spyCall struct {
	eventType string
	payload   map[string]interface{}
}

func (n *spyNotifier) Send(_ context.Context, eventType string, payload map[string]interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, spyCall{eventType: eventType, payload: payload})
}

func (n *spyNotifier) lookup(eventType string) *spyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.calls {
		if n.calls[i].eventType == eventType {
			return &n.calls[i]
		}
	}
	return nil
}

// TestNotify_NilNotifierIsSafe guards the optional-injection contract:
// auto-grab paths run on schedulers built without notifier wiring (legacy
// tests, embedded use) and must not crash when notify() is called.
func TestNotify_NilNotifierIsSafe(t *testing.T) {
	s := &Scheduler{}
	s.notify(context.Background(), notifierEventGrabbed, map[string]interface{}{"title": "x"})
}

// TestNotify_DispatchesEventGrabbed verifies that WithNotifier actually wires
// the field so the Send call lands on the spy. This is the unit-level
// guarantee that auto-grab successes will publish; the searchAndGrabFormat
// integration is verified by reading the code (issue #849).
func TestNotify_DispatchesEventGrabbed(t *testing.T) {
	spy := &spyNotifier{}
	s := &Scheduler{}
	s.WithNotifier(spy)

	s.notify(context.Background(), notifierEventGrabbed, map[string]interface{}{
		"title":  "Dune",
		"size":   int64(1024),
		"author": "Frank Herbert",
	})

	call := spy.lookup(notifierEventGrabbed)
	if call == nil {
		t.Fatalf("expected EventGrabbed call; got %+v", spy.calls)
		return
	}
	if got, want := call.payload["title"], "Dune"; got != want {
		t.Errorf("payload title = %v, want %q", got, want)
	}
	if got, want := call.payload["author"], "Frank Herbert"; got != want {
		t.Errorf("payload author = %v, want %q", got, want)
	}
	if got, want := call.payload["size"], int64(1024); got != want {
		t.Errorf("payload size = %v, want %d", got, want)
	}
}

// TestRefreshMetadata_MonitoredAuthor_MetaError exercises the error-continue
// branch of the refreshMetadata loop by using a metadata provider that always
// returns a valid author. This pushes coverage into the loop update path.
func TestRefreshMetadata_MonitoredAuthor_MetaSuccess(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authRepo := db.NewAuthorRepo(database)
	_ = authRepo.Create(ctx, &models.Author{
		ForeignID:        "OL777A",
		Name:             "Monitored Author",
		SortName:         "Author, Monitored",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	mockProvider := &mockMetaProvider{
		author: &models.Author{
			ForeignID:   "OL777A",
			Name:        "Monitored Author",
			Description: "An updated description from the metadata provider.",
			ImageURL:    "https://example.com/photo.jpg",
		},
	}

	s := &Scheduler{
		authors: authRepo,
		meta:    metadata.NewAggregator(mockProvider),
	}

	// Loop body executes: GetAuthor → success → Update author fields.
	s.refreshMetadata()
}

// TestRefreshMetadata_NilAuthor_DoesNotPanic pins down the regression where
// Aggregator.GetAuthor returns (nil, nil) — which happens when no configured
// provider owns the foreignID prefix, e.g. a Hardcover-prefixed author after
// Hardcover is disabled. The pre-fix code did `author.Description =
// updated.Description` immediately and panicked, killing the refresh loop
// and every subsequent author's refresh that cycle.
func TestRefreshMetadata_NilAuthor_DoesNotPanic(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authRepo := db.NewAuthorRepo(database)
	// Two monitored authors so we can also verify the loop continues past
	// the nil result instead of aborting after the first one.
	for _, a := range []*models.Author{
		{ForeignID: "OL_NIL_1A", Name: "First", SortName: "First", MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL_NIL_2A", Name: "Second", SortName: "Second", MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := authRepo.Create(ctx, a); err != nil {
			t.Fatalf("create author: %v", err)
		}
	}

	// author=nil → mockMetaProvider.GetAuthor returns (nil, nil), which the
	// aggregator passes straight through to the scheduler.
	mockProvider := &mockMetaProvider{author: nil}
	s := &Scheduler{
		authors: authRepo,
		meta:    metadata.NewAggregator(mockProvider),
	}

	// Pre-fix: panics on the first author. Post-fix: skips both and returns.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("refreshMetadata panicked on nil author from aggregator: %v", r)
		}
	}()
	s.refreshMetadata()
}
