package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #3556: a folder one book tracks as its audiobook can hold a file another
// book tracks (the second book's ebook landed there before #3570, or two books
// rendering to one folder share it). Reorganize used to move the folder whole
// and carry that file away, leaving the other book's row on a path that no
// longer exists. It now refuses the folder move and says why.
func TestReorganize_FolderHoldingAnotherBooksFileIsNotMoved(t *testing.T) {
	env, _, audiobookDir, ctx := reorgFixture(t)
	a := env.seed(t, ctx, "Jane Doe", "Shared Title")
	// The same work from a second provider: another author row, same name.
	second := &models.Author{ForeignID: "HC-Jane Doe", Name: "Jane Doe", SortName: "Jane Doe", MetadataProvider: "hardcover"}
	if err := env.authors.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "HC-Shared Title", AuthorID: second.ID, Title: "Shared Title",
		Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}
	if err := env.books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	folder := filepath.Join(audiobookDir, "Jane Doe", "Shared Title (2020)")
	writeFileAt(t, filepath.Join(folder, "part1.mp3"))
	if err := env.books.AddBookFile(ctx, a.ID, models.MediaTypeAudiobook, folder); err != nil {
		t.Fatal(err)
	}
	ebook := filepath.Join(folder, "Shared Title - Jane Doe.epub")
	writeFileAt(t, ebook)
	if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, ebook); err != nil {
		t.Fatal(err)
	}

	// A metadata refresh retitles A, so its folder now renders elsewhere.
	a.Title = "Shared Title Revised"
	if err := env.books.Update(ctx, a); err != nil {
		t.Fatal(err)
	}

	moves, err := env.s.PreviewReorganizeBook(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 {
		t.Fatalf("got %d moves, want 1", len(moves))
	}
	if moves[0].Status != ReorgStatusError || !strings.Contains(moves[0].Message, "another book tracks") {
		t.Fatalf("preview = %+v, want an error explaining the folder holds another book's file", moves[0])
	}
	if strings.Contains(moves[0].Message, b.Title) || strings.Contains(moves[0].Message, ebook) {
		t.Errorf("message %q names the other book's title or file", moves[0].Message)
	}

	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if len(results) != 1 || results[0].Status != ReorgStatusError {
		t.Fatalf("apply = %+v, want the error reported and nothing moved", results)
	}
	if _, err := os.Stat(ebook); err != nil {
		t.Errorf("the other book's ebook was carried off: %v", err)
	}
	if files, _ := env.books.ListBookFiles(ctx, a.ID); len(files) != 1 || files[0].Path != folder {
		t.Errorf("A's row = %+v, want it left at %s", files, folder)
	}
}

// The folder's own book may keep its ebook in it (the shared folder layout,
// #1959), and a row left by a deleted book does not hold the folder in place.
func TestReorganize_FolderHoldingOwnOrDeletedBooksFileStillMoves(t *testing.T) {
	for _, tc := range []string{"own ebook", "deleted book"} {
		t.Run(tc, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			ctx := context.Background()
			audiobookDir := t.TempDir()
			books := db.NewBookRepo(database)
			authors := db.NewAuthorRepo(database)
			s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
				books, authors, db.NewHistoryRepo(database), t.TempDir(), audiobookDir, "", "", "")
			s.WithSettings(db.NewSettingsRepo(database))
			s.WithRootFolders(db.NewRootFolderRepo(database))
			s.WithSeriesRepo(db.NewSeriesRepo(database))
			env := reorgEnv{s: s, books: books, authors: authors}
			a := env.seed(t, ctx, "Jane Doe", "Shared Title")
			folder := filepath.Join(audiobookDir, "unsorted", "Shared Title audio")
			writeFileAt(t, filepath.Join(folder, "part1.mp3"))
			if err := env.books.AddBookFile(ctx, a.ID, models.MediaTypeAudiobook, folder); err != nil {
				t.Fatal(err)
			}
			inside := filepath.Join(folder, "Shared Title.epub")
			writeFileAt(t, inside)
			owner := a
			if tc == "deleted book" {
				owner = env.seed(t, ctx, "Someone Else", "Gone")
			}
			if err := env.books.AddBookFile(ctx, owner.ID, models.MediaTypeEbook, inside); err != nil {
				t.Fatal(err)
			}
			if tc == "deleted book" {
				orphanBook(t, database, owner.ID)
			}
			moves, err := env.s.PreviewReorganizeBook(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range moves {
				if m.Format == models.MediaTypeAudiobook && m.Status != ReorgStatusMove {
					t.Errorf("audiobook folder status = %q (%s), want move", m.Status, m.Message)
				}
			}
		})
	}
}

// #3556, import side: since #3560 two books that render to one ebook name both
// land in the same folder ("Name.epub" and "Name (2).epub"). When the first
// book's audiobook then arrives, the shared folder merge (#1959) used to take
// over that folder as the audiobook's, so the second book's ebook sat inside a
// folder the first book tracks, ready to be carried off by a Reorganize. The
// audiobook now gets a folder of its own instead.
func TestImport_AudiobookDoesNotMergeIntoFolderHoldingAnotherBooksEbook(t *testing.T) {
	for _, single := range []bool{false, true} {
		name := "folder source"
		if single {
			name = "single file source"
		}
		t.Run(name, func(t *testing.T) {
			s, book, dlRepo, bookRepo, ctx, ebookDir := importSharedEbook(t, "copy")

			other := &models.Book{ForeignID: "OL-3556-OTHER", AuthorID: book.AuthorID, Title: book.Title,
				Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}
			if err := bookRepo.Create(ctx, other); err != nil {
				t.Fatal(err)
			}
			otherEbook := filepath.Join(ebookDir, "We Who Wrestle with God - Jordan B. Peterson (2).epub")
			if err := os.WriteFile(otherEbook, bookSized("other"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := bookRepo.AddBookFile(ctx, other.ID, models.MediaTypeEbook, otherEbook); err != nil {
				t.Fatal(err)
			}

			src := t.TempDir()
			audio := filepath.Join(src, "book.m4b")
			if err := os.WriteFile(audio, bookSized("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			importPath := src
			if single {
				importPath = audio
			}
			dl := &models.Download{GUID: "guid-3556-audio-" + name, Title: book.Title + " [M4B]",
				BookID: &book.ID, Status: models.StateCompleted, Quality: "m4b"}
			if err := dlRepo.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}
			s.ImportFromPath(ctx, dl, importPath, "")

			got, err := dlRepo.GetByGUID(ctx, dl.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != models.StateImported {
				t.Fatalf("audiobook import status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			audioRow := audiobookRowFor(t, bookRepo, ctx, book.ID)
			// An audiobook row stores its folder, for a single file source too.
			if filepath.Clean(audioRow) == filepath.Clean(ebookDir) {
				t.Errorf("audiobook merged into %s, which holds another book's ebook; want a folder of its own", ebookDir)
			}
			if _, err := os.Stat(otherEbook); err != nil {
				t.Errorf("other book's ebook disturbed: %v", err)
			}
		})
	}
}

func audiobookRowFor(t *testing.T, books *db.BookRepo, ctx context.Context, bookID int64) string {
	t.Helper()
	files, err := books.ListFiles(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Format == models.MediaTypeAudiobook {
			return f.Path
		}
	}
	t.Fatal("no audiobook row recorded")
	return ""
}

// #3556, the Fix match sibling: relocating an audiobook folder that already
// sits in the library (Fix match, or a manual import pointed inside the
// library) places that folder whole. When another book tracks a file inside
// it, the import is refused instead of carrying that file along, matching what
// the destination preview and Reorganize report.
func TestImport_AudiobookSourceFolderHoldingAnotherBooksFileIsRefused(t *testing.T) {
	for _, mode := range []string{"copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			s, book, dlRepo, bookRepo, settingsRepo, _, ctx := sharedFormatFixture(t, t.TempDir())
			if err := settingsRepo.Set(ctx, "import.mode", mode); err != nil {
				t.Fatal(err)
			}
			other := &models.Book{ForeignID: "OL-3556-HELD", AuthorID: book.AuthorID, Title: "Held Elsewhere",
				Status: models.BookStatusImported, MediaType: models.MediaTypeEbook}
			if err := bookRepo.Create(ctx, other); err != nil {
				t.Fatal(err)
			}
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "book.m4b"), bookSized("m4b-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			held := filepath.Join(src, "Held Elsewhere.epub")
			if err := os.WriteFile(held, bookSized("held"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := bookRepo.AddBookFile(ctx, other.ID, models.MediaTypeEbook, held); err != nil {
				t.Fatal(err)
			}
			dl := &models.Download{GUID: "guid-3556-src-" + mode, Title: book.Title + " [M4B]",
				BookID: &book.ID, Status: models.StateCompleted, Quality: "m4b"}
			if err := dlRepo.Create(ctx, dl); err != nil {
				t.Fatal(err)
			}
			s.ImportFromPath(ctx, dl, src, models.MediaTypeAudiobook)

			got, err := dlRepo.GetByGUID(ctx, dl.GUID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != models.StateImportBlocked || !strings.Contains(got.ErrorMessage, "another book tracks") {
				t.Fatalf("status = %q (%s), want blocked with the reason", got.Status, got.ErrorMessage)
			}
			if strings.Contains(got.ErrorMessage, other.Title) {
				t.Errorf("reason %q names the other book", got.ErrorMessage)
			}
			if _, err := os.Stat(held); err != nil {
				t.Errorf("other book's file moved: %v", err)
			}
			if files, _ := bookRepo.ListFiles(ctx, book.ID); len(files) != 0 {
				t.Errorf("book got rows %+v, want none", files)
			}
		})
	}
}
