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

// stubLibraryFinder is a mock LibraryFinder that returns a fixed path for
// a specific title and "" for everything else.
type stubLibraryFinder struct {
	ownedTitle string
	ownedPath  string
}

func (f *stubLibraryFinder) FindExisting(_ context.Context, title, _ string) string {
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
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal("response body not valid JSON:", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field in response body")
	}
}
