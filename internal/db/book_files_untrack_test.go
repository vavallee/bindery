package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// The Calibre importer needs to know whether it was the caller that inserted a
// book_files row, because rollback may only untrack rows the run created
// (#1635). These exercise AddBookFileIfMissing and UntrackFilePath directly at
// the repository level; the importer side is covered in internal/calibre.

func TestAddBookFileIfMissing_ReportsWhoInserted(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	created, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/claimed.epub")
	if err != nil {
		t.Fatalf("first AddBookFileIfMissing: %v", err)
	}
	if !created {
		t.Fatal("first insert should report created=true")
	}

	created, err = repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/claimed.epub")
	if err != nil {
		t.Fatalf("second AddBookFileIfMissing: %v", err)
	}
	if created {
		t.Error("second insert of the same path should report created=false")
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("want 1 row after two calls, got %d", len(files))
	}
}

func TestAddBookFileIfMissing_DoesNotStealAnotherBooksPath(t *testing.T) {
	database, author, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	other := &models.Book{
		ForeignID: "OL88888W",
		AuthorID:  author.ID,
		Title:     "Other Book",
		SortTitle: "Other Book",
		Monitored: true,
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeEbook,
	}
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("create other book: %v", err)
	}

	if _, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/shared.epub"); err != nil {
		t.Fatalf("seed AddBookFileIfMissing: %v", err)
	}

	created, err := repo.AddBookFileIfMissing(ctx, other.ID, models.MediaTypeEbook, "/lib/shared.epub")
	if err != nil {
		t.Fatalf("second book AddBookFileIfMissing: %v", err)
	}
	if created {
		t.Error("claiming a path another book owns should report created=false")
	}

	files, err := repo.ListFiles(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListFiles for other book: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("path should stay with the original book, got %d rows on the second book", len(files))
	}
}

func TestAddBookFileIfMissing_RefreshesStatus(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if _, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/status.epub"); err != nil {
		t.Fatalf("AddBookFileIfMissing: %v", err)
	}

	got, err := repo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.BookStatusImported {
		t.Errorf("want status %q after the file is tracked, got %q", models.BookStatusImported, got.Status)
	}
}

func TestUntrackFilePath_RemovesTheRowAndRefreshesStatus(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if _, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/gone.epub"); err != nil {
		t.Fatalf("AddBookFileIfMissing: %v", err)
	}

	owner, err := repo.UntrackFilePath(ctx, "/lib/gone.epub")
	if err != nil {
		t.Fatalf("UntrackFilePath: %v", err)
	}
	if owner != book.ID {
		t.Errorf("want owning book %d, got %d", book.ID, owner)
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("want the row gone, got %d", len(files))
	}

	got, err := repo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("want status %q once nothing is tracked, got %q", models.BookStatusWanted, got.Status)
	}
	if got.EbookFilePath != "" {
		t.Errorf("want no effective ebook path, got %q", got.EbookFilePath)
	}
}

func TestUntrackFilePath_UnknownPathIsANoOp(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if _, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/keep.epub"); err != nil {
		t.Fatalf("AddBookFileIfMissing: %v", err)
	}

	owner, err := repo.UntrackFilePath(ctx, "/lib/never-tracked.epub")
	if err != nil {
		t.Fatalf("UntrackFilePath on an unknown path: %v", err)
	}
	if owner != 0 {
		t.Errorf("want owning book 0 for an unknown path, got %d", owner)
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("the tracked row should be untouched, got %d rows", len(files))
	}
}

// The Calibre rollback runs every entity in one transaction, and BookRepo.WithTx
// clones only exec. A version of UntrackFilePath that read through the bare pool
// deadlocked here rather than failing, because MaxOpenConns is 1, so this guards
// against reintroducing a pool read on the path (#1635).
func TestUntrackFilePath_WorksInsideATransaction(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if _, err := repo.AddBookFileIfMissing(ctx, book.ID, models.MediaTypeEbook, "/lib/tx.epub"); err != nil {
		t.Fatalf("AddBookFileIfMissing: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback()
		if _, err := repo.WithTx(tx).UntrackFilePath(ctx, "/lib/tx.epub"); err != nil {
			done <- err
			return
		}
		done <- tx.Commit()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UntrackFilePath inside a transaction: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("UntrackFilePath deadlocked inside a transaction")
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("want the row gone after commit, got %d", len(files))
	}
}
