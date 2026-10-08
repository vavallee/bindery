package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// refreshRaceFixture creates one wanted ebook and a real file for it, so
// derivedFormatPath sees the path resolve.
func refreshRaceFixture(t *testing.T) (context.Context, *BookRepo, *models.Book, string) {
	t.Helper()
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	author := &models.Author{ForeignID: "OL-2375-A", Name: "Race Author", SortName: "Author, Race", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-2375-W", AuthorID: author.ID, Title: "Race Book", SortTitle: "Race Book",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
		Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Race Book.epub")
	if err := os.WriteFile(path, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ctx, books, book, path
}

// withRefreshWindow runs edit once, inside refreshBookStatus's read then write
// window, the way a user PUT racing an import lands (#2375).
func withRefreshWindow(t *testing.T, edit func()) {
	t.Helper()
	fired := false
	refreshBookStatusLoaded = func(int64) {
		if fired {
			return
		}
		fired = true
		edit()
	}
	t.Cleanup(func() { refreshBookStatusLoaded = nil })
}

// TestRefreshBookStatus_KeepsConcurrentEdit is the reproduction #2375 asked
// for: the user unmonitors and renames a book while an import is recording its
// file. refreshBookStatus used to write the whole row back from the snapshot it
// read before the edit, so the book came back monitored under its old title.
func TestRefreshBookStatus_KeepsConcurrentEdit(t *testing.T) {
	ctx, books, book, path := refreshRaceFixture(t)

	withRefreshWindow(t, func() {
		current, err := books.GetByID(ctx, book.ID)
		if err != nil || current == nil {
			t.Fatalf("load for concurrent edit: %v", err)
		}
		current.Monitored = false
		current.Title = "Race Book (edited)"
		if err := books.Update(ctx, current); err != nil {
			t.Fatalf("concurrent edit: %v", err)
		}
	})

	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Monitored {
		t.Error("monitored = true: the concurrent unmonitor was overwritten by the status refresh")
	}
	if got.Title != "Race Book (edited)" {
		t.Errorf("title = %q: the concurrent rename was overwritten by the status refresh", got.Title)
	}
	if got.Status != models.BookStatusImported || got.EbookFilePath != path || got.FilePath != path {
		t.Errorf("status refresh did not land: status=%q ebook=%q file=%q", got.Status, got.EbookFilePath, got.FilePath)
	}
}

// TestRefreshBookStatus_RederivesWhenInputsChange covers the columns the
// refresh derives from. A concurrent switch to "both" must survive, and the
// status must be worked out against it: an ebook alone does not complete a
// book that also wants an audiobook.
func TestRefreshBookStatus_RederivesWhenInputsChange(t *testing.T) {
	ctx, books, book, path := refreshRaceFixture(t)

	withRefreshWindow(t, func() {
		current, err := books.GetByID(ctx, book.ID)
		if err != nil || current == nil {
			t.Fatalf("load for concurrent edit: %v", err)
		}
		current.MediaType = models.MediaTypeBoth
		if err := books.Update(ctx, current); err != nil {
			t.Fatalf("concurrent edit: %v", err)
		}
	})

	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Errorf("media_type = %q, want %q: the concurrent edit was overwritten", got.MediaType, models.MediaTypeBoth)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want %q: an ebook alone does not complete a book that wants both formats", got.Status, models.BookStatusWanted)
	}
	if got.EbookFilePath != path {
		t.Errorf("ebook path = %q, want %q", got.EbookFilePath, path)
	}
}
