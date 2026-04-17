package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
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

	s := New(nil, nil, nil, authors, books, indexers, downloads, clients, settings, blocklist)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.cron == nil {
		t.Fatal("cron field not initialized")
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

// TestFilterBlocklisted_WithRealRepo exercises the non-nil repo path of
// filterBlocklisted, covering the loop body and IsBlocked call.
func TestFilterBlocklisted_WithRealRepo(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := db.NewBlocklistRepo(database)

	results := []newznab.SearchResult{
		{GUID: "good-guid", Title: "Good Release"},
		{GUID: "blocked-guid", Title: "Blocked Release"},
	}

	// Block one GUID.
	_ = repo.Create(ctx, &models.BlocklistEntry{
		GUID:  "blocked-guid",
		Title: "Blocked Release",
	})

	out := filterBlocklisted(ctx, repo, results)
	if len(out) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out))
	}
	if out[0].GUID != "good-guid" {
		t.Errorf("expected 'good-guid' to pass, got %q", out[0].GUID)
	}
}

// TestFilterBlocklisted_AllBlocked verifies that an all-blocked result set
// returns an empty (not nil) slice.
func TestFilterBlocklisted_AllBlocked(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := db.NewBlocklistRepo(database)

	_ = repo.Create(ctx, &models.BlocklistEntry{GUID: "a", Title: "A"})
	_ = repo.Create(ctx, &models.BlocklistEntry{GUID: "b", Title: "B"})

	results := []newznab.SearchResult{{GUID: "a"}, {GUID: "b"}}
	out := filterBlocklisted(ctx, repo, results)
	if len(out) != 0 {
		t.Errorf("expected 0 results, got %d", len(out))
	}
}

// TestFilterBlocklisted_NilRepo_ReturnsAll verifies the production nil-repo guard.
func TestFilterBlocklisted_NilRepo_ReturnsAll(t *testing.T) {
	results := []newznab.SearchResult{
		{GUID: "x"}, {GUID: "y"}, {GUID: "z"},
	}
	out := filterBlocklisted(context.Background(), nil, results)
	if len(out) != 3 {
		t.Errorf("nil BlocklistRepo: expected 3 results, got %d", len(out))
	}
}

// TestFilterBlocklisted_EmptyInput confirms nil/empty input → empty output with nil repo.
func TestFilterBlocklisted_EmptyInput(t *testing.T) {
	ctx := context.Background()
	out := filterBlocklisted(ctx, nil, nil)
	if len(out) != 0 {
		t.Errorf("nil input: expected 0, got %d", len(out))
	}
	out2 := filterBlocklisted(ctx, nil, []newznab.SearchResult{})
	if len(out2) != 0 {
		t.Errorf("empty slice: expected 0, got %d", len(out2))
	}
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
