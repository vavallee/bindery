package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func testDB(t *testing.T) *context.Context {
	t.Helper()
	ctx := context.Background()
	return &ctx
}

func TestPreflightCreatesMissingParent(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "nested", "sub", "bindery.db")
	if err := preflight(dbPath); err != nil {
		t.Fatalf("preflight should create missing parents: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	}
}

func TestPreflightReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX + non-root (root ignores directory mode bits)")
	}
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "readonly")
	if err := os.Mkdir(parent, 0o555); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Restore perms so t.TempDir()'s cleanup can delete the tree.
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := preflight(filepath.Join(parent, "bindery.db"))
	if err == nil {
		t.Fatal("expected preflight to fail on read-only parent")
	}
	// The message must name the path and mention writability; that's the
	// whole point of the check.
	if !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "writable") {
		t.Errorf("error should mention path and writability, got: %v", err)
	}
}

func TestOpenMemory(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	tables := []string{"authors", "books", "series", "editions", "indexers",
		"download_clients", "downloads", "root_folders", "quality_profiles",
		"settings", "history", "schema_migrations"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s does not exist: %v", table, err)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}

	// Running migrate again should not fail
	err = migrate(db)
	if err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}
	db.Close()
}

func TestDefaultQualityProfiles(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM quality_profiles").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3 default quality profiles, got %d", count)
	}
}

func TestAuthorCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewAuthorRepo(database)

	// Create
	author := &models.Author{
		ForeignID:        "OL123A",
		Name:             "Test Author",
		SortName:         "Author, Test",
		Description:      "A test author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	err = repo.Create(ctx, author)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if author.ID == 0 {
		t.Error("expected non-zero ID after create")
	}

	// Get by ID
	got, err := repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got == nil {
		t.Fatal("expected author, got nil")
	}
	if got.Name != "Test Author" {
		t.Errorf("expected name 'Test Author', got '%s'", got.Name)
	}
	if !got.Monitored {
		t.Error("expected monitored=true")
	}

	// Get by foreign ID
	got, err = repo.GetByForeignID(ctx, "OL123A")
	if err != nil {
		t.Fatalf("get by foreign id: %v", err)
	}
	if got == nil {
		t.Fatal("expected author by foreign id")
	}

	// List
	authors, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(authors) != 1 {
		t.Errorf("expected 1 author, got %d", len(authors))
	}

	// Update
	author.Name = "Updated Author"
	author.Monitored = false
	err = repo.Update(ctx, author)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = repo.GetByID(ctx, author.ID)
	if got.Name != "Updated Author" {
		t.Errorf("expected updated name, got '%s'", got.Name)
	}

	// Delete
	err = repo.Delete(ctx, author.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.GetByID(ctx, author.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestBookCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	// Create author first
	author := &models.Author{
		ForeignID: "OL100A", Name: "Author One", SortName: "One, Author",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Create book
	book := &models.Book{
		ForeignID:        "OL200W",
		AuthorID:         author.ID,
		Title:            "Test Book",
		SortTitle:        "Test Book",
		Description:      "A great book",
		Genres:           []string{"fiction", "thriller"},
		Status:           models.BookStatusWanted,
		Monitored:        true,
		AnyEditionOK:     true,
		MetadataProvider: "openlibrary",
	}
	err = bookRepo.Create(ctx, book)
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	if book.ID == 0 {
		t.Error("expected non-zero book ID")
	}

	// Get by ID
	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("get book: %v", err)
	}
	if got.Title != "Test Book" {
		t.Errorf("expected title 'Test Book', got '%s'", got.Title)
	}
	if len(got.Genres) != 2 {
		t.Errorf("expected 2 genres, got %d", len(got.Genres))
	}

	// List by author
	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatalf("list by author: %v", err)
	}
	if len(books) != 1 {
		t.Errorf("expected 1 book, got %d", len(books))
	}

	// List by status
	wanted, err := bookRepo.ListByStatus(ctx, models.BookStatusWanted)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(wanted) != 1 {
		t.Errorf("expected 1 wanted book, got %d", len(wanted))
	}

	// Update status
	book.Status = models.BookStatusImported
	err = bookRepo.Update(ctx, book)
	if err != nil {
		t.Fatalf("update book: %v", err)
	}
	wanted, _ = bookRepo.ListByStatus(ctx, models.BookStatusWanted)
	if len(wanted) != 0 {
		t.Errorf("expected 0 wanted after status update, got %d", len(wanted))
	}

	// Delete
	err = bookRepo.Delete(ctx, book.ID)
	if err != nil {
		t.Fatalf("delete book: %v", err)
	}
}

func TestSettingsCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewSettingsRepo(database)

	// Set
	err = repo.Set(ctx, "test_key", "test_value")
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get
	s, err := repo.Get(ctx, "test_key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s.Value != "test_value" {
		t.Errorf("expected 'test_value', got '%s'", s.Value)
	}

	// Upsert
	err = repo.Set(ctx, "test_key", "updated_value")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s, _ = repo.Get(ctx, "test_key")
	if s.Value != "updated_value" {
		t.Errorf("expected 'updated_value', got '%s'", s.Value)
	}

	// Get non-existent
	s, err = repo.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get nonexistent: %v", err)
	}
	if s != nil {
		t.Error("expected nil for nonexistent key")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 setting, got %d", len(list))
	}
}

func TestIndexerCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewIndexerRepo(database)

	idx := &models.Indexer{
		Name:           "Test Indexer",
		Type:           "newznab",
		URL:            "https://example.com/api",
		APIKey:         "testkey123",
		Categories:     []int{7000, 7020},
		Priority:       25,
		Enabled:        true,
		SupportsSearch: true,
	}
	err = repo.Create(ctx, idx)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if idx.ID == 0 {
		t.Error("expected non-zero ID")
	}

	got, err := repo.GetByID(ctx, idx.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Test Indexer" {
		t.Errorf("expected 'Test Indexer', got '%s'", got.Name)
	}
	if len(got.Categories) != 2 || got.Categories[0] != 7000 {
		t.Errorf("unexpected categories: %v", got.Categories)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 indexer, got %d", len(list))
	}

	err = repo.Delete(ctx, idx.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestCascadeDeleteAuthorBooks(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	author := &models.Author{
		ForeignID: "OL999A", Name: "Cascade Test", SortName: "Test, Cascade",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	authorRepo.Create(ctx, author)

	bookRepo.Create(ctx, &models.Book{
		ForeignID: "OL888W", AuthorID: author.ID, Title: "Book 1", SortTitle: "Book 1",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})
	bookRepo.Create(ctx, &models.Book{
		ForeignID: "OL777W", AuthorID: author.ID, Title: "Book 2", SortTitle: "Book 2",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	})

	books, _ := bookRepo.ListByAuthor(ctx, author.ID)
	if len(books) != 2 {
		t.Fatalf("expected 2 books, got %d", len(books))
	}

	// Delete author should cascade to books
	authorRepo.Delete(ctx, author.ID)

	books, _ = bookRepo.List(ctx)
	if len(books) != 0 {
		t.Errorf("expected 0 books after cascade delete, got %d", len(books))
	}
}
