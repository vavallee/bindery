package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type sharedRootReorgEnv struct {
	reorgEnv
	database *sql.DB
}

// reorgFixtureSharedRoot is reorgFixture with one root for ebooks and
// audiobooks (BINDERY_AUDIOBOOK_DIR unset, the default), where an ebook
// template and another book's audiobook folder can render to the same folder.
func reorgFixtureSharedRoot(t *testing.T) (env sharedRootReorgEnv, libraryDir, audiobookDir string, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx = context.Background()
	root := t.TempDir()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database), root, root, "", "", "")
	s.WithSettings(db.NewSettingsRepo(database))
	s.WithRootFolders(db.NewRootFolderRepo(database))
	s.WithSeriesRepo(db.NewSeriesRepo(database))
	return sharedRootReorgEnv{reorgEnv: reorgEnv{s: s, books: books, authors: authors}, database: database}, root, root, ctx
}

// #3599: an ebook retitled so its template renders into the folder another
// book tracks as its audiobook. Reorganize used to move the ebook there, where
// a later Reorganize of that folder carried it off while this book's row kept
// the old path. It now reports the destination as taken and leaves the file.
func TestReorganize_EbookIntoAnotherBooksFolderIsRefused(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixtureSharedRoot(t)
	author := env.seedAuthor(t, ctx, "Jane Doe")
	a := env.seedBook(t, ctx, author, "Shared Title")
	b := env.seedBook(t, ctx, author, "Other Title")

	folder := filepath.Join(libraryDir, "Jane Doe", "Shared Title (2020)")
	writeFileAt(t, filepath.Join(folder, "part1.mp3"))
	if err := env.books.AddBookFile(ctx, a.ID, models.MediaTypeAudiobook, folder); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(libraryDir, "Jane Doe", "Other Title (2020)", "Other Title - Jane Doe.epub")
	writeFileAt(t, cur)
	if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, cur); err != nil {
		t.Fatal(err)
	}
	// A metadata refresh retitles B onto A's title.
	b.Title = "Shared Title"
	if err := env.books.Update(ctx, b); err != nil {
		t.Fatal(err)
	}

	moves, err := env.s.PreviewReorganizeBook(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 {
		t.Fatalf("got %d moves, want 1", len(moves))
	}
	if moves[0].Status != ReorgStatusCollision || !strings.Contains(moves[0].Message, "inside a folder another book tracks") {
		t.Fatalf("preview = %+v, want a collision explaining the folder belongs to another book", moves[0])
	}
	if strings.Contains(moves[0].Message, folder) {
		t.Errorf("message %q names the other book's folder", moves[0].Message)
	}

	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if len(results) != 1 || results[0].Status != ReorgStatusCollision {
		t.Fatalf("apply = %+v, want the collision reported and nothing moved", results)
	}
	if _, err := os.Stat(cur); err != nil {
		t.Errorf("ebook left its place: %v", err)
	}
	if _, err := os.Stat(moves[0].Proposed); !os.IsNotExist(err) {
		t.Errorf("ebook was written into the other book's folder (stat err = %v)", err)
	}
}

// The book's own audiobook folder is where its ebook belongs in the shared
// folder layout (#1959), and a folder row left by a deleted book owns nothing.
// Neither blocks the move.
func TestReorganize_EbookIntoOwnOrDeletedBooksFolderMoves(t *testing.T) {
	for _, tc := range []string{"own folder", "deleted book"} {
		t.Run(tc, func(t *testing.T) {
			env, libraryDir, _, ctx := reorgFixtureSharedRoot(t)
			author := env.seedAuthor(t, ctx, "Jane Doe")
			b := env.seedBook(t, ctx, author, "Shared Title")
			folderOwner := b
			if tc == "deleted book" {
				folderOwner = env.seedBook(t, ctx, author, "Gone")
			}
			folder := filepath.Join(libraryDir, "Jane Doe", "Shared Title (2020)")
			writeFileAt(t, filepath.Join(folder, "part1.mp3"))
			if err := env.books.AddBookFile(ctx, folderOwner.ID, models.MediaTypeAudiobook, folder); err != nil {
				t.Fatal(err)
			}
			if tc == "deleted book" {
				orphanBook(t, env.database, folderOwner.ID)
			}
			cur := filepath.Join(libraryDir, "unsorted", "x.epub")
			writeFileAt(t, cur)
			if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, cur); err != nil {
				t.Fatal(err)
			}
			moves, err := env.s.PreviewReorganizeBook(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range moves {
				if m.Format == models.MediaTypeEbook && m.Status != ReorgStatusMove {
					t.Errorf("ebook status = %q (%s), want move", m.Status, m.Message)
				}
			}
		})
	}
}

// The Fix match destination preview runs through the same classifier. An
// ebook whose destination sits inside another book's folder is refused by the
// import (#3570), so the preview must not offer it as a clean move.
func TestPreviewImportDestination_EbookIntoAnotherBooksFolder(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixtureSharedRoot(t)
	author := env.seedAuthor(t, ctx, "Jane Doe")
	a := env.seedBook(t, ctx, author, "Shared Title")
	target := env.seedBook(t, ctx, author, "Shared Title ")
	target.Title = "Shared Title"
	if err := env.books.Update(ctx, target); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(libraryDir, "Jane Doe", "Shared Title (2020)")
	writeFileAt(t, filepath.Join(folder, "part1.mp3"))
	if err := env.books.AddBookFile(ctx, a.ID, models.MediaTypeAudiobook, folder); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(libraryDir, "elsewhere", "some.epub")
	writeFileAt(t, src)

	got, err := env.s.PreviewImportDestination(ctx, target.ID, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ReorgStatusCollision {
		t.Errorf("preview = %+v, want a collision", got)
	}
}
