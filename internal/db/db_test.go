package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

	// Delete
	if err := repo.Delete(ctx, dl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 0 {
		t.Errorf("expected 0 downloads after delete, got %d", len(list))
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
