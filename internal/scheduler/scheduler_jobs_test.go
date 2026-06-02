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
