package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type reorgEnv struct {
	s       *Scanner
	books   *db.BookRepo
	authors *db.AuthorRepo
}

func reorgFixture(t *testing.T) (env reorgEnv, libraryDir, audiobookDir string, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx = context.Background()
	libraryDir = t.TempDir()
	audiobookDir = t.TempDir()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	// Default templates: {Author}/{Title} ({Year})/{Title} - {Author}.{ext}
	// and {Author}/{Title} ({Year}) for audiobooks.
	s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database), libraryDir, audiobookDir, "", "", "")
	s.WithSettings(db.NewSettingsRepo(database))
	s.WithRootFolders(db.NewRootFolderRepo(database))
	s.WithSeriesRepo(db.NewSeriesRepo(database))
	return reorgEnv{s: s, books: books, authors: authors}, libraryDir, audiobookDir, ctx
}

func (env reorgEnv) seedAuthor(t *testing.T, ctx context.Context, authorName string) *models.Author {
	t.Helper()
	author := &models.Author{ForeignID: "A-" + authorName, Name: authorName, SortName: authorName, Monitored: true, MetadataProvider: "openlibrary"}
	if err := env.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return author
}

func (env reorgEnv) seedBook(t *testing.T, ctx context.Context, author *models.Author, title string) *models.Book {
	t.Helper()
	rel := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	book := &models.Book{
		ForeignID: "OL-" + title, AuthorID: author.ID, Title: title, SortTitle: title,
		Status: models.BookStatusImported, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", ReleaseDate: &rel,
	}
	if err := env.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return book
}

func (env reorgEnv) seed(t *testing.T, ctx context.Context, authorName, title string) *models.Book {
	t.Helper()
	return env.seedBook(t, ctx, env.seedAuthor(t, ctx, authorName), title)
}

func writeFileAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("book bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReorganize_EbookMove(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")

	// File sits at a non-templated location.
	oldPath := filepath.Join(libraryDir, "misc", "downloaded", "randomname.epub")
	writeFileAt(t, oldPath)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, oldPath); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")

	// Preview.
	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(moves))
	}
	if moves[0].Status != ReorgStatusMove {
		t.Fatalf("status = %q, want move", moves[0].Status)
	}
	if moves[0].Proposed != want {
		t.Fatalf("proposed = %q, want %q", moves[0].Proposed, want)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("preview must not move the file")
	}

	// Apply.
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if len(results) != 1 || results[0].Status != ReorgStatusMoved {
		t.Fatalf("apply result = %+v, want moved", results)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("file not at templated location: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file should be gone, stat err = %v", err)
	}
	// Empty source parents pruned up to (not including) the library root.
	if _, err := os.Stat(filepath.Join(libraryDir, "misc")); !os.IsNotExist(err) {
		t.Errorf("empty source parent 'misc' should be pruned")
	}
	if _, err := os.Stat(libraryDir); err != nil {
		t.Errorf("library root must not be pruned")
	}
	// Index updated.
	files, err := env.books.ListBookFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != want {
		t.Errorf("book_files path = %v, want %q", files, want)
	}
}

func TestReorganize_Noop(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	// Already at the templated path.
	p := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, p)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}
	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].Status != ReorgStatusNoop {
		t.Fatalf("want single noop, got %+v", moves)
	}
	// Apply on a noop is a no-op result, file untouched.
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if results[0].Status != ReorgStatusNoop {
		t.Errorf("apply status = %q, want noop", results[0].Status)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("noop must not move the file: %v", err)
	}
}

func TestReorganize_Collision(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")

	oldPath := filepath.Join(libraryDir, "elsewhere", "book.epub")
	writeFileAt(t, oldPath)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, oldPath); err != nil {
		t.Fatal(err)
	}
	// Pre-occupy the templated destination with an unrelated file.
	dest := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, dest)

	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moves[0].Status != ReorgStatusCollision {
		t.Fatalf("status = %q, want collision", moves[0].Status)
	}
	// Apply must refuse to overwrite.
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if results[0].Status != ReorgStatusCollision {
		t.Errorf("apply status = %q, want collision (no overwrite)", results[0].Status)
	}
	if b, _ := os.ReadFile(oldPath); string(b) != "book bytes" {
		t.Errorf("source must remain untouched on collision")
	}
}

func TestReorganize_Missing(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	// Registered but not on disk.
	ghost := filepath.Join(libraryDir, "ghost", "gone.epub")
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, ghost); err != nil {
		t.Fatal(err)
	}
	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moves[0].Status != ReorgStatusMissing {
		t.Fatalf("status = %q, want missing", moves[0].Status)
	}
}

func TestReorganize_AudiobookFolderMove(t *testing.T) {
	env, _, audiobookDir, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")

	// Audiobook is a folder holding multiple tracks.
	oldDir := filepath.Join(audiobookDir, "unsorted", "My Book audio")
	writeFileAt(t, filepath.Join(oldDir, "part1.mp3"))
	writeFileAt(t, filepath.Join(oldDir, "part2.mp3"))
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, oldDir); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(audiobookDir, "Jane Doe", "My Book (2020)")
	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moves[0].Status != ReorgStatusMove || moves[0].Proposed != want {
		t.Fatalf("audiobook preview = %+v, want move to %q", moves[0], want)
	}
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if results[0].Status != ReorgStatusMoved {
		t.Fatalf("apply = %+v, want moved", results[0])
	}
	for _, part := range []string{"part1.mp3", "part2.mp3"} {
		if _, err := os.Stat(filepath.Join(want, part)); err != nil {
			t.Errorf("track %s missing at destination: %v", part, err)
		}
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("old audiobook dir should be gone")
	}
}

// TestReorganize_DualFormatSharedRoot is the regression test for the audiobook
// UniqueDir mismatch: with BINDERY_AUDIOBOOK_DIR unset (audiobookDir ==
// libraryDir), a dual-format book's audiobook template resolves to the same
// "Title (Year)" folder the ebook already sits in. Import parks it at
// "Title (Year) (2)"; reorganize must read that as a noop, not a false
// collision, and must not try to move an already-correctly-placed audiobook.
func TestReorganize_DualFormatSharedRoot(t *testing.T) {
	// Shared root: audiobookDir == libraryDir.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	root := t.TempDir()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database), root, root, "", "", "")
	s.WithSettings(db.NewSettingsRepo(database))
	s.WithRootFolders(db.NewRootFolderRepo(database))
	s.WithSeriesRepo(db.NewSeriesRepo(database))
	env := reorgEnv{s: s, books: books, authors: authors}

	book := env.seed(t, ctx, "Jane Doe", "My Book")

	// Ebook already at its templated path: <root>/Jane Doe/My Book (2020)/My Book - Jane Doe.epub
	ebook := filepath.Join(root, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, ebook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, ebook); err != nil {
		t.Fatal(err)
	}
	// Audiobook parked at the uniquified folder <root>/Jane Doe/My Book (2020) (2)
	// (where import would have placed it, since (2020) is the ebook's folder).
	audioDir := filepath.Join(root, "Jane Doe", "My Book (2020) (2)")
	writeFileAt(t, filepath.Join(audioDir, "part1.mp3"))
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, audioDir); err != nil {
		t.Fatal(err)
	}

	moves, err := s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Both files are already correctly placed — no collisions, no moves.
	for _, m := range moves {
		if m.Status != ReorgStatusNoop {
			t.Errorf("%s file %q: status = %q (%s), want noop", m.Format, m.Current, m.Status, m.Message)
		}
	}
}

func TestReorganize_AuthorAndLibraryScope(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	author := env.seedAuthor(t, ctx, "Jane Doe")
	b1 := env.seedBook(t, ctx, author, "Book One")
	b2 := env.seedBook(t, ctx, author, "Book Two")

	for _, b := range []*models.Book{b1, b2} {
		p := filepath.Join(libraryDir, "flat", b.Title+".epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
	}
	// Author scope covers both env.books.
	moves, err := env.s.PreviewReorganizeAuthor(ctx, b1.AuthorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 {
		t.Fatalf("author scope: want 2 moves, got %d", len(moves))
	}
	// Library scope also covers both.
	libMoves, err := env.s.PreviewReorganizeLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libMoves) != 2 {
		t.Fatalf("library scope: want 2 moves, got %d", len(libMoves))
	}
}
