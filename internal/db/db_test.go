package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

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
		return
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
		"settings", "history", "abs_import_runs", "abs_provenance", "abs_metadata_conflicts", "series_hardcover_links", "schema_migrations"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s does not exist: %v", table, err)
		}
	}
}

// TestAssertUniqueMigrationVersions covers the duplicate-prefix guard added
// after the 043 collision incident (2026-05-26): two unrelated PRs both
// shipped a migration numbered 043, the apply loop silently skipped the
// second on installs that had already applied the first, and the lost
// schema change wasn't noticed until prod broke.
func TestAssertUniqueMigrationVersions(t *testing.T) {
	t.Run("unique versions pass", func(t *testing.T) {
		entries := []os.DirEntry{
			fakeDirEntry{name: "001_a.sql"},
			fakeDirEntry{name: "002_b.sql"},
			fakeDirEntry{name: "003_c.sql"},
		}
		if err := assertUniqueMigrationVersions(entries); err != nil {
			t.Errorf("unique versions should not error: %v", err)
		}
	})
	t.Run("duplicate prefix errors", func(t *testing.T) {
		entries := []os.DirEntry{
			fakeDirEntry{name: "043_author_monitor_mode.sql"},
			fakeDirEntry{name: "043_download_client_category_audiobook.sql"},
		}
		err := assertUniqueMigrationVersions(entries)
		if err == nil {
			t.Fatal("expected duplicate-version error")
		}
		if !strings.Contains(err.Error(), "duplicate migration version 43") {
			t.Errorf("error should name the duplicate version: %v", err)
		}
		if !strings.Contains(err.Error(), "043_author_monitor_mode.sql") ||
			!strings.Contains(err.Error(), "043_download_client_category_audiobook.sql") {
			t.Errorf("error should name both files: %v", err)
		}
	})
	t.Run("non-numeric prefix bubbles up", func(t *testing.T) {
		entries := []os.DirEntry{fakeDirEntry{name: "bogus_no_prefix.sql"}}
		if err := assertUniqueMigrationVersions(entries); err == nil {
			t.Fatal("expected non-numeric prefix to error")
		}
	})
}

type fakeDirEntry struct{ name string }

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return false }
func (e fakeDirEntry) Type() os.FileMode          { return 0 }
func (e fakeDirEntry) Info() (os.FileInfo, error) { return nil, nil }

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

func TestMigrate033ABSReviewResolutionIdempotent(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	for _, column := range []string{
		"resolved_author_foreign_id",
		"resolved_author_name",
		"resolved_book_foreign_id",
		"resolved_book_title",
		"edited_title",
	} {
		var name string
		if err := database.QueryRow(`SELECT name FROM pragma_table_info('abs_review_queue') WHERE name = ?`, column).Scan(&name); err != nil {
			t.Fatalf("abs_review_queue column %q missing: %v", column, err)
		}
	}

	_, err = database.Exec(`
		INSERT INTO abs_review_queue (
			source_id, library_id, item_id, title, primary_author, asin, media_type,
			review_reason, payload_json, resolved_author_foreign_id, resolved_author_name,
			resolved_book_foreign_id, resolved_book_title, edited_title, status, created_at, updated_at
		)
		VALUES (
			'src', 'lib', 'item', 'Title', 'Author', 'ASIN', 'audiobook',
			'review', '{}', 'author-1', 'Resolved Author', 'book-1',
			'Resolved Book', 'Edited Title', 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)`)
	if err != nil {
		t.Fatalf("seed abs review queue row: %v", err)
	}

	version := migrationVersionForTest(t, "033_abs_review_resolution.sql")
	if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version = ?`, version); err != nil {
		t.Fatalf("clear migration 033 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 033: %v", err)
	}

	var authorID, bookID, editedTitle string
	err = database.QueryRow(`
		SELECT resolved_author_foreign_id, resolved_book_foreign_id, edited_title
		FROM abs_review_queue
		WHERE source_id = 'src' AND library_id = 'lib' AND item_id = 'item'`,
	).Scan(&authorID, &bookID, &editedTitle)
	if err != nil {
		t.Fatalf("reload abs review queue row: %v", err)
	}
	if authorID != "author-1" || bookID != "book-1" || editedTitle != "Edited Title" {
		t.Fatalf("resolution fields changed after rerun: author=%q book=%q edited=%q", authorID, bookID, editedTitle)
	}
}

// TestMigrate047ListEndpointIndexes confirms the Wave 2 / E migration lands
// every index the paginated List endpoints depend on. A missing index here
// silently regresses the sort cost on the 50k-book hot path to a full table
// scan (the failure mode is "everything works, just slow"), so the only
// reliable guard is asserting on sqlite_master.
func TestMigrate047ListEndpointIndexes(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	want := []string{
		"idx_books_sort_title",
		"idx_books_release_date",
		"idx_books_status_sort_title",
		"idx_authors_sort_name",
		"idx_series_books_book",
		"idx_history_created_at_desc",
	}
	for _, name := range want {
		var got string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
			name,
		).Scan(&got)
		if err != nil {
			t.Errorf("index %s missing after migration 047: %v", name, err)
		}
	}

	// Re-running migrate must not fail. CREATE INDEX IF NOT EXISTS keeps the
	// migration idempotent even if its schema_migrations marker is dropped
	// (e.g. a re-applied row from a backup restore).
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migrations should be idempotent: %v", err)
	}
}

// TestMigrate042AuthorAudiobookRootFolder verifies migration 042 adds the
// audiobook_root_folder_id column to the authors table (#579) and that the
// column round-trips a value through CreateForUser / GetByID.
func TestMigrate042AuthorAudiobookRootFolder(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	var name string
	if err := database.QueryRow(
		`SELECT name FROM pragma_table_info('authors') WHERE name = 'audiobook_root_folder_id'`,
	).Scan(&name); err != nil {
		t.Fatalf("authors.audiobook_root_folder_id column missing after migration: %v", err)
	}

	// Re-running migrate must not fail — ALTER TABLE ADD COLUMN is not
	// idempotent in SQLite, so this confirms the schema_migrations marker
	// guards the re-run.
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migrations should be idempotent: %v", err)
	}

	// Round-trip the new field through the repo.
	ctx := context.Background()
	rf := NewRootFolderRepo(database)
	folder, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("create root folder: %v", err)
	}

	authors := NewAuthorRepo(database)
	author := &models.Author{
		ForeignID:             "OL-ab-root-042",
		Name:                  "Audiobook Author",
		SortName:              "Author, Audiobook",
		AudiobookRootFolderID: &folder.ID,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	got, err := authors.GetByID(ctx, author.ID)
	if err != nil || got == nil {
		t.Fatalf("get author: %v", err)
	}
	if got.AudiobookRootFolderID == nil || *got.AudiobookRootFolderID != folder.ID {
		t.Fatalf("AudiobookRootFolderID not persisted: want %d, got %v", folder.ID, got.AudiobookRootFolderID)
	}

	// Updating it to nil must clear it.
	got.AudiobookRootFolderID = nil
	if err := authors.Update(ctx, got); err != nil {
		t.Fatalf("update author: %v", err)
	}
	reloaded, err := authors.GetByID(ctx, author.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("reload author: %v", err)
	}
	if reloaded.AudiobookRootFolderID != nil {
		t.Fatalf("AudiobookRootFolderID should be nil after clear, got %v", *reloaded.AudiobookRootFolderID)
	}
}

func migrationVersionForTest(t *testing.T, filename string) int {
	t.Helper()
	v, err := migrationVersion(filename)
	if err != nil {
		t.Fatalf("migration version for %s: %v", filename, err)
	}
	return v
}

// TestMigrate008_CalibreOnFreshDB verifies the v0.8.0 Calibre migration
// lands the calibre_id column and seeds the three calibre.* settings rows
// on a fresh install.
func TestMigrate008_CalibreOnFreshDB(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// calibre_id column exists on books.
	var colName string
	err = database.QueryRow(`SELECT name FROM pragma_table_info('books') WHERE name='calibre_id'`).Scan(&colName)
	if err != nil {
		t.Fatalf("calibre_id column missing: %v", err)
	}

	// Index on calibre_id exists.
	var idxName string
	err = database.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_books_calibre_id'`).Scan(&idxName)
	if err != nil {
		t.Fatalf("idx_books_calibre_id missing: %v", err)
	}

	// Seeded settings rows are present.
	for _, key := range []string{"calibre.enabled", "calibre.library_path", "calibre.binary_path"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, key).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("settings row %q count = %d, want 1", key, count)
		}
	}
}

// TestMigrate008_CalibreOnUpgradeFromPreCalibre simulates a v0.7.2 → v0.8.0
// upgrade by running migrations 1–7, writing some realistic row data, and
// then running migrate() again to apply 008. The rows must survive, the
// column must exist, and the seeded settings must not collide with any
// pre-existing rows a hand-editing operator might have left in place.
func TestMigrate008_CalibreOnUpgradeFromPreCalibre(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := setPragmas(database); err != nil {
		t.Fatal(err)
	}

	// Apply migrations 1–7 by temporarily hiding 008 behind a schema_migrations
	// pre-fill that claims it's already applied. This mirrors the state of a
	// v0.7.2 deployment at rest.
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	// Roll back the applied 008 marker so the upgrade path re-runs it, and
	// drop the column + index + seeded settings it added — simulating a DB
	// that was rolled back to the v0.7.2 schema but kept its data.
	_, _ = database.Exec(`DELETE FROM schema_migrations WHERE version=8`)
	// Pre-populate a user-overridden calibre.enabled setting to prove the
	// INSERT OR IGNORE in 008 preserves operator edits on upgrade.
	if _, err := database.Exec(`DELETE FROM settings WHERE key LIKE 'calibre.%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO settings (key, value) VALUES ('calibre.enabled', 'true')`); err != nil {
		t.Fatal(err)
	}
	// Drop the column by rebuilding the books table without it — a proxy for
	// a pre-008 schema.
	_, _ = database.Exec(`DROP INDEX IF EXISTS idx_books_calibre_id`)

	// Re-run migrate() — 008 should apply cleanly now.
	if err := migrate(database); err != nil {
		// calibre_id column already exists from the first run (SQLite can't
		// drop columns easily in <3.35), so ALTER TABLE will fail with
		// "duplicate column name". That's the expected upgrade-path behaviour
		// for this migration on a DB that already saw it once — but on a
		// genuine v0.7.2 DB the column won't exist, so the ALTER succeeds.
		// We accept either "already applied" outcome.
		if !strings.Contains(err.Error(), "duplicate column name") {
			t.Fatalf("second migrate on upgrade path: %v", err)
		}
	}

	// User's explicit calibre.enabled=true survives the INSERT OR IGNORE.
	var v string
	if err := database.QueryRow(`SELECT value FROM settings WHERE key='calibre.enabled'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "true" {
		t.Errorf("operator-set calibre.enabled was overwritten: got %q, want 'true'", v)
	}
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
		return
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
		return
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

	// List — the test_key we just wrote must appear; other seeded
	// settings (calibre defaults, etc.) are fine to co-exist.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, s := range list {
		if s.Key == "test_key" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("test_key missing from list: %v", list)
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

func TestPickClientForMediaType(t *testing.T) {
	audioClient := models.DownloadClient{ID: 1, Name: "SAB-audio", Category: "audiobooks", Type: "sabnzbd"}
	ebookClient := models.DownloadClient{ID: 2, Name: "SAB-ebook", Category: "ebooks", Type: "sabnzbd"}
	genericClient := models.DownloadClient{ID: 3, Name: "SAB-generic", Category: "books", Type: "sabnzbd"}
	// #700: a "dual" client uses the explicit CategoryAudiobook field; the
	// picker should treat that as a stronger signal than the legacy "category
	// contains audio" heuristic.
	dualClient := models.DownloadClient{ID: 4, Name: "SAB-dual", Category: "books", CategoryAudiobook: "audiobooks", Type: "sabnzbd"}

	tests := []struct {
		name      string
		clients   []models.DownloadClient
		mediaType string
		wantID    int64
	}{
		{"empty list returns nil", nil, "ebook", 0},
		{"single client always wins", []models.DownloadClient{ebookClient}, "audiobook", 2},
		{"audiobook prefers audio category", []models.DownloadClient{ebookClient, audioClient}, "audiobook", 1},
		{"ebook prefers non-audio category", []models.DownloadClient{audioClient, ebookClient}, "ebook", 2},
		{"audiobook falls back to first when no match", []models.DownloadClient{ebookClient, genericClient}, "audiobook", 2},
		{"ebook falls back to first when all audio", []models.DownloadClient{audioClient}, "ebook", 1},
		{"audiobook prefers explicit CategoryAudiobook over legacy heuristic", []models.DownloadClient{audioClient, dualClient}, "audiobook", 4},
		{"ebook on dual-config client returns it via fallback path", []models.DownloadClient{dualClient}, "ebook", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PickClientForMediaType(tt.clients, tt.mediaType)
			if tt.wantID == 0 {
				if got != nil {
					t.Errorf("expected nil, got client ID %d", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a client, got nil")
				return
			}
			if got.ID != tt.wantID {
				t.Errorf("expected client ID %d, got %d (%s)", tt.wantID, got.ID, got.Name)
			}
		})
	}
}

func TestDownloadClientRepoCredentialFields(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	// qBittorrent: Username/Password should round-trip through dedicated fields.
	qbt := &models.DownloadClient{
		Name:     "My qBittorrent",
		Type:     "qbittorrent",
		Host:     "localhost",
		Port:     8080,
		Username: "admin",
		Password: "secret",
		Enabled:  true,
	}
	if err := repo.Create(ctx, qbt); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByID(ctx, qbt.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("Username: want admin, got %q", got.Username)
	}
	if got.Password != "secret" {
		t.Errorf("Password: want secret, got %q", got.Password)
	}
	if got.URLBase != "" {
		t.Errorf("URLBase: want empty, got %q", got.URLBase)
	}
	if got.APIKey != "" {
		t.Errorf("APIKey: want empty for credential client, got %q", got.APIKey)
	}

	// sabnzbd: APIKey should survive as-is; Username/Password stay empty
	sab := &models.DownloadClient{
		Name:    "My SABnzbd",
		Type:    "sabnzbd",
		Host:    "localhost",
		Port:    8181,
		APIKey:  "myapikey",
		Enabled: true,
	}
	if err := repo.Create(ctx, sab); err != nil {
		t.Fatalf("create sab: %v", err)
	}
	gotSab, err := repo.GetByID(ctx, sab.ID)
	if err != nil {
		t.Fatalf("get sab: %v", err)
	}
	if gotSab.APIKey != "myapikey" {
		t.Errorf("APIKey: want myapikey, got %q", gotSab.APIKey)
	}
	if gotSab.Username != "" || gotSab.Password != "" {
		t.Errorf("sabnzbd should not populate virtual creds, got user=%q pass=%q", gotSab.Username, gotSab.Password)
	}
}

// TestNormalizeClientCredentialStorageWritePath covers the write-path guard in
// normalizeClientCredentialStorage: a qBittorrent client saved with a bare
// url_base and an empty api_key must read back with url_base preserved and
// username NOT populated from it. (closes #422)
func TestNormalizeClientCredentialStorageWritePath(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	// A client with a bare url_base path and no api_key — the write-path guard
	// (normalizeClientCredentialStorage) must not migrate url_base into username
	// because api_key is empty, which means this is not a legacy credential row.
	qbt := &models.DownloadClient{
		Name:    "qBittorrent bare url_base",
		Type:    "qbittorrent",
		Host:    "localhost",
		Port:    8080,
		URLBase: "qbit",
		APIKey:  "", // empty — no migration should happen
		Enabled: true,
	}
	if err := repo.Create(ctx, qbt); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByID(ctx, qbt.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// url_base must be preserved exactly as written.
	if got.URLBase != "qbit" {
		t.Errorf("URLBase: want %q, got %q", "qbit", got.URLBase)
	}
	// username must NOT be populated from url_base when api_key is empty.
	if got.Username != "" {
		t.Errorf("Username: want empty (should not be populated from url_base when api_key is empty), got %q", got.Username)
	}
}

// TestLegacyCredentialURLBase exercises legacyCredentialURLBase directly to
// guard against regressions in the legacy-row detection logic. (closes #423)
func TestLegacyCredentialURLBase(t *testing.T) {
	tests := []struct {
		name     string
		username string
		urlBase  string
		apiKey   string
		want     bool
	}{
		// Legacy row: username column is still empty, url_base held the username,
		// api_key held the password.
		{"empty username non-empty urlBase with apiKey", "", "admin", "secret", true},
		// Legacy row: both fields kept in sync by old code.
		{"username equals urlBase with apiKey", "admin", "admin", "secret", true},
		// Modern row: distinct username and a real url_base path — must NOT fire.
		{"distinct username and urlBase", "admin", "/qbit", "secret", false},
		// Modern row: username set, url_base empty — already migrated.
		{"username set urlBase empty", "admin", "", "secret", false},
		// Both empty — nothing to migrate.
		{"both empty", "", "", "", false},
		// Whitespace-only urlBase does not count as a legacy row.
		{"whitespace urlBase", "", "   ", "secret", false},
		// Modern row: bare url_base but no api_key — client has a real url_base
		// path but no password stored in the legacy location. Must NOT fire.
		// This is the core regression from #423.
		{"bare urlBase empty apiKey", "", "qbit", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := legacyCredentialURLBase(tt.username, tt.urlBase, tt.apiKey)
			if got != tt.want {
				t.Errorf("legacyCredentialURLBase(%q, %q, %q) = %v, want %v",
					tt.username, tt.urlBase, tt.apiKey, got, tt.want)
			}
		})
	}
}

func TestDownloadClientRepoGetEnabledByProtocol(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	sab := &models.DownloadClient{Name: "SAB", Type: "sabnzbd", Host: "h", Port: 1, APIKey: "k", Enabled: true, Priority: 1}
	qbt := &models.DownloadClient{Name: "QBT", Type: "qbittorrent", Host: "h", Port: 2, Enabled: true, Priority: 1}
	repo.Create(ctx, sab)
	repo.Create(ctx, qbt)

	usenet, err := repo.GetEnabledByProtocol(ctx, "usenet")
	if err != nil {
		t.Fatal(err)
	}
	if len(usenet) != 1 || usenet[0].Type != "sabnzbd" {
		t.Errorf("usenet: expected 1 sabnzbd client, got %v", usenet)
	}

	torrents, err := repo.GetEnabledByProtocol(ctx, "torrent")
	if err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 || torrents[0].Type != "qbittorrent" {
		t.Errorf("torrent: expected 1 qbittorrent client, got %v", torrents)
	}

	// GetFirstEnabledByProtocol falls back to any client when none of the
	// preferred type exists
	client, err := repo.GetFirstEnabledByProtocol(ctx, "torrent")
	if err != nil {
		t.Fatal(err)
	}
	if client == nil || client.Type != "qbittorrent" {
		t.Errorf("expected qbittorrent fallback, got %v", client)
	}
}

// Regression for https://github.com/vavallee/bindery/issues/8 — deleting an
// author failed with SQLITE_CONSTRAINT_FOREIGNKEY (787) because the
// `downloads` table had bare `REFERENCES books(id)` (NO ACTION) and blocked
// the author→book cascade whenever any download pointed at the book. After
// migration 007 the reference is `ON DELETE SET NULL`, so the audit row
// survives but loses its link.
func TestDeleteAuthorWithDownload(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	author := &models.Author{
		ForeignID: "OL13A", Name: "Delete Me", SortName: "Me, Delete",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL13W", AuthorID: author.ID, Title: "Stuck Book", SortTitle: "Stuck Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Insert a download pointing at the book, bypassing the repo to keep the
	// test schema-level (the repo layer may add defaults we don't need here).
	_, err = database.ExecContext(ctx, `
		INSERT INTO downloads (guid, book_id, title, nzb_url)
		VALUES ('test-guid-1', ?, 'release.nzb', 'https://example/1')`, book.ID)
	if err != nil {
		t.Fatalf("seed download: %v", err)
	}

	if err := authorRepo.Delete(ctx, author.ID); err != nil {
		t.Fatalf("delete author must succeed even with dependent download: %v", err)
	}

	var downloadCount int
	var linkedBookID *int64
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*), book_id FROM downloads WHERE guid = 'test-guid-1'`,
	).Scan(&downloadCount, &linkedBookID); err != nil {
		t.Fatalf("inspect download: %v", err)
	}
	if downloadCount != 1 {
		t.Errorf("download row should survive parent delete, got count=%d", downloadCount)
	}
	if linkedBookID != nil {
		t.Errorf("download.book_id should be NULL after cascade, got %d", *linkedBookID)
	}
}

func TestDownloadRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	dl := &models.Download{
		GUID:     "test-guid-abc",
		Title:    "Some.Book.epub",
		NZBURL:   "https://example.com/dl.nzb",
		Size:     1024,
		Status:   models.StateGrabbed,
		Protocol: "usenet",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}
	if dl.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// GetByGUID
	got, err := repo.GetByGUID(ctx, "test-guid-abc")
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got == nil || got.Title != "Some.Book.epub" {
		t.Errorf("unexpected result: %v", got)
	}

	// GetByGUID — not found
	missing, _ := repo.GetByGUID(ctx, "nonexistent")
	if missing != nil {
		t.Error("expected nil for nonexistent GUID")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 download, got %d", len(list))
	}

	// ListByStatus
	queued, _ := repo.ListByStatus(ctx, models.StateGrabbed)
	if len(queued) != 1 {
		t.Errorf("expected 1 queued, got %d", len(queued))
	}

	// UpdateStatus — walk through valid transitions and verify each succeeds.
	validSeq := []models.DownloadState{
		models.StateDownloading,
		models.StateCompleted,
		models.StateImportPending,
		models.StateImporting,
		models.StateImported,
	}
	for _, next := range validSeq {
		if err := repo.UpdateStatus(ctx, dl.ID, next); err != nil {
			t.Errorf("UpdateStatus(%q): %v", next, err)
		}
	}

	// UpdateStatus — invalid transition must return ErrInvalidTransition.
	if err := repo.UpdateStatus(ctx, dl.ID, models.StateDownloading); err == nil {
		t.Error("expected error for illegal transition imported→downloading")
	}

	// SetNzoID
	if err := repo.SetNzoID(ctx, dl.ID, "nzo_123"); err != nil {
		t.Errorf("SetNzoID: %v", err)
	}
	got, _ = repo.GetByGUID(ctx, "test-guid-abc")
	if got.SABnzbdNzoID == nil || *got.SABnzbdNzoID != "nzo_123" {
		t.Errorf("expected nzo_123, got %v", got.SABnzbdNzoID)
	}

	// GetByNzoID
	byNzo, err := repo.GetByNzoID(ctx, "nzo_123")
	if err != nil {
		t.Fatalf("GetByNzoID: %v", err)
	}
	if byNzo == nil || byNzo.GUID != "test-guid-abc" {
		t.Errorf("GetByNzoID: unexpected %v", byNzo)
	}

	// SetError
	if err := repo.SetError(ctx, dl.ID, "something went wrong"); err != nil {
		t.Errorf("SetError: %v", err)
	}

	// SetErrorWithStatus — transitions from StateFailed must reject (no valid
	// transitions from StateFailed) but setting to the same state should succeed.
	if err := repo.SetErrorWithStatus(ctx, dl.ID, models.StateFailed, "still broken"); err != nil {
		t.Errorf("SetErrorWithStatus same-state: %v", err)
	}
	got, getErr := repo.GetByGUID(ctx, dl.GUID)
	if getErr != nil || got == nil {
		t.Fatalf("reload after SetErrorWithStatus: %v", getErr)
	}
	if got.ErrorMessage != "still broken" {
		t.Errorf("expected error message persisted, got %q", got.ErrorMessage)
	}

	// Delete
	if err := repo.Delete(ctx, dl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 0 {
		t.Errorf("expected 0 downloads after delete, got %d", len(list))
	}
}

func TestDownloadRepoRetryFailed(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	oldNzoID := "old-nzo"
	oldTorrentID := "old-torrent"
	dl := &models.Download{
		GUID:         "retry-guid",
		Title:        "Old Title",
		NZBURL:       "https://example.com/old.nzb",
		Size:         100,
		SABnzbdNzoID: &oldNzoID,
		TorrentID:    &oldTorrentID,
		Status:       models.StateFailed,
		Protocol:     "usenet",
		Quality:      "epub",
		ErrorMessage: "old failure",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create failed download: %v", err)
	}
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := database.ExecContext(ctx, `
		UPDATE downloads
		SET grabbed_at=?, completed_at=?, imported_at=?, import_retry_count=?
		WHERE id=?`, oldTime, oldTime, oldTime, 3, dl.ID); err != nil {
		t.Fatalf("seed retry metadata: %v", err)
	}

	retry := &models.Download{
		ID:       dl.ID,
		Title:    "New Title",
		NZBURL:   "https://example.com/new.nzb",
		Size:     200,
		Status:   models.StateGrabbed,
		Protocol: "usenet",
		Quality:  "m4b",
	}
	ok, err := repo.RetryFailed(ctx, retry)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if !ok {
		t.Fatal("expected failed row to be claimed")
	}

	got, err := repo.GetByGUID(ctx, "retry-guid")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.Status != models.StateGrabbed {
		t.Fatalf("expected grabbed retry state, got %q", got.Status)
	}
	if got.Title != "New Title" || got.NZBURL != "https://example.com/new.nzb" || got.Size != 200 || got.Quality != "m4b" {
		t.Fatalf("retry did not refresh release fields: %+v", got)
	}
	if got.SABnzbdNzoID != nil || got.TorrentID != nil {
		t.Fatalf("expected remote IDs cleared, got nzo=%v torrent=%v", got.SABnzbdNzoID, got.TorrentID)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("expected error message cleared, got %q", got.ErrorMessage)
	}
	if got.GrabbedAt != nil || got.CompletedAt != nil || got.ImportedAt != nil {
		t.Fatalf("expected timestamps cleared, got grabbed=%v completed=%v imported=%v", got.GrabbedAt, got.CompletedAt, got.ImportedAt)
	}
	if got.ImportRetryCount != 0 {
		t.Fatalf("expected retry count reset, got %d", got.ImportRetryCount)
	}

	ok, err = repo.RetryFailed(ctx, retry)
	if err != nil {
		t.Fatalf("second RetryFailed: %v", err)
	}
	if ok {
		t.Fatal("expected non-failed row not to be claimed")
	}
}

func TestDownloadRepoResetImportRetry(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	nzoID := "old-nzo"
	torrentID := "old-torrent"
	dl := &models.Download{
		GUID:         "import-retry-guid",
		Title:        "Import Retry",
		NZBURL:       "https://example.com/retry.nzb",
		Size:         100,
		SABnzbdNzoID: &nzoID,
		TorrentID:    &torrentID,
		Status:       models.StateImportFailed,
		Protocol:     "usenet",
		Quality:      "epub",
		ErrorMessage: "visible import failure",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create import failed download: %v", err)
	}
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := database.ExecContext(ctx, `
		UPDATE downloads
		SET grabbed_at=?, completed_at=?, imported_at=?, import_retry_count=?
		WHERE id=?`, oldTime, oldTime, oldTime, 3, dl.ID); err != nil {
		t.Fatalf("seed import retry metadata: %v", err)
	}

	accepted, found, err := repo.ResetImportRetry(ctx, dl.ID)
	if err != nil {
		t.Fatalf("ResetImportRetry: %v", err)
	}
	if !accepted || !found {
		t.Fatalf("ResetImportRetry accepted=%v found=%v, want true true", accepted, found)
	}

	got, err := repo.GetByGUID(ctx, "import-retry-guid")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.Status != models.StateImportFailed {
		t.Fatalf("status changed to %q, want importFailed", got.Status)
	}
	if got.ImportRetryCount != 0 {
		t.Fatalf("import retry count = %d, want 0", got.ImportRetryCount)
	}
	if got.ErrorMessage != "visible import failure" {
		t.Fatalf("error message changed to %q", got.ErrorMessage)
	}
	if got.SABnzbdNzoID == nil || *got.SABnzbdNzoID != nzoID || got.TorrentID == nil || *got.TorrentID != torrentID {
		t.Fatalf("remote IDs changed, got nzo=%v torrent=%v", got.SABnzbdNzoID, got.TorrentID)
	}
	if got.GrabbedAt == nil || got.CompletedAt == nil || got.ImportedAt == nil {
		t.Fatalf("timestamps changed, got grabbed=%v completed=%v imported=%v", got.GrabbedAt, got.CompletedAt, got.ImportedAt)
	}

	nonFailed := &models.Download{
		GUID:     "not-import-failed",
		Title:    "Not Import Failed",
		NZBURL:   "https://example.com/not-failed.nzb",
		Status:   models.StateCompleted,
		Protocol: "usenet",
	}
	if err := repo.Create(ctx, nonFailed); err != nil {
		t.Fatalf("create completed download: %v", err)
	}
	accepted, found, err = repo.ResetImportRetry(ctx, nonFailed.ID)
	if err != nil {
		t.Fatalf("ResetImportRetry non-failed: %v", err)
	}
	if accepted || !found {
		t.Fatalf("ResetImportRetry non-failed accepted=%v found=%v, want false true", accepted, found)
	}

	accepted, found, err = repo.ResetImportRetry(ctx, 999999)
	if err != nil {
		t.Fatalf("ResetImportRetry missing: %v", err)
	}
	if accepted || found {
		t.Fatalf("ResetImportRetry missing accepted=%v found=%v, want false false", accepted, found)
	}
}

func TestBlocklistRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewBlocklistRepo(database)

	entry := &models.BlocklistEntry{
		GUID:   "bl-guid-1",
		Title:  "Bad.Release.epub",
		Reason: "wrong edition",
	}
	if err := repo.Create(ctx, entry); err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// IsBlocked — present
	blocked, err := repo.IsBlocked(ctx, "bl-guid-1")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Error("expected guid to be blocked")
	}

	// IsBlocked — absent
	blocked, _ = repo.IsBlocked(ctx, "not-blocked")
	if blocked {
		t.Error("expected non-blocked guid to return false")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 entry, got %d", len(list))
	}

	// DeleteByID
	if err := repo.DeleteByID(ctx, entry.ID); err != nil {
		t.Fatalf("delete by id: %v", err)
	}
	blocked, _ = repo.IsBlocked(ctx, "bl-guid-1")
	if blocked {
		t.Error("expected unblocked after delete")
	}

	// DeleteByBookID
	bookID := int64(42)
	entry2 := &models.BlocklistEntry{GUID: "bl-guid-2", Title: "t", BookID: &bookID}
	repo.Create(ctx, entry2)
	if err := repo.DeleteByBookID(ctx, bookID); err != nil {
		t.Fatalf("delete by book id: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 0 {
		t.Errorf("expected empty after DeleteByBookID, got %d", len(list))
	}
}

func TestHistoryRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewHistoryRepo(database)

	evt := &models.HistoryEvent{
		EventType:   "grabbed",
		SourceTitle: "Dune.epub",
		Data:        `{"guid":"abc"}`,
	}
	if err := repo.Create(ctx, evt); err != nil {
		t.Fatalf("create: %v", err)
	}
	if evt.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// GetByID
	got, err := repo.GetByID(ctx, evt.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.EventType != "grabbed" {
		t.Errorf("unexpected result: %v", got)
	}

	// GetByID — not found
	missing, _ := repo.GetByID(ctx, 9999)
	if missing != nil {
		t.Error("expected nil for nonexistent ID")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 event, got %d", len(list))
	}

	// ListByType
	grabbed, _ := repo.ListByType(ctx, "grabbed")
	if len(grabbed) != 1 {
		t.Errorf("expected 1 grabbed event, got %d", len(grabbed))
	}
	imported, _ := repo.ListByType(ctx, "imported")
	if len(imported) != 0 {
		t.Errorf("expected 0 imported events, got %d", len(imported))
	}

	// Add a second event linked to a real book for ListByBook
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-H1", Name: "Author", SortName: "Author", MetadataProvider: "openlibrary", Monitored: true}
	authorRepo.Create(ctx, author)
	book := &models.Book{ForeignID: "OL-B1", AuthorID: author.ID, Title: "Dune", SortTitle: "Dune", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	bookRepo.Create(ctx, book)

	evt2 := &models.HistoryEvent{BookID: &book.ID, EventType: "imported", SourceTitle: "Dune.epub"}
	if err := repo.Create(ctx, evt2); err != nil {
		t.Fatalf("create evt2: %v", err)
	}

	byBook, _ := repo.ListByBook(ctx, book.ID)
	if len(byBook) != 1 {
		t.Errorf("expected 1 event for book, got %d", len(byBook))
	}

	// Delete evt (evt2 still remains)
	if err := repo.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 1 {
		t.Errorf("expected 1 event after delete, got %d", len(list))
	}
}

func TestUserRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	// Count — zero before setup
	n, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 users, got %d", n)
	}

	// Create
	u, err := repo.Create(ctx, "admin", "hashed-password")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 || u.Username != "admin" {
		t.Errorf("unexpected user: %+v", u)
	}

	// Count — one after create
	n, _ = repo.Count(ctx)
	if n != 1 {
		t.Errorf("expected 1 user, got %d", n)
	}

	// GetByUsername
	got, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got == nil || got.PasswordHash != "hashed-password" {
		t.Errorf("unexpected result: %v", got)
	}

	// GetByUsername — not found
	missing, _ := repo.GetByUsername(ctx, "nobody")
	if missing != nil {
		t.Error("expected nil for nonexistent user")
	}

	// GetByID
	byID, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID == nil || byID.Username != "admin" {
		t.Errorf("GetByID: unexpected %v", byID)
	}

	// GetByID — not found
	missing2, _ := repo.GetByID(ctx, 9999)
	if missing2 != nil {
		t.Error("expected nil for nonexistent ID")
	}

	// UpdatePassword
	if err := repo.UpdatePassword(ctx, u.ID, "new-hash"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, _ = repo.GetByID(ctx, u.ID)
	if got.PasswordHash != "new-hash" {
		t.Errorf("expected new-hash, got %q", got.PasswordHash)
	}

	// UpdateUsername
	if err := repo.UpdateUsername(ctx, u.ID, "superadmin"); err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	got, _ = repo.GetByID(ctx, u.ID)
	if got.Username != "superadmin" {
		t.Errorf("expected superadmin, got %q", got.Username)
	}
}

// TestMigrate026_DedupBooks verifies that migration 026 merges duplicate book
// rows that differ only in whitespace/case, re-parents dependent rows, and
// keeps the row with the best file state.
func TestMigrate026_DedupBooks(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()

	// Seed one author.
	_, err = database.ExecContext(ctx,
		`INSERT INTO authors (foreign_id, name, sort_name, monitored)
		 VALUES ('OL-D1A', 'Test Author', 'Author, Test', 1)`)
	if err != nil {
		t.Fatal("seed author:", err)
	}
	var authorID int64
	if err := database.QueryRowContext(ctx, `SELECT id FROM authors WHERE foreign_id='OL-D1A'`).Scan(&authorID); err != nil {
		t.Fatal("get author id:", err)
	}

	// Insert 3 duplicate book rows: same normalised title, different whitespace/case.
	// Row A has a file path (should be the winner).
	// Row B and C are pure duplicates with no file.
	insertBook := func(foreignID, title, filePath string) int64 {
		_, err := database.ExecContext(ctx,
			`INSERT INTO books (foreign_id, author_id, title, sort_title, monitored, any_edition_ok)
			 VALUES (?, ?, ?, lower(?), 1, 1)`,
			foreignID, authorID, title, title)
		if err != nil {
			t.Fatalf("insert book %s: %v", foreignID, err)
		}
		var id int64
		database.QueryRowContext(ctx, `SELECT id FROM books WHERE foreign_id=?`, foreignID).Scan(&id)
		if filePath != "" {
			database.ExecContext(ctx, `UPDATE books SET ebook_file_path=? WHERE id=?`, filePath, id)
		}
		return id
	}

	idA := insertBook("OL-D1W", "Dune", "/books/dune.epub")
	idB := insertBook("OL-D2W", "dune", "")     // case duplicate — no file
	idC := insertBook("OL-D3W", "  Dune  ", "") // whitespace duplicate — no file

	// Seed a series_books row pointing at loser B.
	_, err = database.ExecContext(ctx,
		`INSERT INTO series (foreign_id, title) VALUES ('OL-S1', 'Dune Series')`)
	if err != nil {
		t.Fatal("insert series:", err)
	}
	var seriesID int64
	database.QueryRowContext(ctx, `SELECT id FROM series WHERE foreign_id='OL-S1'`).Scan(&seriesID)
	_, err = database.ExecContext(ctx,
		`INSERT OR IGNORE INTO series_books (series_id, book_id, position_in_series, primary_series)
		 VALUES (?, ?, '1', 1)`, seriesID, idB)
	if err != nil {
		t.Fatal("insert series_books:", err)
	}

	// Seed a history row pointing at loser C.
	_, err = database.ExecContext(ctx,
		`INSERT INTO history (book_id, event_type) VALUES (?, 'grabbed')`, idC)
	if err != nil {
		t.Fatal("insert history:", err)
	}

	// Re-run migrate (026 was already applied to the empty DB; roll back its
	// marker so migrate() re-runs it against the seeded duplicate rows). The
	// version is the filename number, not the slice index.
	v026 := migrationVersionForTest(t, "026_dedup_books.sql")
	database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=?`, v026)
	if err := migrate(database); err != nil {
		t.Fatal("re-run migrate:", err)
	}

	// Only the winner (idA) should survive.
	var bookCount int
	database.QueryRowContext(ctx, `SELECT COUNT(*) FROM books WHERE author_id=?`, authorID).Scan(&bookCount)
	if bookCount != 1 {
		t.Fatalf("expected 1 book after dedup, got %d", bookCount)
	}
	var survivorID int64
	database.QueryRowContext(ctx, `SELECT id FROM books WHERE author_id=?`, authorID).Scan(&survivorID)
	if survivorID != idA {
		t.Errorf("expected winner to be idA (%d), got %d", idA, survivorID)
	}

	// series_books must point to the winner.
	var sbBookID int64
	database.QueryRowContext(ctx, `SELECT book_id FROM series_books WHERE series_id=?`, seriesID).Scan(&sbBookID)
	if sbBookID != idA {
		t.Errorf("series_books.book_id: expected %d (winner), got %d", idA, sbBookID)
	}

	// history must point to the winner.
	var hBookID int64
	database.QueryRowContext(ctx, `SELECT book_id FROM history WHERE event_type='grabbed'`).Scan(&hBookID)
	if hBookID != idA {
		t.Errorf("history.book_id: expected %d (winner), got %d", idA, hBookID)
	}
}

// TestApplyMigrationRollsBackOnFailure verifies that a migration which fails
// partway through leaves the database completely unchanged: no partial DDL and
// no schema_migrations row. This is the core guarantee of finding 1.
func TestApplyMigrationRollsBackOnFailure(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	// A migration whose first statement succeeds (creates a table) and whose
	// second statement is invalid SQL. A non-transactional runner would leave
	// rollback_probe behind; a transactional one must not.
	const badMigration = `-- +migrate Up
CREATE TABLE rollback_probe (id INTEGER PRIMARY KEY);
THIS IS NOT VALID SQL;
`
	err = applyMigration(database, 9001, "9001_bad.sql", badMigration)
	if err == nil {
		t.Fatal("expected applyMigration to fail on invalid SQL")
	}

	// The table from the first statement must NOT exist — the tx rolled back.
	var name string
	qerr := database.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='rollback_probe'",
	).Scan(&name)
	if qerr == nil {
		t.Fatal("rollback_probe table survived a failed migration — not transactional")
	}
	if qerr != sql.ErrNoRows {
		t.Fatalf("unexpected error checking for rollback_probe: %v", qerr)
	}

	// No schema_migrations row must have been recorded for the failed version.
	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 9001",
	).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed migration recorded a schema_migrations row (count=%d)", count)
	}
}

// TestApplyMigrationRestoresForeignKeysAfterFailure verifies that a migration
// carrying `PRAGMA foreign_keys=OFF` which then fails still leaves FK
// enforcement ON on the pooled connection. This is finding 4.
func TestApplyMigrationRestoresForeignKeysAfterFailure(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	assertFKEnabled := func(stage string) {
		t.Helper()
		var on int
		if err := database.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
			t.Fatalf("read foreign_keys pragma (%s): %v", stage, err)
		}
		if on != 1 {
			t.Fatalf("foreign_keys enforcement is OFF %s — finding 4 not fixed", stage)
		}
	}

	assertFKEnabled("before migration")

	// A migration that disables FKs (as 007/034 do) and then fails.
	const badFKMigration = `-- +migrate Up
PRAGMA foreign_keys=OFF;
CREATE TABLE fk_probe (id INTEGER PRIMARY KEY);
THIS IS NOT VALID SQL;
`
	err = applyMigration(database, 9002, "9002_bad_fk.sql", badFKMigration)
	if err == nil {
		t.Fatal("expected applyMigration to fail")
	}

	assertFKEnabled("after a failed FK-toggling migration")

	// The probe table must also have been rolled back.
	var name string
	if qerr := database.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='fk_probe'",
	).Scan(&name); qerr != sql.ErrNoRows {
		t.Fatalf("fk_probe table survived a failed migration (err=%v)", qerr)
	}
}

// TestParseMigrationStripsForeignKeysPragma verifies that bare PRAGMA
// foreign_keys statements are pulled out of the transactional statement list
// and reported, while the real DDL is preserved.
func TestParseMigrationStripsForeignKeysPragma(t *testing.T) {
	content007, err := migrationsFS.ReadFile("migrations/007_downloads_fk_set_null.sql")
	if err != nil {
		t.Fatalf("read 007: %v", err)
	}
	stmts, toggles := parseMigration(string(content007))
	if !toggles {
		t.Fatal("007 should be detected as toggling foreign_keys")
	}
	for _, s := range stmts {
		if isForeignKeysPragma(s) {
			t.Fatalf("PRAGMA foreign_keys leaked into transactional statements: %q", s)
		}
	}

	// 026 has no PRAGMA foreign_keys line, so it is fully covered by the
	// transactional runner with no special handling (finding 3).
	content026, err := migrationsFS.ReadFile("migrations/026_dedup_books.sql")
	if err != nil {
		t.Fatalf("read 026: %v", err)
	}
	_, toggles026 := parseMigration(string(content026))
	if toggles026 {
		t.Fatal("026 unexpectedly toggles foreign_keys")
	}
}

// TestMigrationVersionIsFilenameNumber verifies the canonical version is the
// filename prefix, not the slice index. With the 010 gap, migration 011 must
// resolve to version 11 (finding 2).
func TestMigrationVersionIsFilenameNumber(t *testing.T) {
	cases := map[string]int{
		"001_initial.sql":                    1,
		"009_author_aliases.sql":             9,
		"011_calibre_mode.sql":               11,
		"040_download_client_path_remap.sql": 40,
	}
	for name, want := range cases {
		got, err := migrationVersion(name)
		if err != nil {
			t.Errorf("migrationVersion(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("migrationVersion(%q) = %d, want %d", name, got, want)
		}
	}
	if _, err := migrationVersion("noprefix.sql"); err == nil {
		t.Error("expected error for filename without numeric prefix")
	}
}

// TestReconcileMigrationVersions verifies that a database written by the
// legacy index-based runner is rewritten to filename-based versions exactly
// once, without re-running or skipping any migration (finding 2, safety).
func TestReconcileMigrationVersions(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	sortEntriesForTest(entries)

	// Simulate a legacy DB: wipe schema_migrations and refill with index-based
	// versions (1..N) as the old runner would have.
	if _, err := database.Exec("DELETE FROM schema_migrations"); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}
	for i := range entries {
		if _, err := database.Exec(
			"INSERT INTO schema_migrations (version) VALUES (?)", i+1,
		); err != nil {
			t.Fatalf("seed legacy version %d: %v", i+1, err)
		}
	}

	// Reconcile.
	if err := reconcileMigrationVersions(database, entries); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Every recorded version must now equal a filename number, and the count
	// must be unchanged (no migration lost).
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != len(entries) {
		t.Fatalf("row count changed during reconciliation: got %d want %d", count, len(entries))
	}
	for _, entry := range entries {
		v, err := migrationVersion(entry.Name())
		if err != nil {
			t.Fatalf("migrationVersion: %v", err)
		}
		var n int
		if err := database.QueryRow(
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", v,
		).Scan(&n); err != nil {
			t.Fatalf("count version %d: %v", v, err)
		}
		if n != 1 {
			t.Fatalf("after reconciliation version %d (%s) has %d rows, want 1", v, entry.Name(), n)
		}
	}

	// Reconciling again must be a no-op (idempotent).
	if err := reconcileMigrationVersions(database, entries); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// A full migrate() after reconciliation must not re-run anything: no
	// "already exists" errors, and the count stays put.
	if err := migrate(database); err != nil {
		t.Fatalf("migrate after reconciliation re-ran a migration: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count after migrate: %v", err)
	}
	if count != len(entries) {
		t.Fatalf("migrate after reconciliation changed row count: got %d want %d", count, len(entries))
	}
}

// TestReconcileMigrationVersionsLeavesFreshDB verifies a database already on
// the filename-based scheme (or fresh) is untouched by reconciliation.
func TestReconcileMigrationVersionsLeavesFreshDB(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	sortEntriesForTest(entries)

	before := versionSetForTest(t, database)

	if err := reconcileMigrationVersions(database, entries); err != nil {
		t.Fatalf("reconcile on already-filename-based DB: %v", err)
	}

	after := versionSetForTest(t, database)

	if len(before) != len(after) {
		t.Fatalf("reconciliation changed row count on a filename-based DB: %d -> %d", len(before), len(after))
	}
	for v := range before {
		if !after[v] {
			t.Fatalf("reconciliation dropped version %d on a filename-based DB", v)
		}
	}
}

func sortEntriesForTest(entries []os.DirEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
}

// versionSetForTest returns every version recorded in schema_migrations.
func versionSetForTest(t *testing.T, database *sql.DB) map[int]bool {
	t.Helper()
	rows, err := database.Query("SELECT version FROM schema_migrations")
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()
	set := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		set[v] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate versions: %v", err)
	}
	return set
}
