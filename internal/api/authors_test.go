package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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
}

func (p *stubMetaProvider) Name() string { return "stub" }
func (p *stubMetaProvider) SearchAuthors(_ context.Context, _ string) ([]models.Author, error) {
	return nil, nil
}
func (p *stubMetaProvider) SearchBooks(_ context.Context, _ string) ([]models.Book, error) {
	return nil, nil
}
func (p *stubMetaProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return nil, nil
}
func (p *stubMetaProvider) GetBook(_ context.Context, _ string) (*models.Book, error) {
	return nil, nil
}
func (p *stubMetaProvider) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return nil, nil
}
func (p *stubMetaProvider) GetBookByISBN(_ context.Context, _ string) (*models.Book, error) {
	return nil, nil
}

// GetAuthorWorks satisfies the worksProvider sub-interface used by Aggregator.
func (p *stubMetaProvider) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	return p.works, nil
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

func TestFetchAuthorBooks_AutoSearchUsesBoundedConcurrency(t *testing.T) {
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
}

func (p *searchableAuthorProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	return p.searchAuthorsByQuery[query], nil
}

func (p *searchableAuthorProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
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
	handler *AuthorHandler
}

func newRelinkUpstreamFixture(t *testing.T, provider metadata.Provider) *relinkUpstreamFixture {
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
		handler: NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil),
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
	if len(aliases) != 1 || aliases[0].Name != "J. R. R. Tolkien" {
		t.Fatalf("aliases = %+v, want original placeholder spelling", aliases)
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
