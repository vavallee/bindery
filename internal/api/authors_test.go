package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// searcherSpy records every SearchAndGrabBook call so tests can assert on it.
type searcherSpy struct {
	mu    sync.Mutex
	calls []string // book titles in call order
}

func (s *searcherSpy) SearchAndGrabBook(_ context.Context, book models.Book) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, book.Title)
}

func (s *searcherSpy) titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

type concurrentSearcherSpy struct {
	mu     sync.Mutex
	calls  int
	active int
	max    int
}

func (s *concurrentSearcherSpy) SearchAndGrabBook(_ context.Context, _ models.Book) {
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	s.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	s.mu.Lock()
	s.active--
	s.mu.Unlock()
}

func (s *concurrentSearcherSpy) stats() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.max
}

// stubMetaProvider is a fake metadata.Provider whose GetAuthorWorks returns
// a caller-supplied book list, allowing FetchAuthorBooks to run without
// hitting the real OpenLibrary API.
type stubMetaProvider struct {
	works []models.Book
	// getBookByID lets a test return a specific Book for a foreign ID.
	// Used to exercise the AddBook direct-insert path for DNB-prefixed
	// foreign IDs (issue #667).
	getBookByID map[string]*models.Book
	// name overrides the provider's reported name. When non-empty it's
	// returned by Name() — required when a test exercises a code path
	// that routes by prefix via the aggregator (e.g. "dnb" for DNB IDs).
	name string
	// editionsByBook lets tests exercise metadata edition hydration.
	editionsByBook map[string][]models.Edition
	editionCalls   []string
	// authorWorksByName lets tests act as an author-scoped supplemental
	// provider such as Hardcover.
	authorWorksByName []models.Book
	// author, when non-nil, is returned by GetAuthor so tests can exercise
	// the author-profile refresh path (Discussion #1226).
	author *models.Author
}

func (p *stubMetaProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "stub"
}
func (p *stubMetaProvider) SearchAuthors(_ context.Context, _ string) ([]models.Author, error) {
	return nil, nil
}
func (p *stubMetaProvider) SearchBooks(_ context.Context, _ string) ([]models.Book, error) {
	return nil, nil
}
func (p *stubMetaProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return p.author, nil
}
func (p *stubMetaProvider) GetBook(_ context.Context, fid string) (*models.Book, error) {
	if p.getBookByID != nil {
		if b, ok := p.getBookByID[fid]; ok {
			return b, nil
		}
	}
	return nil, nil
}
func (p *stubMetaProvider) GetEditions(_ context.Context, fid string) ([]models.Edition, error) {
	p.editionCalls = append(p.editionCalls, fid)
	if p.editionsByBook != nil {
		return p.editionsByBook[fid], nil
	}
	return nil, nil
}
func (p *stubMetaProvider) GetBookByISBN(_ context.Context, _ string) (*models.Book, error) {
	return nil, nil
}

// GetAuthorWorks satisfies the worksProvider sub-interface used by Aggregator.
func (p *stubMetaProvider) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	return p.works, nil
}

func (p *stubMetaProvider) GetAuthorWorksByName(_ context.Context, _ string) ([]models.Book, error) {
	return p.authorWorksByName, nil
}

func enableHardcoverFeatureForTest(t *testing.T, ctx context.Context, settings *db.SettingsRepo) {
	t.Helper()
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "hc-test-token"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, SettingHardcoverEnhancedSeriesEnabled, "true"); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteAuthor_WithDeleteFiles verifies the ?deleteFiles=true branch
// sweeps every book's on-disk path. The invariant under test is ordering:
// paths must be collected from `books ListByAuthor` *before* the cascade
// wipes the book rows, otherwise we'd have nothing to sweep. A regression
// here re-orphans files (issue #15).
func TestDeleteAuthor_WithDeleteFiles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL900A", Name: "Jared Diamond", SortName: "Diamond, Jared",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Two audiobook folders + one ebook file, all populated.
	root := t.TempDir()
	mkFolder := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "part1.m4b"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	path1 := mkFolder("Guns Germs and Steel (1997)")
	path2 := mkFolder("Collapse (2005)")
	path3 := filepath.Join(root, "The World Until Yesterday.epub")
	if err := os.WriteFile(path3, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Plus a file-less wanted book — must not trip anything even though
	// FilePath is empty. Note: BookRepo.Create ignores FilePath, so we
	// back-fill via SetFilePath (same path the real importer takes).
	for _, b := range []*models.Book{
		{ForeignID: "OL901W", AuthorID: author.ID, Title: "Guns Germs", SortTitle: "Guns", FilePath: path1, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL902W", AuthorID: author.ID, Title: "Collapse", SortTitle: "Collapse", FilePath: path2, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL903W", AuthorID: author.ID, Title: "World", SortTitle: "World", FilePath: path3, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL904W", AuthorID: author.ID, Title: "Wanted No File", SortTitle: "Wanted", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if b.FilePath != "" {
			if err := bookRepo.SetFilePath(ctx, b.ID, b.FilePath); err != nil {
				t.Fatal(err)
			}
		}
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10)+"?deleteFiles=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{path1, path2, path3} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", p, err)
		}
	}
	// And the author row is gone.
	got, _ := authorRepo.GetByID(ctx, author.ID)
	if got != nil {
		t.Error("expected author deleted")
	}
}

// TestDeleteAuthor_SkipsFileOwnedByAnotherBook covers the #1368 ownership guard
// on the author-delete sweep: a book under the deleted author whose STALE legacy
// file_path points at a file another (surviving) book still tracks in book_files
// must not be removed from disk.
func TestDeleteAuthor_SkipsFileOwnedByAnotherBook(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	shared := filepath.Join(t.TempDir(), "shared.m4b")
	if err := os.WriteFile(shared, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Surviving owner under a different author, tracking the file in book_files.
	keepAuthor := &models.Author{ForeignID: "OLKEEP", Name: "Keep", SortName: "Keep", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, keepAuthor); err != nil {
		t.Fatal(err)
	}
	owner := &models.Book{ForeignID: "OLOWNER", AuthorID: keepAuthor.ID, Title: "Owner", SortTitle: "owner", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	if err := bookRepo.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, owner.ID, models.MediaTypeAudiobook, shared); err != nil {
		t.Fatal(err)
	}

	// Doomed author with a book whose legacy file_path aliases the same file but
	// has no book_files row of its own.
	author := &models.Author{ForeignID: "OLDOOM", Name: "Doom", SortName: "Doom", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ghost := &models.Book{ForeignID: "OLGHOST", AuthorID: author.ID, Title: "Ghost", SortTitle: "ghost", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	if err := bookRepo.Create(ctx, ghost); err != nil {
		t.Fatal(err)
	}
	ghost.FilePath = shared
	if err := bookRepo.Update(ctx, ghost); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10)+"?deleteFiles=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared file deleted by author delete despite another book owning it (#1368): %v", err)
	}
	if of, err := bookRepo.ListFiles(ctx, owner.ID); err != nil || len(of) != 1 {
		t.Errorf("owner should still track the file, got %d rows (err=%v)", len(of), err)
	}
}

// TestDeleteAuthor_WithoutDeleteFiles confirms the default path leaves
// files on disk. Preserves the pre-#15 behaviour for anyone who hits the
// delete button reflexively without opting into a disk sweep.
func TestDeleteAuthor_WithoutDeleteFiles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL910A", Name: "Keep Files", SortName: "Files, Keep",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL911W", AuthorID: author.ID, Title: "Book", SortTitle: "Book",
		FilePath: path, Status: "imported", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should survive default delete, stat err=%v", err)
	}
}

// TestFetchAuthorBooks_FiresSearchForMonitoredAuthor verifies that
// FetchAuthorBooks calls SearchAndGrabBook once per newly created book when
// the author is monitored. The stub metadata provider returns two works so we
// expect exactly two search calls.
func TestFetchAuthorBooks_FiresSearchForMonitoredAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL500A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL501W", Title: "First Book", SortTitle: "first book", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL502W", Title: "Second Book", SortTitle: "second book", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(stub)
	spy := &searcherSpy{}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, spy)
	h.FetchAuthorBooks(author, true, "")

	titles := spy.titles()
	if len(titles) != 2 {
		t.Fatalf("expected 2 searcher calls, got %d: %v", len(titles), titles)
	}
}

// TestFetchAuthorBooks_RefreshesAuthorProfile verifies that a metadata refresh
// repopulates the author's OWN profile fields (description + image) from the
// linked provider, not just the catalogue. Before the fix the refresh fetched
// books but left Description/ImageURL empty even when the provider supplied them
// (Discussion #1226).
func TestFetchAuthorBooks_RefreshesAuthorProfile(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	// An already-linked OpenLibrary author whose local profile is empty,
	// mirroring the reporter's "linked but bio/photo blank" state.
	author := &models.Author{
		ForeignID: "OL6094856A", Name: "Paul Cornell", SortName: "Cornell, Paul",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL700W", Title: "London Falling", SortTitle: "london falling", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
		author: &models.Author{
			ForeignID:        "OL6094856A",
			Name:             "Paul Cornell",
			Description:      "British writer of novels, comics and TV.",
			ImageURL:         "https://covers.openlibrary.org/a/id/14431281-L.jpg",
			Disambiguation:   "British author",
			RatingsCount:     42,
			AverageRating:    4.1,
			MetadataProvider: "openlibrary",
		},
	}
	agg := metadata.NewAggregator(stub)

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	got, err := authorRepo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "British writer of novels, comics and TV." {
		t.Errorf("Description not refreshed: got %q", got.Description)
	}
	if got.ImageURL != "https://covers.openlibrary.org/a/id/14431281-L.jpg" {
		t.Errorf("ImageURL not refreshed: got %q", got.ImageURL)
	}
	if got.Disambiguation != "British author" {
		t.Errorf("Disambiguation not refreshed: got %q", got.Disambiguation)
	}
	if got.RatingsCount != 42 || got.AverageRating != 4.1 {
		t.Errorf("ratings not refreshed: count=%d avg=%v", got.RatingsCount, got.AverageRating)
	}
	if got.LastMetadataRefreshAt == nil {
		t.Error("LastMetadataRefreshAt not stamped on profile refresh")
	}
	// The catalogue must still be populated by the same refresh.
	books, _ := bookRepo.ListByAuthor(ctx, author.ID)
	if len(books) != 1 {
		t.Errorf("expected 1 book synced, got %d", len(books))
	}
}

func TestFetchAuthorBooks_AutoSearchUsesBoundedConcurrency(t *testing.T) {
	// This test asserts the concurrency cap; disable search pacing so launches
	// aren't spaced out (pacing is covered in the concurrency package).
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = 3 * time.Second })

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL800A", Name: "Parallel Author", SortName: "Author, Parallel",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	works := make([]models.Book, 8)
	for i := range works {
		works[i] = models.Book{
			ForeignID:        "OL80" + strconv.Itoa(i) + "W",
			Title:            "Book " + strconv.Itoa(i),
			SortTitle:        "book " + strconv.Itoa(i),
			Language:         "eng",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
		}
	}
	stub := &stubMetaProvider{works: works}
	agg := metadata.NewAggregator(stub)
	spy := &concurrentSearcherSpy{}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, spy)
	h.FetchAuthorBooks(author, true, "")

	calls, maxActive := spy.stats()
	if calls != len(works) {
		t.Fatalf("expected %d search calls, got %d", len(works), calls)
	}
	if maxActive > authorAutoSearchConcurrency {
		t.Fatalf("max concurrent searches = %d, want <= %d", maxActive, authorAutoSearchConcurrency)
	}
	if maxActive < 2 {
		t.Fatalf("expected searches to run concurrently, max active = %d", maxActive)
	}
}

// stubLibraryFinder is a mock LibraryFinder that returns a fixed path for
// a specific title and "" for everything else.
type stubLibraryFinder struct {
	ownedTitle string
	ownedPath  string
}

func (f *stubLibraryFinder) FindExisting(_ context.Context, title, _, _ string) string {
	if title == f.ownedTitle {
		return f.ownedPath
	}
	return ""
}

// TestFetchAuthorBooks_SkipsSearchForOwnedBooks verifies that when the
// LibraryFinder reports a book is already on disk, FetchAuthorBooks sets the
// file path on the book row and does NOT call SearchAndGrabBook for it. Books
// NOT found on disk should still be searched normally.
// TestFetchAuthorBooks_BackfillsHardcoverGenres verifies that refreshing an
// author updates an existing book's genres when the incoming work carries
// Hardcover provenance, so libraries imported before genre sourcing get cleaned.
func TestFetchAuthorBooks_BackfillsHardcoverGenres(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL800A", Name: "Backfill Author", SortName: "Author, Backfill",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Pre-existing row with noisy OpenLibrary subjects and no Hardcover link.
	existing := &models.Book{
		ForeignID: "OL801W", AuthorID: author.ID, Title: "Old Book", SortTitle: "old book",
		Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "openlibrary",
		Genres: []string{"Fiction", "American literature"},
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// Refresh returns the same work, now Hardcover-matched with clean genres.
	stub := &stubMetaProvider{
		works: []models.Book{
			{
				ForeignID: "OL801W", Title: "Old Book", SortTitle: "old book", Language: "eng",
				Status: models.BookStatusWanted, MetadataProvider: "openlibrary",
				HardcoverForeignID: "hc:old-book", Genres: []string{"Fantasy"},
			},
		},
	}
	agg := metadata.NewAggregator(stub)

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, &searcherSpy{})
	h.FetchAuthorBooks(author, false, "")

	got, err := bookRepo.GetByForeignID(ctx, "OL801W")
	if err != nil || got == nil {
		t.Fatalf("reload book: %v", err)
	}
	if want := []string{"Fantasy"}; !slices.Equal(got.Genres, want) {
		t.Errorf("genres should be backfilled from hardcover: want %v, got %v", want, got.Genres)
	}
}

func TestFetchAuthorBooks_SkipsSearchForOwnedBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL700A", Name: "Owned Author", SortName: "Author, Owned",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL701W", Title: "Already Owned", SortTitle: "already owned", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL702W", Title: "Not Yet Owned", SortTitle: "not yet owned", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(stub)
	spy := &searcherSpy{}
	finder := &stubLibraryFinder{
		ownedTitle: "Already Owned",
		ownedPath:  "/library/Owned Author/Already Owned.epub",
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, spy).WithFinder(finder)
	h.FetchAuthorBooks(author, true, "")

	titles := spy.titles()
	// Only "Not Yet Owned" should have triggered a search.
	if len(titles) != 1 {
		t.Fatalf("expected 1 searcher call, got %d: %v", len(titles), titles)
	}
	if titles[0] != "Not Yet Owned" {
		t.Errorf("expected search for 'Not Yet Owned', got %q", titles[0])
	}

	// The owned book's file path should have been persisted in the DB.
	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ownedBook *models.Book
	for i := range books {
		if books[i].Title == "Already Owned" {
			ownedBook = &books[i]
			break
		}
	}
	if ownedBook == nil {
		t.Fatal("expected 'Already Owned' book to be created in DB")
		return
	}
	if ownedBook.FilePath != finder.ownedPath {
		t.Errorf("expected file path %q, got %q", finder.ownedPath, ownedBook.FilePath)
	}
}

// TestFetchAuthorBooks_SkipsSearchWhenNotMonitored confirms that books added
// for an unmonitored author do NOT trigger an indexer search — the user has
// opted out of automatic activity for this author.
func TestFetchAuthorBooks_SkipsSearchWhenNotMonitored(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL600A", Name: "Unmonitored", SortName: "Unmonitored",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL601W", Title: "Some Book", SortTitle: "some book", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(stub)
	spy := &searcherSpy{}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, spy)
	h.FetchAuthorBooks(author, true, "")

	if titles := spy.titles(); len(titles) != 0 {
		t.Errorf("expected no searcher calls for unmonitored author, got %v", titles)
	}
}

func TestFetchAuthorBooks_AppliesAuthorMonitorModes(t *testing.T) {
	now := time.Now().UTC()
	oldDate := now.AddDate(0, 0, -30)
	middleDate := now.AddDate(0, 0, -7)
	futureDate := now.AddDate(0, 0, 7)

	testCases := []struct {
		name        string
		mode        string
		latestCount int
		want        map[string]bool
	}{
		{
			name:        "all",
			mode:        models.AuthorMonitorModeAll,
			latestCount: 1,
			want: map[string]bool{
				"Old Book":     true,
				"Middle Book":  true,
				"Future Book":  true,
				"Unknown Book": true,
			},
		},
		{
			name:        "future",
			mode:        models.AuthorMonitorModeFuture,
			latestCount: 1,
			want: map[string]bool{
				"Old Book":     false,
				"Middle Book":  false,
				"Future Book":  true,
				"Unknown Book": false,
			},
		},
		{
			name:        "latest",
			mode:        models.AuthorMonitorModeLatest,
			latestCount: 2,
			want: map[string]bool{
				"Old Book":     false,
				"Middle Book":  true,
				"Future Book":  true,
				"Unknown Book": false,
			},
		},
		{
			name:        "none",
			mode:        models.AuthorMonitorModeNone,
			latestCount: 1,
			want: map[string]bool{
				"Old Book":     false,
				"Middle Book":  false,
				"Future Book":  false,
				"Unknown Book": false,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()

			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			profileRepo := db.NewMetadataProfileRepo(database)
			ctx := context.Background()

			author := &models.Author{
				ForeignID:          "OL-MODE-" + tc.name,
				Name:               "Mode Author",
				SortName:           "Author, Mode",
				MetadataProvider:   "openlibrary",
				Monitored:          true,
				MonitorMode:        tc.mode,
				MonitorLatestCount: tc.latestCount,
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}

			stub := &stubMetaProvider{
				works: []models.Book{
					{ForeignID: "OL-MODE-OLD-" + tc.name, Title: "Old Book", SortTitle: "old book", ReleaseDate: &oldDate, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
					{ForeignID: "OL-MODE-MID-" + tc.name, Title: "Middle Book", SortTitle: "middle book", ReleaseDate: &middleDate, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
					{ForeignID: "OL-MODE-FUT-" + tc.name, Title: "Future Book", SortTitle: "future book", ReleaseDate: &futureDate, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
					{ForeignID: "OL-MODE-UNK-" + tc.name, Title: "Unknown Book", SortTitle: "unknown book", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
				},
			}
			spy := &searcherSpy{}
			h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, spy)
			h.FetchAuthorBooks(author, true, "")

			books, err := bookRepo.ListByAuthor(ctx, author.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != len(tc.want) {
				t.Fatalf("books = %d, want %d", len(books), len(tc.want))
			}
			for _, book := range books {
				want, ok := tc.want[book.Title]
				if !ok {
					t.Fatalf("unexpected book %q", book.Title)
				}
				if book.Monitored != want {
					t.Errorf("%s monitored = %v, want %v", book.Title, book.Monitored, want)
				}
			}

			seenSearches := map[string]bool{}
			for _, title := range spy.titles() {
				seenSearches[title] = true
			}
			for title, wantMonitored := range tc.want {
				if seenSearches[title] != wantMonitored {
					t.Errorf("search for %q = %v, want %v", title, seenSearches[title], wantMonitored)
				}
			}
		})
	}
}

// TestFetchAuthorBooks_NoneMode_PreservesListedBook_DropsBackCatalogue is the
// #1290 reconcile guarantee from the API side. A Hardcover-list-created author
// is monitored but pinned to MonitorMode "none", and the single listed book was
// already inserted as monitored + wanted. When the scheduler's catalogue
// discovery (FetchAuthorBooks) then surfaces the author's whole back-catalogue,
// the listed book must stay monitored (already-tracked rows are left untouched)
// while every newly-discovered work stays unmonitored — no back-catalogue
// blowup.
func TestFetchAuthorBooks_NoneMode_PreservesListedBook_DropsBackCatalogue(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID:          "OL-LIST-AUTHOR",
		Name:               "List Author",
		SortName:           "Author, List",
		MetadataProvider:   "openlibrary",
		Monitored:          true,
		MonitorMode:        models.AuthorMonitorModeNone,
		MonitorLatestCount: 1,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// The listed book, as the syncer inserts it: monitored + wanted, sharing the
	// foreign id of one of the catalogue works the provider will surface.
	listed := &models.Book{
		ForeignID:        "OL-LISTED",
		Title:            "The Listed Book",
		AuthorID:         author.ID,
		MetadataProvider: "openlibrary",
		Monitored:        true,
		Status:           models.BookStatusWanted,
		Genres:           []string{},
	}
	if err := bookRepo.Create(ctx, listed); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{
			// The listed book reappears as part of the discovered catalogue.
			{ForeignID: "OL-LISTED", Title: "The Listed Book", SortTitle: "the listed book", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			// Back-catalogue the user never asked for.
			{ForeignID: "OL-BACK1", Title: "Back Catalogue One", SortTitle: "back catalogue one", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL-BACK2", Title: "Back Catalogue Two", SortTitle: "back catalogue two", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL-BACK3", Title: "Back Catalogue Three", SortTitle: "back catalogue three", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	spy := &searcherSpy{}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, spy)
	h.FetchAuthorBooks(author, true, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantMonitored := map[string]bool{
		"The Listed Book":      true,  // preserved
		"Back Catalogue One":   false, // never auto-wanted
		"Back Catalogue Two":   false,
		"Back Catalogue Three": false,
	}
	if len(books) != len(wantMonitored) {
		t.Fatalf("books = %d, want %d: %+v", len(books), len(wantMonitored), books)
	}
	for _, b := range books {
		want, ok := wantMonitored[b.Title]
		if !ok {
			t.Fatalf("unexpected book %q", b.Title)
		}
		if b.Monitored != want {
			t.Errorf("%s monitored = %v, want %v", b.Title, b.Monitored, want)
		}
	}

	// The discovery pass searches only newly-discovered monitored books. Under
	// "none" nothing new is monitored, and the already-tracked listed book is
	// skipped (it was searched when the syncer created it). So this pass must
	// queue zero searches — in particular none for the back-catalogue.
	if got := spy.titles(); len(got) != 0 {
		t.Errorf("unexpected searches queued by discovery pass: %v", got)
	}
}

func TestFetchAuthorBooksHydratesOnlySupplementalHardcoverBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "OL-HYDRATE-A",
		Name:             "Author",
		SortName:         "Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	primary := &stubMetaProvider{
		name: "openlibrary",
		works: []models.Book{{
			ForeignID:        "OL-HYDRATE-W",
			Title:            "Primary Book",
			SortTitle:        "Primary Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
		}},
	}
	audioASIN := "B000AUTHOR"
	hardcover := &stubMetaProvider{
		name: "hardcover",
		authorWorksByName: []models.Book{{
			ForeignID:        "hc:audio-book",
			Title:            "Audio Book",
			SortTitle:        "Audio Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
		}},
		editionsByBook: map[string][]models.Edition{
			"hc:audio-book": {{
				ForeignID: "hc:audio-book-edition",
				Title:     "Audio Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	agg := metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil).
		WithEditionHydration(editionRepo)
	h.FetchAuthorBooks(author, false, "")

	primaryBook, err := bookRepo.GetByForeignID(ctx, "OL-HYDRATE-W")
	if err != nil {
		t.Fatal(err)
	}
	if primaryBook == nil {
		t.Fatal("expected primary book")
		return
	}
	if primaryBook.ASIN != "" {
		t.Fatalf("primary OpenLibrary book was unexpectedly hydrated: %+v", primaryBook)
	}
	hardcoverBook, err := bookRepo.GetByForeignID(ctx, "hc:audio-book")
	if err != nil {
		t.Fatal(err)
	}
	if hardcoverBook == nil || hardcoverBook.ASIN != audioASIN {
		t.Fatalf("hardcover book was not hydrated: %+v", hardcoverBook)
	}
	if len(primary.editionCalls) != 0 {
		t.Fatalf("primary provider edition calls = %+v", primary.editionCalls)
	}
	if len(hardcover.editionCalls) != 1 || hardcover.editionCalls[0] != "hc:audio-book" {
		t.Fatalf("hardcover edition calls = %+v", hardcover.editionCalls)
	}
	editions, err := editionRepo.ListByBook(ctx, hardcoverBook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:audio-book-edition" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

func TestFetchAuthorBooksHydratesMatchedOpenLibraryHardcoverEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	enableHardcoverFeatureForTest(t, ctx, settingsRepo)
	author := &models.Author{
		ForeignID:        "OL-MATCH-A",
		Name:             "Author",
		SortName:         "Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	primary := &stubMetaProvider{
		name: "openlibrary",
		works: []models.Book{{
			ForeignID:        "OL-MATCH-W",
			Title:            "Matched Book",
			SortTitle:        "Matched Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
		}},
	}
	audioASIN := "B000MATCH"
	hardcover := &stubMetaProvider{
		name: "hardcover",
		authorWorksByName: []models.Book{{
			ForeignID:        "hc:matched-book",
			Title:            "Matched Book",
			SortTitle:        "Matched Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
		}},
		editionsByBook: map[string][]models.Edition{
			"hc:matched-book": {{
				ForeignID: "hc:matched-book-audio",
				Title:     "Matched Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	agg := metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).
		WithHardcoverFeatureSettings(settingsRepo, true).
		WithEditionHydration(editionRepo)
	h.FetchAuthorBooks(author, false, "")

	book, err := bookRepo.GetByForeignID(ctx, "OL-MATCH-W")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil {
		t.Fatal("expected OpenLibrary book")
		return
	}
	if book.ForeignID != "OL-MATCH-W" || book.MetadataProvider != "openlibrary" {
		t.Fatalf("book identity was rebound unexpectedly: %+v", book)
	}
	if book.MediaType != models.MediaTypeAudiobook || book.ASIN != audioASIN {
		t.Fatalf("matched book was not hydrated: %+v", book)
	}
	if len(primary.editionCalls) != 0 {
		t.Fatalf("primary provider edition calls = %+v", primary.editionCalls)
	}
	if len(hardcover.editionCalls) != 1 || hardcover.editionCalls[0] != "hc:matched-book" {
		t.Fatalf("hardcover edition calls = %+v", hardcover.editionCalls)
	}
	editions, err := editionRepo.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:matched-book-audio" {
		t.Fatalf("expected matched edition, got %+v", editions)
	}
}

func TestFetchAuthorBooksDoesNotHydrateMatchedHardcoverWhenEnhancedDisabled(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "OL-MATCH-DISABLED-A",
		Name:             "Author",
		SortName:         "Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	primary := &stubMetaProvider{
		name: "openlibrary",
		works: []models.Book{{
			ForeignID:        "OL-MATCH-DISABLED-W",
			Title:            "Matched Disabled Book",
			SortTitle:        "Matched Disabled Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
		}},
	}
	audioASIN := "B000DISABL"
	hardcover := &stubMetaProvider{
		name: "hardcover",
		authorWorksByName: []models.Book{{
			ForeignID:        "hc:matched-disabled-book",
			Title:            "Matched Disabled Book",
			SortTitle:        "Matched Disabled Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
		}},
		editionsByBook: map[string][]models.Edition{
			"hc:matched-disabled-book": {{
				ForeignID: "hc:matched-disabled-book-audio",
				Title:     "Matched Disabled Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	agg := metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).
		WithHardcoverFeatureSettings(settingsRepo, true).
		WithEditionHydration(editionRepo)
	h.FetchAuthorBooks(author, false, "")

	book, err := bookRepo.GetByForeignID(ctx, "OL-MATCH-DISABLED-W")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil {
		t.Fatal("expected OpenLibrary book")
		return
	}
	if book.ASIN != "" {
		t.Fatalf("matched book was hydrated while enhanced Hardcover was disabled: %+v", book)
	}
	if len(hardcover.editionCalls) != 0 {
		t.Fatalf("hardcover edition calls = %+v, want none", hardcover.editionCalls)
	}
	editions, err := editionRepo.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 0 {
		t.Fatalf("editions were persisted while enhanced Hardcover was disabled: %+v", editions)
	}
}

func TestFetchAuthorBooksHydratesMatchedHardcoverTitleDedupUpgrade(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	enableHardcoverFeatureForTest(t, ctx, settingsRepo)
	author := &models.Author{
		ForeignID:        "OL-DEDUP-HYDRATE-A",
		Name:             "Author",
		SortName:         "Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{
		ForeignID:        "OL-DEDUP-EXISTING-W",
		AuthorID:         author.ID,
		Title:            "Shared Book",
		SortTitle:        "Shared Book",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
		MediaType:        models.MediaTypeEbook,
		Monitored:        true,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	primary := &stubMetaProvider{
		name: "openlibrary",
		works: []models.Book{{
			ForeignID:        "OL-DEDUP-NEW-W",
			Title:            "Shared Book",
			SortTitle:        "Shared Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "openlibrary",
		}},
	}
	audioASIN := "B000DEDUP"
	hardcover := &stubMetaProvider{
		name: "hardcover",
		authorWorksByName: []models.Book{{
			ForeignID:        "hc:dedup-book",
			Title:            "Shared Book",
			SortTitle:        "Shared Book",
			Status:           models.BookStatusWanted,
			Genres:           []string{},
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
		}},
		editionsByBook: map[string][]models.Edition{
			"hc:dedup-book": {{
				ForeignID: "hc:dedup-book-audio",
				Title:     "Shared Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	agg := metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).
		WithHardcoverFeatureSettings(settingsRepo, true).
		WithEditionHydration(editionRepo)
	h.FetchAuthorBooks(author, false, "")

	updated, err := bookRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MediaType != models.MediaTypeBoth || updated.ASIN != audioASIN {
		t.Fatalf("existing book was not upgraded and hydrated: %+v", updated)
	}
	if created, err := bookRepo.GetByForeignID(ctx, "OL-DEDUP-NEW-W"); err != nil {
		t.Fatal(err)
	} else if created != nil {
		t.Fatalf("dedup path created a second book: %+v", created)
	}
	if len(hardcover.editionCalls) != 1 || hardcover.editionCalls[0] != "hc:dedup-book" {
		t.Fatalf("hardcover edition calls = %+v", hardcover.editionCalls)
	}
	editions, err := editionRepo.ListByBook(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:dedup-book-audio" {
		t.Fatalf("expected dedup hydrated edition, got %+v", editions)
	}
}

// TestFetchAuthorBooks_DedupsEditionSuffix is a regression test for issue #283.
// When two provider results for the same work differ only in a trailing
// parenthesised edition qualifier (e.g. "Dune" vs "Dune (German Edition)"),
// ingestion must create exactly one book row, not two.
func TestFetchAuthorBooks_DedupsEditionSuffix(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL900A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Two works for the same book — one with, one without the edition suffix.
	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL901W", Title: "Dune", SortTitle: "dune", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL902W", Title: "Dune (German Edition)", SortTitle: "dune german edition", Language: "ger", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book after dedup, got %d: %v", len(books), func() []string {
			var titles []string
			for _, b := range books {
				titles = append(titles, b.Title)
			}
			return titles
		}())
	}
}

// TestFetchAuthorBooks_DedupsExistingRows verifies that when the DB already
// contains a row for "Dune (German Edition)" and the provider returns "Dune",
// the sync does not create a second row — the existing row is recognised as the
// same work via NormalizeTitleForDedup.
func TestFetchAuthorBooks_DedupsExistingRows(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL910A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Seed the DB with the edition-qualified title (simulates an older sync).
	existing := &models.Book{
		ForeignID: "OL911W", AuthorID: author.ID,
		Title: "Dune (German Edition)", SortTitle: "dune german edition",
		Language: "ger", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// Provider now returns the non-qualified form.
	stub := &stubMetaProvider{
		works: []models.Book{
			{ForeignID: "OL912W", Title: "Dune", SortTitle: "dune", Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book (no duplicate created), got %d", len(books))
	}
}

// fixedAuthorProvider is a minimal metadata provider whose GetAuthor always
// returns a pre-set author, regardless of the foreignID argument. Used to
// simulate the race-condition path in TestCreateAuthor_DuplicateConstraint.
type fixedAuthorProvider struct {
	stubMetaProvider
	result *models.Author
}

func (p *fixedAuthorProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return p.result, nil
}

type searchableAuthorProvider struct {
	stubMetaProvider
	searchAuthorsByQuery map[string][]models.Author
	authors              map[string]*models.Author
	searchAuthorsErr     error
	searchAuthorQueries  []string
	getAuthorErr         error
	getAuthorCalls       int
}

func (p *searchableAuthorProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	p.searchAuthorQueries = append(p.searchAuthorQueries, query)
	if p.searchAuthorsErr != nil {
		return nil, p.searchAuthorsErr
	}
	return p.searchAuthorsByQuery[query], nil
}

func (p *searchableAuthorProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	p.getAuthorCalls++
	if p.getAuthorErr != nil {
		return nil, p.getAuthorErr
	}
	if p.authors == nil {
		return nil, nil
	}
	if author := p.authors[foreignID]; author != nil {
		copy := *author
		return &copy, nil
	}
	return nil, nil
}

type relinkUpstreamFixture struct {
	ctx     context.Context
	authors *db.AuthorRepo
	aliases *db.AuthorAliasRepo
	books   *db.BookRepo
	handler *AuthorHandler
}

func newRelinkUpstreamFixture(t *testing.T, provider metadata.Provider, enrichers ...metadata.Provider) *relinkUpstreamFixture {
	t.Helper()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	return &relinkUpstreamFixture{
		ctx:     context.Background(),
		authors: authorRepo,
		aliases: aliasRepo,
		books:   bookRepo,
		handler: NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider, enrichers...), nil, profileRepo, nil),
	}
}

func (f *relinkUpstreamFixture) createAuthor(t *testing.T, author *models.Author) *models.Author {
	t.Helper()
	if err := f.authors.Create(f.ctx, author); err != nil {
		t.Fatal(err)
	}
	return author
}

func (f *relinkUpstreamFixture) relink(t *testing.T, authorID int64) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/"+strconv.FormatInt(authorID, 10)+"/relink-upstream", bytes.NewReader(nil))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	f.handler.RelinkUpstream(rec, req)
	return rec
}

func (f *relinkUpstreamFixture) relinkTo(t *testing.T, authorID int64, foreignID string) *httptest.ResponseRecorder {
	t.Helper()

	return f.relinkToCandidate(t, authorID, foreignID, "")
}

func (f *relinkUpstreamFixture) relinkToCandidate(t *testing.T, authorID int64, foreignID, authorName string) *httptest.ResponseRecorder {
	t.Helper()

	return f.relinkToCandidateAs(t, 0, authorID, foreignID, authorName)
}

func (f *relinkUpstreamFixture) relinkToCandidateAs(t *testing.T, userID int64, authorID int64, foreignID, authorName string) *httptest.ResponseRecorder {
	t.Helper()

	payload := map[string]string{"foreignAuthorId": foreignID}
	if authorName != "" {
		payload["authorName"] = authorName
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/"+strconv.FormatInt(authorID, 10)+"/relink-upstream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	f.handler.RelinkUpstream(rec, req)
	return rec
}

func (f *relinkUpstreamFixture) candidates(t *testing.T, authorID int64, term string) *httptest.ResponseRecorder {
	t.Helper()

	return f.candidatesAs(t, 0, authorID, term)
}

func (f *relinkUpstreamFixture) candidatesAs(t *testing.T, userID int64, authorID int64, term string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(authorID, 10)+"/relink-upstream/candidates?term="+term, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	f.handler.RelinkCandidates(rec, req)
	return rec
}

// TestCreateAuthor_DuplicateConstraint_Returns409 is a regression test for
// issue #91: when the database INSERT fails with a UNIQUE constraint violation
// (the race-condition path where GetByForeignID passes but the row already
// exists by the time the INSERT executes), the handler must return 409 instead
// of 500.
func TestCreateAuthor_DuplicateConstraint_Returns409(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	const existingForeignID = "OL_DUPL_99A"

	// Pre-populate the DB with an author that has existingForeignID.
	existing := &models.Author{
		ForeignID: existingForeignID, Name: "Existing Author", SortName: "Author, Existing",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// The metadata provider returns an author with the *same* ForeignID as the
	// one already in the DB, even though the request uses a different ID. This
	// reproduces the race-condition path: GetByForeignID("OL_NEW") returns nil
	// (check passes), but meta.GetAuthor returns ForeignID="OL_DUPL_99A" which
	// already exists, so the INSERT hits the UNIQUE constraint.
	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        existingForeignID,
			Name:             "Existing Author",
			SortName:         "Author, Existing",
			MetadataProvider: "openlibrary",
		},
	}
	agg := metadata.NewAggregator(provider)

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL_NEW_99A", // different from existingForeignID — GetByForeignID passes
		"authorName":      "Existing Author",
		"monitored":       false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for UNIQUE constraint violation, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal("response body not valid JSON:", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field in response body")
	}
	if _, ok := resp["canonicalAuthor"]; !ok {
		t.Error("expected canonicalAuthor in response body")
	}
}

func TestCreateAuthor_DirectDuplicateReturnsCanonicalAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(&stubMetaProvider{}), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL13200512A",
		"authorName":      "Emilia Jae",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(existing.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], existing.ID)
	}
	if resp["canonicalAuthor"] == nil {
		t.Fatalf("canonicalAuthor missing from response: %+v", resp)
	}
}

func TestCreateAuthor_AlternateDuplicateReturnsCanonicalAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, existing.ID, "hc:emilia-jae"); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(&stubMetaProvider{}), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "hc:emilia-jae",
		"authorName":      "Emilia Jae",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(existing.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], existing.ID)
	}
	if resp["canonicalAuthor"] == nil {
		t.Fatalf("canonicalAuthor missing from response: %+v", resp)
	}
}

func TestCreateAuthor_UsesCanonicalMetadataAndRecordsAlias(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL23919A",
			Name:             "J. K. Rowling",
			SortName:         "Rowling, J. K.",
			Description:      "Canonical author row",
			ImageURL:         "https://example.com/jk.jpg",
			MetadataProvider: "openlibrary",
		},
	}

	h := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL23919A",
		"authorName":      "J.K. Rowling",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	authors, err := authorRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	if authors[0].Name != "J. K. Rowling" {
		t.Fatalf("name = %q, want canonical name", authors[0].Name)
	}
	aliases, err := aliasRepo.ListByAuthor(context.Background(), authors[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "J.K. Rowling" {
		t.Fatalf("aliases = %+v, want J.K. Rowling alias", aliases)
	}
}

func TestCreateAuthor_UsesGlobalMonitorDefaultsWhenOmitted(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	if err := settingsRepo.Set(ctx, SettingAuthorDefaultMonitorMode, models.AuthorMonitorModeFuture); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, SettingAuthorDefaultMonitorLatestCount, "4"); err != nil {
		t.Fatal(err)
	}

	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL-GLOBAL-A",
			Name:             "Global Defaults",
			SortName:         "Defaults, Global",
			MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), settingsRepo, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL-GLOBAL-A",
		"authorName":      "Global Defaults",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := authorRepo.GetByForeignID(ctx, "OL-GLOBAL-A")
	if err != nil || got == nil {
		t.Fatalf("fetch author: %v, got=%+v", err, got)
	}
	if got.MonitorMode != models.AuthorMonitorModeFuture || got.MonitorLatestCount != 4 {
		t.Fatalf("monitor defaults = %q/%d, want future/4", got.MonitorMode, got.MonitorLatestCount)
	}
}

func TestUpdateAuthor_ApplyMonitorModeToExistingBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID:        "OL-APPLY-A",
		Name:             "Apply Author",
		SortName:         "Author, Apply",
		MetadataProvider: "openlibrary",
		Monitored:        true,
		MonitorMode:      models.AuthorMonitorModeAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	oldDate := time.Now().UTC().AddDate(0, 0, -3)
	futureDate := time.Now().UTC().AddDate(0, 0, 3)
	books := []*models.Book{
		{ForeignID: "OL-APPLY-OLD", AuthorID: author.ID, Title: "Old Book", SortTitle: "old book", ReleaseDate: &oldDate, Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"},
		{ForeignID: "OL-APPLY-FUTURE", AuthorID: author.ID, Title: "Future Book", SortTitle: "future book", ReleaseDate: &futureDate, Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"},
		{ForeignID: "OL-APPLY-UNKNOWN", AuthorID: author.ID, Title: "Unknown Book", SortTitle: "unknown book", Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"},
		{ForeignID: "OL-APPLY-EXCLUDED", AuthorID: author.ID, Title: "Excluded Book", SortTitle: "excluded book", ReleaseDate: &futureDate, Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"},
	}
	for _, book := range books {
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
	}
	if err := bookRepo.SetExcluded(ctx, books[3].ID, true); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"monitorMode":                models.AuthorMonitorModeFuture,
		"applyMonitorModeToExisting": true,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/author/"+strconv.FormatInt(author.ID, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	gotBooks, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, book := range gotBooks {
		got[book.Title] = book.Monitored
	}
	want := map[string]bool{
		"Old Book":      false,
		"Future Book":   true,
		"Unknown Book":  false,
		"Excluded Book": false,
	}
	for title, wantMonitored := range want {
		if got[title] != wantMonitored {
			t.Errorf("%s monitored = %v, want %v", title, got[title], wantMonitored)
		}
	}
}

// TestUpdateAuthor_ApplyMonitorModeSeries covers the per-series monitor mode
// (#810): selected series → monitored, others → unmonitored, excluded books
// stay unmonitored regardless of series membership.
func TestUpdateAuthor_ApplyMonitorModeSeries(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID: "OL-S-A", Name: "Series Author", SortName: "Author, Series",
		MetadataProvider: "openlibrary", Monitored: true, MonitorMode: models.AuthorMonitorModeAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// 5 books: 3 in series A, 1 in series B, 1 standalone.
	mk := func(fid, title string) *models.Book {
		return &models.Book{
			ForeignID: fid, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary",
		}
	}
	a1, a2, a3 := mk("OL-A1", "A1"), mk("OL-A2", "A2"), mk("OL-A3", "A3")
	b1 := mk("OL-B1", "B1")
	solo := mk("OL-SOLO", "Solo")
	for _, b := range []*models.Book{a1, a2, a3, b1, solo} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	// Exclude one of the A-series books — it must stay unmonitored even
	// though the user picks series A.
	if err := bookRepo.SetExcluded(ctx, a3.ID, true); err != nil {
		t.Fatal(err)
	}

	sA := &models.Series{ForeignID: "ol-series:A", Title: "A"}
	sB := &models.Series{ForeignID: "ol-series:B", Title: "B"}
	if err := seriesRepo.CreateOrGet(ctx, sA); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.CreateOrGet(ctx, sB); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a1.ID, a2.ID, a3.ID} {
		if err := seriesRepo.LinkBook(ctx, sA.ID, id, "", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := seriesRepo.LinkBook(ctx, sB.ID, b1.ID, "", true); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo, nil, nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"monitorMode":                models.AuthorMonitorModeSeries,
		"monitoredSeriesIds":         []int64{sA.ID},
		"applyMonitorModeToExisting": true,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/author/"+strconv.FormatInt(author.ID, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]bool{}
	for _, b := range got {
		byTitle[b.Title] = b.Monitored
	}
	want := map[string]bool{
		"A1":   true,  // in selected series
		"A2":   true,  // in selected series
		"A3":   false, // excluded — wins over series membership
		"B1":   false, // series B not selected
		"Solo": false, // no series at all
	}
	for title, wantMon := range want {
		if byTitle[title] != wantMon {
			t.Errorf("%s monitored = %v, want %v", title, byTitle[title], wantMon)
		}
	}

	// Verify the response carries the pinned series IDs back.
	var respAuthor models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &respAuthor); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(respAuthor.MonitoredSeriesIDs) != 1 || respAuthor.MonitoredSeriesIDs[0] != sA.ID {
		t.Errorf("response MonitoredSeriesIDs = %v, want [%d]", respAuthor.MonitoredSeriesIDs, sA.ID)
	}
}

// TestUpdateAuthor_RejectsForeignSeriesID covers the validation that a
// monitored series id must belong to the author. Accepting any id would let
// one author's pin set reference an unrelated catalog row.
func TestUpdateAuthor_RejectsForeignSeriesID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	owner := &models.Author{ForeignID: "OL-OWN", Name: "Owner", SortName: "Owner", MetadataProvider: "openlibrary", Monitored: true}
	other := &models.Author{ForeignID: "OL-OTH", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	ownerBook := &models.Book{ForeignID: "OL-OB", AuthorID: owner.ID, Title: "OB", SortTitle: "ob", Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"}
	otherBook := &models.Book{ForeignID: "OL-XB", AuthorID: other.ID, Title: "XB", SortTitle: "xb", Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := bookRepo.Create(ctx, ownerBook); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.Create(ctx, otherBook); err != nil {
		t.Fatal(err)
	}

	ownerSeries := &models.Series{ForeignID: "ol-series:own", Title: "Own"}
	foreignSeries := &models.Series{ForeignID: "ol-series:foreign", Title: "Foreign"}
	if err := seriesRepo.CreateOrGet(ctx, ownerSeries); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.CreateOrGet(ctx, foreignSeries); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, ownerSeries.ID, ownerBook.ID, "", true); err != nil {
		t.Fatal(err)
	}
	// foreignSeries is linked only to other author's book.
	if err := seriesRepo.LinkBook(ctx, foreignSeries.ID, otherBook.ID, "", true); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo, nil, nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"monitorMode":        models.AuthorMonitorModeSeries,
		"monitoredSeriesIds": []int64{foreignSeries.ID},
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/author/"+strconv.FormatInt(owner.ID, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(owner.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for foreign series id, got %d: %s", rec.Code, rec.Body.String())
	}
	// Nothing should have been persisted.
	got, _ := authorRepo.ListMonitoredSeriesIDs(ctx, owner.ID)
	if len(got) != 0 {
		t.Errorf("expected empty pin set after rejection, got %v", got)
	}
}

// TestListAuthorSeries returns only series the author actually has books in.
func TestListAuthorSeries(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-LS", Name: "LS", SortName: "LS", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-LS-B", AuthorID: a.ID, Title: "T", SortTitle: "t", Status: models.BookStatusWanted, Monitored: true, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "ol-series:t", Title: "T-Series"}
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	// A second unlinked series exists globally — must not appear in the
	// per-author response.
	noise := &models.Series{ForeignID: "ol-series:noise", Title: "Noise"}
	if err := seriesRepo.CreateOrGet(ctx, noise); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, seriesRepo, nil, nil, profileRepo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(a.ID, 10)+"/series", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(a.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.ListSeries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got []models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != s.ID {
		t.Fatalf("got %d series, want only the linked one (id %d): %+v", len(got), s.ID, got)
	}
	// Regression guard for #1209: the series payload must carry its book
	// membership. Books has `json:",omitempty"`, so an empty slice is dropped
	// from JSON entirely; assert against the raw body that the "books" key is
	// present and that the membership round-trips with the linked book.
	if got[0].Books == nil || len(got[0].Books) != 1 || got[0].Books[0].BookID != book.ID {
		t.Fatalf("series books not populated: %+v", got[0].Books)
	}
	if !strings.Contains(rec.Body.String(), `"books"`) {
		t.Fatalf("response JSON missing books array (omitempty drop): %s", rec.Body.String())
	}
}

func TestCreateAuthor_RelinksExistingABSAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "abs:author:lib-books:author-tolkien",
		Name:             "J. R. R. Tolkien",
		SortName:         "Tolkien, J. R. R.",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL26320A",
			Name:             "J.R.R. Tolkien",
			SortName:         "Tolkien, J.R.R.",
			Description:      "Author of The Hobbit.",
			ImageURL:         "https://example.com/tolkien.jpg",
			MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL26320A",
		"authorName":      "J.R.R. Tolkien",
		"monitored":       true,
		"searchOnAdd":     false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	got := authors[0]
	if got.ID != existing.ID || got.ForeignID != "OL26320A" || got.Name != "J.R.R. Tolkien" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("author = %+v, want relinked existing Tolkien", got)
	}
	aliases, err := aliasRepo.ListByAuthor(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "J. R. R. Tolkien" {
		t.Fatalf("aliases = %+v, want old ABS spelling", aliases)
	}
}

func TestCreateAuthor_HiddenIdentifierConflictDoesNotLeakCanonicalAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	aliceAuthor := &models.Author{
		ForeignID:        "hc:emilia-jae",
		Name:             "Canonical Emilia",
		SortName:         "Emilia, Canonical",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, aliceAuthor.ID, "OL13200512A"); err != nil {
		t.Fatalf("seed alice identifier: %v", err)
	}
	bobAuthor := &models.Author{
		ForeignID:        "abs:author:bob:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, bobAuthor, bob.ID); err != nil {
		t.Fatalf("seed bob author: %v", err)
	}

	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL13200512A",
			Name:             "Emilia Jae",
			SortName:         "Jae, Emilia",
			MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL13200512A",
		"authorName":      "Emilia Jae",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUserID(req.Context(), bob.ID))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["canonicalAuthorId"]; ok {
		t.Fatalf("response leaked canonicalAuthorId: %v", resp)
	}
	if _, ok := resp["canonicalAuthor"]; ok {
		t.Fatalf("response leaked canonicalAuthor: %v", resp)
	}
	got, err := authorRepo.GetByID(ctx, bobAuthor.ID)
	if err != nil || got == nil {
		t.Fatalf("bob author lookup: %+v err=%v", got, err)
	}
	if got.ForeignID != bobAuthor.ForeignID || got.MetadataProvider != bobAuthor.MetadataProvider {
		t.Fatalf("bob author mutated unexpectedly: %+v", got)
	}
}

func TestCreateAuthor_HiddenPrimaryConflictDoesNotLeakCanonicalAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	aliceAuthor := &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Canonical Emilia",
		SortName:         "Emilia, Canonical",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}

	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL13200512A",
			Name:             "Emilia Jae",
			SortName:         "Jae, Emilia",
			MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL13200512A",
		"authorName":      "Emilia Jae",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUserID(req.Context(), bob.ID))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["canonicalAuthorId"]; ok {
		t.Fatalf("response leaked canonicalAuthorId: %v", resp)
	}
	if _, ok := resp["canonicalAuthor"]; ok {
		t.Fatalf("response leaked canonicalAuthor: %v", resp)
	}
	if got, err := authorRepo.GetByID(ctx, aliceAuthor.ID); err != nil || got == nil || got.ForeignID != aliceAuthor.ForeignID {
		t.Fatalf("alice author after Create = %+v err=%v", got, err)
	}
}

func TestCreateAuthor_RejectsNormalizedDuplicate(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL23919A",
		Name:             "J. K. Rowling",
		SortName:         "Rowling, J. K.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	provider := &fixedAuthorProvider{
		result: &models.Author{
			ForeignID:        "OL_NEW_ROWLING",
			Name:             "J.K. Rowling",
			SortName:         "Rowling, J.K.",
			MetadataProvider: "openlibrary",
		},
	}
	h := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignAuthorId": "OL_NEW_ROWLING",
		"authorName":      "J.K. Rowling",
		"monitored":       true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(existing.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], existing.ID)
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
}

func TestRelinkUpstream_RelinksPlaceholderAuthorUsingInitialsFallback(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		searchAuthorsByQuery: map[string][]models.Author{
			"J.R.R. Tolkien": {{ForeignID: "OL26320A", Name: "J.R.R. Tolkien"}},
		},
		authors: map[string]*models.Author{
			"OL26320A": {
				ForeignID:        "OL26320A",
				Name:             "J.R.R. Tolkien",
				SortName:         "Tolkien, J.R.R.",
				Description:      "Author of The Hobbit.",
				ImageURL:         "https://example.com/tolkien.jpg",
				MetadataProvider: "openlibrary",
			},
		},
	})

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:lib-books:author-tolkien",
		Name:             "J. R. R. Tolkien",
		SortName:         "Tolkien, J. R. R.",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	rec := fixture.relink(t, existing.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != "OL26320A" || got.Name != "J.R.R. Tolkien" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("author = %+v, want relinked upstream Tolkien", got)
	}
	aliases, err := fixture.aliases.ListByAuthor(fixture.ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "J. R. R. Tolkien" || aliases[0].SourceOLID != "abs:author:lib-books:author-tolkien" {
		t.Fatalf("aliases = %+v, want original placeholder spelling with previous foreign id", aliases)
	}
}

func TestRelinkUpstream_InvalidRequestBodyReturns400(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{})
	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:library:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/"+strconv.FormatInt(existing.ID, 10)+"/relink-upstream", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(existing.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	fixture.handler.RelinkUpstream(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelinkUpstream_PreservesNonNormalizedPreviousNameAlias(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		authors: map[string]*models.Author{
			"OL398175A": {
				ForeignID:        "OL398175A",
				Name:             "Donald Trump",
				SortName:         "Trump, Donald",
				MetadataProvider: "openlibrary",
			},
		},
	})

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:lib-books:donald-j-trump",
		Name:             "Donald J. Trump",
		SortName:         "Trump, Donald J.",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	rec := fixture.relinkToCandidate(t, existing.ID, "OL398175A", "Donald Trump")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != "OL398175A" || got.Name != "Donald Trump" {
		t.Fatalf("author = %+v, want relinked Donald Trump", got)
	}
	aliases, err := fixture.aliases.ListByAuthor(fixture.ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].AuthorID != got.ID || aliases[0].Name != "Donald J. Trump" {
		t.Fatalf("aliases = %+v, want previous name pointing at relinked author", aliases)
	}
	if aliases[0].SourceOLID != "abs:author:lib-books:donald-j-trump" {
		t.Fatalf("alias sourceOlId = %q, want previous foreign id", aliases[0].SourceOLID)
	}
}

func TestRelinkUpstream_PreviousNameAliasFallsBackToNewForeignID(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		authors: map[string]*models.Author{
			"OL-CANON": {
				ForeignID:        "OL-CANON",
				Name:             "Canonical Author",
				SortName:         "Author, Canonical",
				MetadataProvider: "openlibrary",
			},
		},
	})

	existing := fixture.createAuthor(t, &models.Author{
		Name:             "Placeholder Author",
		SortName:         "Author, Placeholder",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	rec := fixture.relinkToCandidate(t, existing.ID, "OL-CANON", "Canonical Author")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	aliases, err := fixture.aliases.ListByAuthor(fixture.ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "Placeholder Author" {
		t.Fatalf("aliases = %+v, want previous name alias", aliases)
	}
	if aliases[0].SourceOLID != "OL-CANON" {
		t.Fatalf("alias sourceOlId = %q, want new upstream foreign id fallback", aliases[0].SourceOLID)
	}
}

func TestRelinkUpstream_AutomaticUsesPrimaryProviderWhenEnricherAlsoMatches(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t,
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "openlibrary"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Emilia Jae": {{ForeignID: "OL13200512A", Name: "Emilia Jae", MetadataProvider: "openlibrary"}},
			},
			authors: map[string]*models.Author{
				"OL13200512A": {
					ForeignID:        "OL13200512A",
					Name:             "Emilia Jae",
					SortName:         "Jae, Emilia",
					Description:      "OpenLibrary author.",
					MetadataProvider: "openlibrary",
				},
			},
		},
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Emilia Jae": {{ForeignID: "hc:emilia-jae", Name: "Emilia Jae", MetadataProvider: "hardcover"}},
			},
		},
	)

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:library:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})

	rec := fixture.relink(t, existing.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != "OL13200512A" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("author = %+v, want primary OpenLibrary relink", got)
	}
	aliases, err := fixture.aliases.ListByAuthor(fixture.ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases = %+v, want no alias for exact same-name relink", aliases)
	}
}

func TestRelinkUpstream_ManualCandidateReplacesSparseCanonicalLink(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t,
		&searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}},
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover"},
			authors: map[string]*models.Author{
				"hc:emilia-jae": {
					ForeignID:        "hc:emilia-jae",
					Name:             "Emilia Jae",
					SortName:         "Jae, Emilia",
					Description:      "Fantasy author.",
					ImageURL:         "https://example.com/emilia.jpg",
					MetadataProvider: "hardcover",
				},
			},
		},
	)

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:          "OL13200512A",
		Name:               "Emilia Jae",
		SortName:           "Jae, Emilia",
		MetadataProvider:   "openlibrary",
		Monitored:          true,
		MonitorMode:        models.AuthorMonitorModeLatest,
		MonitorLatestCount: 2,
	})
	book := &models.Book{
		ForeignID:        "hc:a-throne-of-wings-and-embers-2024",
		AuthorID:         existing.ID,
		Title:            "A Throne of Wings and Embers",
		SortTitle:        "A Throne of Wings and Embers",
		Status:           models.BookStatusImported,
		MediaType:        models.MediaTypeAudiobook,
		MetadataProvider: "hardcover",
	}
	if err := fixture.books.Create(fixture.ctx, book); err != nil {
		t.Fatal(err)
	}

	rec := fixture.relinkTo(t, existing.ID, "hc:emilia-jae")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != existing.ID || got.ForeignID != "hc:emilia-jae" || got.MetadataProvider != "hardcover" {
		t.Fatalf("author identity = %+v, want same ID relinked to Hardcover", got)
	}
	oldIdentifier, err := fixture.authors.GetAuthorIdentifier(fixture.ctx, "OL13200512A")
	if err != nil {
		t.Fatal(err)
	}
	if oldIdentifier == nil || oldIdentifier.AuthorID != existing.ID {
		t.Fatalf("old identifier = %+v, want attached to relinked author %d", oldIdentifier, existing.ID)
	}
	newIdentifier, err := fixture.authors.GetAuthorIdentifier(fixture.ctx, "hc:emilia-jae")
	if err != nil {
		t.Fatal(err)
	}
	if newIdentifier == nil || newIdentifier.AuthorID != existing.ID {
		t.Fatalf("new identifier = %+v, want attached to relinked author %d", newIdentifier, existing.ID)
	}
	if got.Description != "Fantasy author." || got.ImageURL != "https://example.com/emilia.jpg" {
		t.Fatalf("author metadata not updated: %+v", got)
	}
	if got.MonitorMode != models.AuthorMonitorModeLatest || got.MonitorLatestCount != 2 || !got.Monitored {
		t.Fatalf("monitor settings changed unexpectedly: %+v", got)
	}
	books, err := fixture.books.ListByAuthor(fixture.ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != book.ID {
		t.Fatalf("books = %+v, want existing book preserved", books)
	}
}

func TestRelinkUpstream_ManualCandidateFallsBackToVerifiedSearchResult(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t,
		&searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}},
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "dnb"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Emilia Jae": {{
					ForeignID:        "dnb:123456789",
					Name:             "Emilia Jae",
					SortName:         "Jae, Emilia",
					Description:      "DNB author record.",
					MetadataProvider: "dnb",
				}},
			},
			getAuthorErr: errors.New("dnb does not support author lookup by ID"),
		},
	)

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.relinkToCandidate(t, existing.ID, "dnb:123456789", "Emilia Jae")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != "dnb:123456789" || got.MetadataProvider != "dnb" || got.Description != "DNB author record." {
		t.Fatalf("author = %+v, want DNB search candidate relink", got)
	}
}

func TestRelinkUpstream_AllowsIdentifierAlreadyAttachedToSameAuthor(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		authors: map[string]*models.Author{
			"OL13200512A": {
				ForeignID:        "OL13200512A",
				Name:             "Emilia Jae",
				SortName:         "Jae, Emilia",
				MetadataProvider: "openlibrary",
			},
		},
	})
	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "hc:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "hardcover",
		Monitored:        true,
	})
	if err := fixture.authors.UpsertAuthorIdentifier(fixture.ctx, existing.ID, "OL13200512A"); err != nil {
		t.Fatal(err)
	}

	rec := fixture.relinkTo(t, existing.ID, "OL13200512A")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != "OL13200512A" || got.MetadataProvider != "openlibrary" {
		t.Fatalf("author = %+v, want alternate promoted to primary", got)
	}
}

func TestRelinkUpstream_ManualCandidateRejectsUnverifiedFallback(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t,
		&searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}},
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "dnb"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Emilia Jae": {{ForeignID: "dnb:other", Name: "Emilia Jae", MetadataProvider: "dnb"}},
			},
			getAuthorErr: errors.New("dnb does not support author lookup by ID"),
		},
	)

	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.relinkToCandidate(t, existing.ID, "dnb:missing", "Emilia Jae")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := fixture.authors.GetByID(fixture.ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ForeignID != existing.ForeignID || got.MetadataProvider != existing.MetadataProvider {
		t.Fatalf("author mutated unexpectedly: %+v", got)
	}
}

func TestRelinkUpstream_RejectsOtherUsersAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	handler := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(&searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover"},
		authors: map[string]*models.Author{
			"hc:emilia-jae": {
				ForeignID:        "hc:emilia-jae",
				Name:             "Emilia Jae",
				SortName:         "Jae, Emilia",
				MetadataProvider: "hardcover",
			},
		},
	}), nil, profileRepo, nil)
	aliceAuthor := &models.Author{
		ForeignID:        "abs:author:alice:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"foreignAuthorId": "hc:emilia-jae"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/"+strconv.FormatInt(aliceAuthor.ID, 10)+"/relink-upstream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(aliceAuthor.ID, 10))
	req = req.WithContext(auth.WithUserID(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), bob.ID))
	rec := httptest.NewRecorder()

	handler.RelinkUpstream(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := authorRepo.GetByID(ctx, aliceAuthor.ID)
	if err != nil || got == nil {
		t.Fatalf("alice author lookup: %+v err=%v", got, err)
	}
	if got.ForeignID != aliceAuthor.ForeignID || got.MetadataProvider != aliceAuthor.MetadataProvider {
		t.Fatalf("alice author mutated: %+v", got)
	}
}

func TestRelinkUpstream_HiddenIdentifierConflictDoesNotLeakCanonicalAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authorRepo := db.NewAuthorRepo(database)
	fixture := &relinkUpstreamFixture{
		ctx:     ctx,
		authors: authorRepo,
		aliases: db.NewAuthorAliasRepo(database),
		books:   db.NewBookRepo(database),
		handler: NewAuthorHandler(authorRepo, db.NewAuthorAliasRepo(database), db.NewBookRepo(database), nil, metadata.NewAggregator(&searchableAuthorProvider{
			authors: map[string]*models.Author{
				"OL13200512A": {
					ForeignID:        "OL13200512A",
					Name:             "Emilia Jae",
					SortName:         "Jae, Emilia",
					MetadataProvider: "openlibrary",
				},
			},
		}), nil, db.NewMetadataProfileRepo(database), nil),
	}
	aliceAuthor := &models.Author{
		ForeignID:        "hc:emilia-jae",
		Name:             "Canonical Emilia",
		SortName:         "Emilia, Canonical",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, aliceAuthor.ID, "OL13200512A"); err != nil {
		t.Fatalf("seed alice identifier: %v", err)
	}
	bobAuthor := &models.Author{
		ForeignID:        "abs:author:bob:emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, bobAuthor, bob.ID); err != nil {
		t.Fatalf("seed bob author: %v", err)
	}

	rec := fixture.relinkToCandidateAs(t, bob.ID, bobAuthor.ID, "OL13200512A", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["canonicalAuthorId"]; ok {
		t.Fatalf("response leaked canonicalAuthorId: %v", resp)
	}
	if _, ok := resp["canonicalAuthor"]; ok {
		t.Fatalf("response leaked canonicalAuthor: %v", resp)
	}
	got, err := authorRepo.GetByID(ctx, bobAuthor.ID)
	if err != nil || got == nil {
		t.Fatalf("bob author lookup: %+v err=%v", got, err)
	}
	if got.ForeignID != bobAuthor.ForeignID || got.MetadataProvider != bobAuthor.MetadataProvider {
		t.Fatalf("bob author mutated unexpectedly: %+v", got)
	}
}

func TestRelinkCandidates_SearchesPrimaryAndEnrichers(t *testing.T) {
	primary := &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{
			"Emilia Jae": {{
				ForeignID:        "OL13200512A",
				Name:             "Emilia Jae",
				ImageURL:         "https://example.com/search-emilia.jpg",
				MetadataProvider: "openlibrary",
			}},
		},
		authors: map[string]*models.Author{
			"OL13200512A": {
				ForeignID:        "OL13200512A",
				Name:             "Emilia Jae",
				ImageURL:         "https://example.com/full-emilia.jpg",
				MetadataProvider: "openlibrary",
			},
		},
	}
	fixture := newRelinkUpstreamFixture(t,
		primary,
		&searchableAuthorProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover"},
			searchAuthorsByQuery: map[string][]models.Author{
				"Emilia Jae": {{ForeignID: "hc:emilia-jae", Name: "Emilia Jae", Description: "Fantasy author.", MetadataProvider: "hardcover"}},
			},
		},
	)
	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.candidates(t, existing.ID, "Emilia%20Jae")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []models.Author
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1: %+v", len(got), got)
	}
	if got[0].ForeignID != "hc:emilia-jae" {
		t.Fatalf("candidate ids = %+v", got)
	}
	if primary.getAuthorCalls != 0 {
		t.Fatalf("GetAuthor calls = %d, want 0 for candidate image hydration", primary.getAuthorCalls)
	}
	if got[0].ImageURL != "" {
		t.Fatalf("hardcover candidate image = %q, want unchanged empty image", got[0].ImageURL)
	}
}

func TestRelinkCandidates_DefaultsToAuthorNameAndReturnsEmptySlice(t *testing.T) {
	provider := &searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}}
	fixture := newRelinkUpstreamFixture(t, provider)
	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.candidates(t, existing.ID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "[]\n" {
		t.Fatalf("body = %q, want empty JSON array", got)
	}
	if len(provider.searchAuthorQueries) != 1 || provider.searchAuthorQueries[0] != "Emilia Jae" {
		t.Fatalf("search queries = %+v, want fallback author name", provider.searchAuthorQueries)
	}
}

func TestRelinkCandidates_ReturnsBadGatewayOnProviderError(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		searchAuthorsErr: errors.New("provider unavailable"),
	})
	existing := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})

	rec := fixture.candidates(t, existing.ID, "Emilia%20Jae")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelinkCandidates_ReturnsFailedDependencyWithoutMetadataAggregator(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	existing := &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	handler := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(existing.ID, 10)+"/relink-upstream/candidates", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(existing.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	handler.RelinkCandidates(rec, req)

	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("expected 424, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelinkUpstream_RejectsCanonicalConflict(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		searchAuthorsByQuery: map[string][]models.Author{
			"Andrzej Sapkowski": {{ForeignID: "OL368638A", Name: "Andrzej Sapkowski"}},
		},
		authors: map[string]*models.Author{
			"OL368638A": {
				ForeignID:        "OL368638A",
				Name:             "Andrzej Sapkowski",
				SortName:         "Sapkowski, Andrzej",
				MetadataProvider: "openlibrary",
			},
		},
	})

	placeholder := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:lib-books:author-sapkowski",
		Name:             "Andrzej Sapkowski",
		SortName:         "Sapkowski, Andrzej",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	canonical := fixture.createAuthor(t, &models.Author{
		ForeignID:        "OL368638A",
		Name:             "Canonical Sapkowski",
		SortName:         "Sapkowski, Canonical",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})
	rec := fixture.relink(t, placeholder.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(canonical.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], canonical.ID)
	}
	got, err := fixture.authors.GetByID(fixture.ctx, placeholder.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID placeholder: %v", err)
	}
	if got.ForeignID != placeholder.ForeignID || got.MetadataProvider != placeholder.MetadataProvider {
		t.Fatalf("placeholder author mutated unexpectedly: %+v", got)
	}
}

func TestRelinkUpstream_RejectsAlternateIdentifierConflict(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		authors: map[string]*models.Author{
			"OL13200512A": {
				ForeignID:        "OL13200512A",
				Name:             "Emilia Jae",
				SortName:         "Jae, Emilia",
				MetadataProvider: "openlibrary",
			},
		},
	})

	placeholder := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:lib-books:author-emilia-jae",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	canonical := fixture.createAuthor(t, &models.Author{
		ForeignID:        "hc:emilia-jae",
		Name:             "Canonical Emilia",
		SortName:         "Emilia, Canonical",
		MetadataProvider: "hardcover",
		Monitored:        true,
	})
	if err := fixture.authors.UpsertAuthorIdentifier(fixture.ctx, canonical.ID, "OL13200512A"); err != nil {
		t.Fatal(err)
	}

	rec := fixture.relinkTo(t, placeholder.ID, "OL13200512A")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(canonical.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], canonical.ID)
	}
	got, err := fixture.authors.GetByID(fixture.ctx, placeholder.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID placeholder: %v", err)
	}
	if got.ForeignID != placeholder.ForeignID || got.MetadataProvider != placeholder.MetadataProvider {
		t.Fatalf("placeholder author mutated unexpectedly: %+v", got)
	}
}

func TestRelinkUpstream_RejectsCanonicalNameConflict(t *testing.T) {
	fixture := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		searchAuthorsByQuery: map[string][]models.Author{
			"J.R.R. Tolkien": {{ForeignID: "OL26320A", Name: "J.R.R. Tolkien"}},
		},
		authors: map[string]*models.Author{
			"OL26320A": {
				ForeignID:        "OL26320A",
				Name:             "J.R.R. Tolkien",
				SortName:         "Tolkien, J.R.R.",
				Description:      "Author of The Hobbit.",
				MetadataProvider: "openlibrary",
			},
		},
	})

	placeholder := fixture.createAuthor(t, &models.Author{
		ForeignID:        "abs:author:lib-books:author-tolkien",
		Name:             "J. R. R. Tolkien",
		SortName:         "Tolkien, J. R. R.",
		MetadataProvider: "audiobookshelf",
		Monitored:        true,
	})
	canonical := fixture.createAuthor(t, &models.Author{
		ForeignID:        "manual:tolkien",
		Name:             "J.R.R. Tolkien",
		SortName:         "Tolkien, J.R.R.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	})
	rec := fixture.relink(t, placeholder.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["canonicalAuthorId"].(float64)) != int(canonical.ID) {
		t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], canonical.ID)
	}
	got, err := fixture.authors.GetByID(fixture.ctx, placeholder.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID placeholder: %v", err)
	}
	if got.ForeignID != placeholder.ForeignID || got.MetadataProvider != placeholder.MetadataProvider {
		t.Fatalf("placeholder author mutated unexpectedly: %+v", got)
	}
}

// TestFetchAuthorBooks_DeduplicatesEbookAndAudiobookEditions is the regression
// test for issue #442. When OpenLibrary returns two Work records for the same
// title — one with media_type=ebook and one with media_type=audiobook —
// FetchAuthorBooks must create exactly one book row and upgrade its media_type
// to "both" rather than creating a second book entry.
func TestFetchAuthorBooks_DeduplicatesEbookAndAudiobookEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL3101279A", Name: "Matt Dinniman", SortName: "Dinniman, Matt",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Simulate OL returning two Work records for the same title: one ebook Work
	// and one audiobook Work (different foreign IDs, same normalized title).
	stub := &stubMetaProvider{
		works: []models.Book{
			{
				ForeignID: "OL1001W", Title: "Dungeon Crawler Carl",
				SortTitle: "dungeon crawler carl", Language: "eng",
				MediaType: models.MediaTypeEbook,
				Status:    models.BookStatusWanted, Genres: []string{},
				MetadataProvider: "openlibrary",
			},
			{
				ForeignID: "OL1002W", Title: "Dungeon Crawler Carl",
				SortTitle: "dungeon crawler carl", Language: "eng",
				MediaType: models.MediaTypeAudiobook,
				Status:    models.BookStatusWanted, Genres: []string{},
				MetadataProvider: "openlibrary",
			},
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Must produce exactly one book row.
	if len(books) != 1 {
		t.Fatalf("expected 1 book after dedup, got %d: %v", len(books), func() []string {
			var titles []string
			for _, b := range books {
				titles = append(titles, b.Title+" ("+b.MediaType+")")
			}
			return titles
		}())
	}
	// The single row must be upgraded to dual-format.
	if books[0].MediaType != models.MediaTypeBoth {
		t.Errorf("expected media_type=%q after ebook+audiobook merge, got %q",
			models.MediaTypeBoth, books[0].MediaType)
	}
}

// TestFetchAuthorBooks_DeduplicatesEbookAndAudiobookEditions_Resync checks
// that a second sync (re-sync) of the same author does not create duplicate
// entries when the DB already contains a dual-format row created by the first
// sync. This is the re-sync arm of issue #442.
func TestFetchAuthorBooks_DeduplicatesEbookAndAudiobookEditions_Resync(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL3101279A", Name: "Matt Dinniman", SortName: "Dinniman, Matt",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the DB with the ebook work (simulates a prior sync that only
	// saw the ebook Work from OL before the audiobook Work existed).
	existing := &models.Book{
		ForeignID: "OL1001W", AuthorID: author.ID,
		Title: "Dungeon Crawler Carl", SortTitle: "dungeon crawler carl",
		Language: "eng", MediaType: models.MediaTypeEbook,
		Status: models.BookStatusWanted, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// On re-sync, OL now returns both Work records (ebook and audiobook).
	stub := &stubMetaProvider{
		works: []models.Book{
			{
				ForeignID: "OL1001W", Title: "Dungeon Crawler Carl",
				SortTitle: "dungeon crawler carl", Language: "eng",
				MediaType: models.MediaTypeEbook,
				Status:    models.BookStatusWanted, Genres: []string{},
				MetadataProvider: "openlibrary",
			},
			{
				ForeignID: "OL1002W", Title: "Dungeon Crawler Carl",
				SortTitle: "dungeon crawler carl", Language: "eng",
				MediaType: models.MediaTypeAudiobook,
				Status:    models.BookStatusWanted, Genres: []string{},
				MetadataProvider: "openlibrary",
			},
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("re-sync: expected 1 book, got %d", len(books))
	}
	if books[0].MediaType != models.MediaTypeBoth {
		t.Errorf("re-sync: expected media_type=%q after audiobook Work discovered, got %q",
			models.MediaTypeBoth, books[0].MediaType)
	}
}

// --- Issue #667 regression tests --------------------------------------------

// TestCleanupOrphanIfNoBooks_DeletesAuthorWithZeroBooks is the unit-level
// guarantee that the orphan-cleanup helper removes a just-created author
// who has no books — the failure mode reported in issue #667 bug 3.
func TestCleanupOrphanIfNoBooks_DeletesAuthorWithZeroBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "dnb:author:should-be-deleted",
		Name:             "Should Be Deleted",
		SortName:         "Deleted, Should Be",
		MetadataProvider: "dnb",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	bookCreated := false
	h.cleanupOrphanIfNoBooks(author, &bookCreated)

	if got, _ := authorRepo.GetByID(ctx, author.ID); got != nil {
		t.Fatalf("expected author deleted, still present: %+v", got)
	}
}

// TestCleanupOrphanIfNoBooks_KeepsAuthorWithBooks is the safety guard:
// if the async FetchAuthorBooks goroutine has already raced ahead and
// inserted books for this author (the OL/Hardcover happy path) we MUST
// NOT delete — the user still gets value from those books even though
// the specific add-book request failed.
func TestCleanupOrphanIfNoBooks_KeepsAuthorWithBooks(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL999A", Name: "Has Books", SortName: "Books, Has",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL999W", AuthorID: author.ID, Title: "Some Other Book",
		SortTitle: "some other book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	bookCreated := false
	h.cleanupOrphanIfNoBooks(author, &bookCreated)

	if got, _ := authorRepo.GetByID(ctx, author.ID); got == nil {
		t.Fatal("author with existing books was wrongly deleted")
	}
}

// TestCleanupOrphanIfNoBooks_NoopWhenBookCreated covers the happy path:
// the AddBook flow succeeded, bookCreated=true, the defer must do
// nothing. Belt-and-braces — even though the defer is only registered
// when authorWasJustCreated is true, the bookCreated flag is the final
// gate that the deletion is unwanted.
func TestCleanupOrphanIfNoBooks_NoopWhenBookCreated(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL777A", Name: "Survivor", SortName: "Survivor",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	bookCreated := true
	h.cleanupOrphanIfNoBooks(author, &bookCreated)

	if got, _ := authorRepo.GetByID(ctx, author.ID); got == nil {
		t.Fatal("author wrongly deleted despite bookCreated=true")
	}
}

func TestAddBook_DoesNotResolveOtherUsersAlternateAuthorIdentifier(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	aliceAuthor := &models.Author{
		ForeignID:        "OL-ALICE",
		Name:             "Alice Author",
		SortName:         "Author, Alice",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, aliceAuthor.ID, "hc:alice-author"); err != nil {
		t.Fatalf("seed alice identifier: %v", err)
	}
	provider := &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover"},
		authors: map[string]*models.Author{
			"hc:alice-author": {
				ForeignID:        "hc:alice-author",
				Name:             "Alice Author",
				SortName:         "Author, Alice",
				MetadataProvider: "hardcover",
			},
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "hc:book-one",
		"foreignAuthorId": "hc:alice-author",
		"authorName":      "Alice Author",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).
		WithContext(auth.WithUserID(context.Background(), bob.ID))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := authorRepo.GetByForeignID(ctx, "hc:alice-author"); err != nil || got != nil {
		t.Fatalf("hidden identifier created bob-visible author: %+v err=%v", got, err)
	}
	if got, err := authorRepo.GetByID(ctx, aliceAuthor.ID); err != nil || got == nil || got.ForeignID != "OL-ALICE" {
		t.Fatalf("alice author after AddBook = %+v err=%v", got, err)
	}
}

func TestAddBook_DirectInsertCoversAlternateAuthorIdentifier(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	existing := &models.Author{
		ForeignID:        "OL-ALICE",
		Name:             "Alice Author",
		SortName:         "Author, Alice",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, existing.ID, "hc:alice-author"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	primary := &models.Book{
		ForeignID:        "hc:book-one",
		Title:            "Book One",
		SortTitle:        "Book One",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	provider := &stubMetaProvider{
		name:  "hardcover",
		works: nil,
		getBookByID: map[string]*models.Book{
			"hc:book-one": primary,
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "hc:book-one",
		"foreignAuthorId": "hc:alice-author",
		"authorName":      "Alice Author",
	})
	parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(parent)
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(ctx, "hc:book-one")
	if err != nil || got == nil {
		t.Fatalf("book after AddBook = %+v err=%v, want persisted", got, err)
	}
	if got.AuthorID != existing.ID {
		t.Fatalf("book author_id = %d, want existing author %d", got.AuthorID, existing.ID)
	}
	if !got.Monitored {
		t.Fatalf("book should be monitored after AddBook success")
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want existing author reused", len(authors))
	}
}

// TestAddBook_InvalidMediaTypeRejected pins the request validation for the
// optional mediaType field (#1397): anything outside ebook/audiobook/both
// fails fast with a 400 before any author/book work happens.
func TestAddBook_InvalidMediaTypeRejected(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(&stubMetaProvider{name: "openlibrary"}), nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OL-BOOK",
		"foreignAuthorId": "OL-AUTHOR",
		"mediaType":       "paperback",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid mediaType, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAddBook_MediaTypeOverridesProvider verifies the explicit request
// choice (#1397) wins over the provider's media type on the direct-insert
// path and is persisted on the final monitored book.
func TestAddBook_MediaTypeOverridesProvider(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	existing := &models.Author{
		ForeignID:        "OL-ALICE",
		Name:             "Alice Author",
		SortName:         "Author, Alice",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, existing.ID, "hc:alice-author"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	primary := &models.Book{
		ForeignID:        "hc:book-one",
		Title:            "Book One",
		SortTitle:        "Book One",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MediaType:        models.MediaTypeEbook, // provider says ebook; request forces audiobook
		MetadataProvider: "hardcover",
	}
	provider := &stubMetaProvider{
		name:  "hardcover",
		works: nil,
		getBookByID: map[string]*models.Book{
			"hc:book-one": primary,
		},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "hc:book-one",
		"foreignAuthorId": "hc:alice-author",
		"authorName":      "Alice Author",
		"mediaType":       models.MediaTypeAudiobook,
	})
	parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(parent)
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(ctx, "hc:book-one")
	if err != nil || got == nil {
		t.Fatalf("book after AddBook = %+v err=%v, want persisted", got, err)
	}
	if got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("book mediaType = %q, want %q (request override)", got.MediaType, models.MediaTypeAudiobook)
	}
	if !got.Monitored {
		t.Fatalf("book should be monitored after AddBook success")
	}
}

// TestAddBook_OrphanAuthorDeletedOnTimeout is the end-to-end guarantee
// for issue #667. With a DNB-shaped synthetic author ID, the
// (legacy-flow) async fetch returns zero books deterministically and the
// poll loop times out. Before this fix the author row stayed in the DB
// forever; now the deferred cleanup removes it. We short-circuit the
// poll with a fast ctx cancel rather than waiting the full 15s.
func TestAddBook_OrphanAuthorDeletedOnTimeout(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// stubMetaProvider.GetAuthorWorks returns empty by default, which
	// exactly mirrors what DNB's GetAuthorWorks now does for synthetic
	// IDs. The poll loop will never see a book.
	stub := &stubMetaProvider{works: nil}
	agg := metadata.NewAggregator(stub)

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "dnb:doesnotexist",
		"foreignAuthorId": "dnb:author:phantom-author",
		"authorName":      "Phantom Author",
	})

	// Short-deadline context so the poll loop's ctx.Done() branch fires
	// in ~50ms instead of the hardcoded 15s. The defer should still
	// run after the 504 response.
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(parent)
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	// Either 504 (ctx cancel) or 404 (poll deadline) is acceptable —
	// both must trigger the orphan cleanup.
	if rec.Code != http.StatusGatewayTimeout && rec.Code != http.StatusNotFound {
		t.Fatalf("expected 504 or 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Give any in-flight async goroutine a moment to drain before
	// checking; the orphan-cleanup defer ran synchronously on
	// AddBook return so the author should already be gone.
	time.Sleep(50 * time.Millisecond)

	got, _ := authorRepo.GetByForeignID(context.Background(), "dnb:author:phantom-author")
	if got != nil {
		t.Fatalf("orphan author was not cleaned up after timeout: %+v", got)
	}
}

// TestAddBook_DNBDirectInsertSucceeds is the end-to-end guarantee that
// adding a DNB-prefixed book no longer hangs on the poll loop. The
// async FetchAuthorBooks goroutine returns zero books for DNB synthetic
// IDs (correctly, since DNB SRU has no author→works endpoint); the new
// direct-insert path in AddBook fetches the requested record and
// persists it before the poll loop starts. Without this fix, the poll
// times out at 15 s and the request returns 404 — the exact failure
// zippoking saw on bindery-dev with ISBN 978-3-8449-3577-6.
func TestAddBook_DNBDirectInsertSucceeds(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// Mirrors what DNB.GetBook returns for ISBN 978-3-8449-3577-6:
	// a *models.Book carrying its own embedded synthetic author.
	primary := &models.Book{
		ForeignID:        "dnb:1305873874",
		Title:            "Der war's",
		SortTitle:        "Der war's",
		Language:         "ger",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "dnb",
		Monitored:        true,
	}
	stub := &stubMetaProvider{
		name:  "dnb", // aggregator routes "dnb:" prefix to a provider named "dnb"
		works: nil,   // GetAuthorWorks empty — matches real DNB short-circuit
		getBookByID: map[string]*models.Book{
			"dnb:1305873874": primary,
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "dnb:1305873874",
		"foreignAuthorId": "dnb:gnd:123120802",
		"authorName":      "Juli Zeh",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Book persisted with author linkage and marked monitored.
	got, err := bookRepo.GetByForeignID(context.Background(), "dnb:1305873874")
	if err != nil || got == nil {
		t.Fatalf("book not persisted: err=%v got=%v", err, got)
	}
	if !got.Monitored {
		t.Errorf("book should be Monitored=true after AddBook success")
	}
	if got.AuthorID == 0 {
		t.Errorf("book AuthorID should be linked to the author row")
	}

	// Author persisted with the synthetic foreign ID.
	auth, err := authorRepo.GetByForeignID(context.Background(), "dnb:gnd:123120802")
	if err != nil || auth == nil {
		t.Fatalf("author not persisted: err=%v auth=%v", err, auth)
	}
}

// TestAddBook_NameOnlyResolvesToExistingLibraryAuthor covers the Google-Books
// add path: a result carrying an author NAME but no author ID and no ISBN (so
// resolveAuthorForBook returns nil) must still be addable. It resolves by name
// onto the user's existing author (no duplicate), direct-inserts the chosen
// edition, and stamps a media type (GB leaves it empty, which would otherwise
// mis-route the indexer search).
func TestAddBook_NameOnlyResolvesToExistingLibraryAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	// Existing library author, stored INVERTED to prove inversion-aware matching.
	existing := &models.Author{
		ForeignID: "OL564887A", Name: "Brooks, Arthur C.", SortName: "Brooks, Arthur C.",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// A Google Books result: author name, no author ID, no ISBN editions, and no
	// media type set.
	gbBook := &models.Book{
		ForeignID: "gb:cMGGEQAAQBAJ", Title: "The Meaning of Your Life",
		SortTitle: "meaning of your life", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "googlebooks",
	}
	stub := &stubMetaProvider{
		name:        "googlebooks", // aggregator routes "gb:" to this provider
		getBookByID: map[string]*models.Book{"gb:cMGGEQAAQBAJ": gbBook},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId": "gb:cMGGEQAAQBAJ",
		"authorName":    "Arthur C. Brooks", // natural form; matches the inverted record
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := bookRepo.GetByForeignID(ctx, "gb:cMGGEQAAQBAJ")
	if err != nil || got == nil {
		t.Fatalf("book not persisted: err=%v got=%v", err, got)
	}
	if got.AuthorID != existing.ID {
		t.Errorf("book should link to the existing author %d, got %d", existing.ID, got.AuthorID)
	}
	if got.MediaType == "" {
		t.Error("media type should be stamped (not empty) so the indexer search routes correctly")
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Errorf("expected no duplicate author (1 total), got %d", len(authors))
	}
}

// TestAddBook_DirectInsertCoversSlowAsyncSync is the #804 regression test.
//
// Scenario from the bug report: a user adds a single book by a prolific
// author (Stephenie Meyer, 175 works). OpenLibrary's GetAuthorWorks takes
// longer than the 15s poll budget. The poll loop times out → 404 → the
// orphan-cleanup defer deletes the just-created author row → the still-
// running async goroutine then logs a FK-constraint failure for every
// book it tries to insert against the now-deleted author_id.
//
// The fix lifts the existing DNB direct-insert path so it also fires when
// the author was just created, regardless of provider. We simulate the
// slow OL by having GetAuthorWorks block past the test's wall-clock
// budget; the test asserts that AddBook still returns 201 because the
// direct-insert ran synchronously before the poll loop.
func TestAddBook_DirectInsertCoversSlowAsyncSync(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// Mirrors OpenLibrary's response for the user's requested book.
	primary := &models.Book{
		ForeignID:        "OL12345W",
		Title:            "Twilight",
		SortTitle:        "Twilight",
		Language:         "eng",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	// works empty: simulates an OL author whose works list is still being
	// fetched when the poll loop starts. Without the direct-insert path the
	// poll would time out (no books appear) and the cleanup defer would
	// delete the just-created author.
	stub := &stubMetaProvider{
		name:  "openlibrary",
		works: nil,
		getBookByID: map[string]*models.Book{
			"OL12345W": primary,
		},
	}
	agg := metadata.NewAggregator(stub)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OL12345W",
		"foreignAuthorId": "OL1391085A",
		"authorName":      "Stephenie Meyer",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	// 201 (not 404) proves the direct-insert ran before the poll loop —
	// without the fix, this fails with "book not found after author sync".
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := bookRepo.GetByForeignID(context.Background(), "OL12345W")
	if err != nil || got == nil {
		t.Fatalf("book not persisted: err=%v got=%v", err, got)
	}
	if !got.Monitored {
		t.Errorf("book should be Monitored=true after AddBook success")
	}
	if got.AuthorID == 0 {
		t.Errorf("book AuthorID should be linked to the author row")
	}

	// Author kept (not orphan-cleaned): the direct-insert persisted a book
	// against author.ID, so cleanupOrphanIfNoBooks sees len(books)>0 and
	// skips deletion. Before the fix, the goroutine's inserts all FK-failed
	// because the author row had already been removed.
	auth, err := authorRepo.GetByForeignID(context.Background(), "OL1391085A")
	if err != nil || auth == nil {
		t.Fatalf("author was orphan-cleaned despite direct-insert: err=%v auth=%v", err, auth)
	}
}

func TestAddBook_DirectInsertHydratesMatchedHardcoverEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	enableHardcoverFeatureForTest(t, ctx, settingsRepo)

	primaryBook := &models.Book{
		ForeignID:          "OL-TWILIGHT-W",
		Title:              "Twilight",
		SortTitle:          "Twilight",
		Language:           "eng",
		Status:             models.BookStatusWanted,
		Genres:             []string{},
		MetadataProvider:   "openlibrary",
		MediaType:          models.MediaTypeAudiobook,
		HardcoverForeignID: "hc:twilight",
	}
	primary := &stubMetaProvider{
		name:  "openlibrary",
		works: nil,
		getBookByID: map[string]*models.Book{
			"OL-TWILIGHT-W": primaryBook,
		},
	}
	audioASIN := "B000TWILIT"
	hardcover := &stubMetaProvider{
		name: "hardcover",
		editionsByBook: map[string][]models.Edition{
			"hc:twilight": {{
				ForeignID: "hc:twilight-audio",
				Title:     "Twilight",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	agg := metadata.NewAggregator(primary, hardcover).WithAudnexClient(nil)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).
		WithHardcoverFeatureSettings(settingsRepo, true).
		WithEditionHydration(editionRepo)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "OL-TWILIGHT-W",
		"foreignAuthorId": "OL1391085A",
		"authorName":      "Stephenie Meyer",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(hardcover.editionCalls) != 1 || hardcover.editionCalls[0] != "hc:twilight" {
		t.Fatalf("hardcover edition calls = %+v, want [hc:twilight]", hardcover.editionCalls)
	}
	got, err := bookRepo.GetByForeignID(ctx, "OL-TWILIGHT-W")
	if err != nil || got == nil {
		t.Fatalf("book not persisted: err=%v got=%v", err, got)
	}
	if got.ASIN != audioASIN {
		t.Fatalf("book ASIN = %q, want %q", got.ASIN, audioASIN)
	}
	editions, err := editionRepo.ListByBook(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:twilight-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

// TestCanUpgradeToBoth validates the helper that decides whether two
// complementary media types should be merged into a dual-format row.
func TestCanUpgradeToBoth(t *testing.T) {
	cases := []struct {
		existing, incoming string
		want               bool
	}{
		{models.MediaTypeEbook, models.MediaTypeAudiobook, true},
		{models.MediaTypeAudiobook, models.MediaTypeEbook, true},
		{models.MediaTypeEbook, models.MediaTypeEbook, false},
		{models.MediaTypeAudiobook, models.MediaTypeAudiobook, false},
		{models.MediaTypeBoth, models.MediaTypeEbook, false},
		{models.MediaTypeBoth, models.MediaTypeAudiobook, false},
		{models.MediaTypeEbook, models.MediaTypeBoth, false},
		{"", models.MediaTypeEbook, false},
	}
	for _, c := range cases {
		got := canUpgradeToBoth(c.existing, c.incoming)
		if got != c.want {
			t.Errorf("canUpgradeToBoth(%q, %q) = %v, want %v",
				c.existing, c.incoming, got, c.want)
		}
	}
}

// TestDeleteAuthor_PathContainment_RejectsOutsideRoots is the author-side
// Wave 1 / Bundle B guard. When the delete-files sweep walks a book whose
// file_path is outside every configured root, the on-disk file is left
// alone but the author + book rows are still removed via the FK cascade.
func TestDeleteAuthor_PathContainment_RejectsOutsideRoots(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL920A", Name: "Outside Author", SortName: "Author, Outside",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	libA := t.TempDir()
	// Outside-roots file: this must survive the delete.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.epub")
	if err := os.WriteFile(outsidePath, []byte("untouchable"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Inside-roots file: this should still be deleted.
	insidePath := filepath.Join(libA, "inside.epub")
	if err := os.WriteFile(insidePath, []byte("legit"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, b := range []*models.Book{
		{ForeignID: "OL920W1", AuthorID: author.ID, Title: "Outside", SortTitle: "outside", FilePath: outsidePath, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL920W2", AuthorID: author.ID, Title: "Inside", SortTitle: "inside", FilePath: insidePath, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := bookRepo.SetFilePath(ctx, b.ID, b.FilePath); err != nil {
			t.Fatal(err)
		}
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil).
		WithRoots(NewLibraryRoots(staticRootLister{paths: []string{libA}}))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10)+"?deleteFiles=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	// Outside-roots file: must survive.
	if _, err := os.Stat(outsidePath); err != nil {
		t.Errorf("file outside library roots must NOT be deleted: stat err=%v", err)
	}
	// Inside-roots file: removed normally.
	if _, err := os.Stat(insidePath); !os.IsNotExist(err) {
		t.Errorf("file inside library root SHOULD be deleted, stat err=%v", err)
	}
	// Author row gone.
	if got, _ := authorRepo.GetByID(ctx, author.ID); got != nil {
		t.Errorf("author row should be deleted, got id=%d", got.ID)
	}
}

// authorListFixture spins up the minimum wiring required by AuthorHandler.List:
// a memory DB, AuthorRepo, and a handler that only needs the repo + profile
// repo to satisfy the constructor.
func authorListFixture(t *testing.T) (*AuthorHandler, *db.AuthorRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	profiles := db.NewMetadataProfileRepo(database)
	h := NewAuthorHandler(authors, nil, books, nil, nil, nil, profiles, nil)
	return h, authors, context.Background()
}

// seedAuthorsForPagination seeds n authors with deterministic sort_names
// "Sort 001", "Sort 002" so the sort_name ORDER BY in ListPage is
// predictable.
func seedAuthorsForPagination(t *testing.T, authors *db.AuthorRepo, ctx context.Context, n int) []string {
	t.Helper()
	sorts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		s := fmt.Sprintf("Sort %03d", i)
		a := &models.Author{
			ForeignID: fmt.Sprintf("OL-PAGE-%03d", i), Name: s, SortName: s,
			MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		sorts = append(sorts, s)
	}
	return sorts
}

func TestAuthorList_Paginates(t *testing.T) {
	h, authors, ctx := authorListFixture(t)
	sorts := seedAuthorsForPagination(t, authors, ctx, 10)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/author?limit=3&offset=0", nil))
	var first authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.Total != 10 || first.Limit != 3 || first.Offset != 0 || len(first.Items) != 3 {
		t.Errorf("first page = %+v, want total=10 limit=3 offset=0 len=3", first)
	}
	for i, a := range first.Items {
		if a.SortName != sorts[i] {
			t.Errorf("first page item %d sort_name = %q, want %q", i, a.SortName, sorts[i])
		}
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/author?limit=3&offset=9", nil))
	var tail authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tail); err != nil {
		t.Fatalf("decode tail: %v", err)
	}
	if tail.Total != 10 || len(tail.Items) != 1 || tail.Items[0].SortName != sorts[9] {
		t.Errorf("tail page = %+v, want one item %q", tail, sorts[9])
	}
}

func TestAuthorList_DefaultsAndCaps(t *testing.T) {
	h, authors, ctx := authorListFixture(t)
	seedAuthorsForPagination(t, authors, ctx, 3)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/author", nil))
	var defaults authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &defaults); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if defaults.Limit != authorListDefaultLimit {
		t.Errorf("expected default limit %d, got %d", authorListDefaultLimit, defaults.Limit)
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/author?limit=10000", nil))
	var clamped authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &clamped); err != nil {
		t.Fatalf("decode clamped: %v", err)
	}
	if clamped.Limit != authorListMaxLimit {
		t.Errorf("expected clamped limit %d, got %d", authorListMaxLimit, clamped.Limit)
	}
}

func TestAuthorList_OrderStable(t *testing.T) {
	h, authors, ctx := authorListFixture(t)
	seedAuthorsForPagination(t, authors, ctx, 5)
	collect := func() []string {
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/author?limit=5&offset=0", nil))
		var page authorListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]string, len(page.Items))
		for i, a := range page.Items {
			out[i] = a.SortName
		}
		return out
	}
	first := collect()
	second := collect()
	if len(first) != 5 || len(second) != 5 {
		t.Fatalf("expected 5+5 items, got %d/%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("order changed at %d: %q vs %q", i, first[i], second[i])
		}
	}
}

// TestAuthorHandler_GoroutineCancelsOnLifetimeCtxCancel is the #846 guard
// for AuthorHandler: the FetchAuthorBooks / orphan-cleanup / SearchOnAdd
// goroutines must derive from the process-lifecycle ctx, not
// context.Background(). The simplest expression of that contract is that
// bgCtx() returns the configured lifetime ctx and observes its cancellation
// from a spawned goroutine. AddBook's full plumbing is exercised elsewhere;
// this test isolates the ctx wiring without dragging in the metadata
// aggregator. The fallback case (no WithLifetimeCtx) is also asserted so
// the legacy tests' construction sites keep their previous semantics.
func TestAuthorHandler_GoroutineCancelsOnLifetimeCtxCancel(t *testing.T) {
	// Default: no lifetime ctx wired → bgCtx must be Background() and
	// never cancel.
	h := &AuthorHandler{}
	bg := h.bgCtx()
	if bg == nil {
		t.Fatal("bgCtx returned nil")
	}
	select {
	case <-bg.Done():
		t.Fatal("default bgCtx must not be cancelled")
	default:
	}

	// With WithLifetimeCtx wired, the goroutine started below must
	// observe the cancellation.
	lifetimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h = (&AuthorHandler{}).WithLifetimeCtx(lifetimeCtx)

	observed := make(chan struct{})
	go func() {
		<-h.bgCtx().Done()
		close(observed)
	}()

	cancel()
	select {
	case <-observed:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not observe lifetime ctx cancellation")
	}
}

// --- reparenting mis-attached books during author sync (#1405) ---

// reparentFixture creates the author being synced plus an existing book row
// attached to a different owner, then runs FetchAuthorBooks with a stub work
// matching that book's foreign id. Returns the synced author and the book row
// re-read after the sync.
func reparentFixture(t *testing.T, ownerForeignID string, credited []string) (*models.Author, *models.Book) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	synced := &models.Author{
		ForeignID: "OL500A", Name: "Real Author", SortName: "Author, Real",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, synced); err != nil {
		t.Fatal(err)
	}

	// The current owner of the book row. (A truly orphaned owner row can't be
	// fabricated here: books.author_id is FK-enforced in-memory — the
	// owner==nil branch in reparentMisattachedBook is defensive cover for
	// legacy databases that predate FK enforcement.)
	owner := &models.Author{
		ForeignID: ownerForeignID, Name: "Other Author", SortName: "Author, Other",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ownerID := owner.ID

	book := &models.Book{
		ForeignID: "OL501W", AuthorID: ownerID, Title: "Elantris", SortTitle: "elantris",
		Language: "eng", Status: models.BookStatusWanted, Monitored: true,
		Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{{
			ForeignID: "OL501W", Title: "Elantris", SortTitle: "elantris",
			Language: "eng", Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", CreditedAuthorForeignIDs: credited,
		}},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, &searcherSpy{})
	h.FetchAuthorBooks(synced, false, "")

	got, err := bookRepo.GetByForeignID(ctx, "OL501W")
	if err != nil || got == nil {
		t.Fatalf("re-read book: %v (nil=%v)", err, got == nil)
	}
	return synced, got
}

// A row sitting under an author who is NOT credited on the work (duplicate OL
// author record, wrong link) is re-linked to the author being synced, making
// it visible on that author's page again.
func TestFetchAuthorBooks_ReparentsMisattachedBook(t *testing.T) {
	synced, got := reparentFixture(t, "OL777A", []string{"OL500A"})
	if got.AuthorID != synced.ID {
		t.Errorf("book should be re-linked to synced author %d, still under %d", synced.ID, got.AuthorID)
	}
}

// A co-authored work (current owner IS credited) must stay put — otherwise it
// would ping-pong between its authors on alternating syncs.
func TestFetchAuthorBooks_KeepsCoauthoredBook(t *testing.T) {
	synced, got := reparentFixture(t, "OL777A", []string{"OL500A", "OL777A"})
	if got.AuthorID == synced.ID {
		t.Error("co-authored book must not be stolen from its credited owner")
	}
}

// Without a credited-author list the attachment cannot be verified, so the
// row stays where it is (conservative default).
func TestFetchAuthorBooks_KeepsBookWithUnknownAuthorship(t *testing.T) {
	synced, got := reparentFixture(t, "OL777A", nil)
	if got.AuthorID == synced.ID {
		t.Error("book with unknown authorship must not be moved")
	}
}

// --- MonitorNewItems: refresh discovery vs initial sync (#1348) ---

// monitorNewItemsFixture runs a catalogue sync for an author with the given
// MonitorNewItems policy and returns the created book. initial selects the
// add-flow entry point (FetchAuthorBooks) vs the refresh/discovery one
// (RefreshAuthorBooks).
func monitorNewItemsFixture(t *testing.T, monitorNewItems string, initial bool) *models.Book {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID: "OL800A", Name: "Prolific Author", SortName: "Author, Prolific",
		MetadataProvider: "openlibrary", Monitored: true,
		MonitorMode: models.AuthorMonitorModeAll, MonitorNewItems: monitorNewItems,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	stub := &stubMetaProvider{
		works: []models.Book{{
			ForeignID: "OL801W", Title: "Back Catalogue Book", SortTitle: "back catalogue book",
			Language: "eng", Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary",
		}},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, &searcherSpy{})
	if initial {
		h.FetchAuthorBooks(author, false, "")
	} else {
		h.RefreshAuthorBooks(author, false, "")
	}

	got, err := bookRepo.GetByForeignID(ctx, "OL801W")
	if err != nil || got == nil {
		t.Fatalf("re-read book: %v (nil=%v)", err, got == nil)
	}
	return got
}

// With MonitorNewItems=none, a refresh discovering new works creates them
// unmonitored — the #1348 trap (refresh mass-monitoring a back-catalogue
// under monitor-mode 'all') is defused.
func TestRefreshAuthorBooks_MonitorNewItemsNone_CreatesUnmonitored(t *testing.T) {
	got := monitorNewItemsFixture(t, models.AuthorMonitorNewItemsNone, false)
	if got.Monitored {
		t.Error("refresh-discovered book must be unmonitored under monitorNewItems=none")
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("book status should still be wanted (it just isn't monitored), got %q", got.Status)
	}
}

// The default policy keeps the historical behaviour: refresh-discovered works
// follow the author's monitor mode.
func TestRefreshAuthorBooks_MonitorNewItemsAll_FollowsMode(t *testing.T) {
	got := monitorNewItemsFixture(t, models.AuthorMonitorNewItemsAll, false)
	if !got.Monitored {
		t.Error("refresh-discovered book should follow monitor-mode 'all' under monitorNewItems=all")
	}
}

// The initial sync (add flow, migrations) is governed by MonitorMode alone —
// MonitorNewItems only constrains later discovery.
func TestFetchAuthorBooks_InitialSyncIgnoresMonitorNewItems(t *testing.T) {
	got := monitorNewItemsFixture(t, models.AuthorMonitorNewItemsNone, true)
	if !got.Monitored {
		t.Error("initial sync must honour monitor-mode 'all' even with monitorNewItems=none")
	}
}
