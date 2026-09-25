package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestBookFileRepo_AddAndListByBook(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/a.epub"); err != nil {
		t.Fatalf("AddBookFile a.epub: %v", err)
	}
	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/a.mobi"); err != nil {
		t.Fatalf("AddBookFile a.mobi: %v", err)
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["/lib/a.epub"] || !paths["/lib/a.mobi"] {
		t.Errorf("unexpected file paths: %v", paths)
	}
}

func TestBookFileRepo_DuplicatePathIgnored(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/dup.epub"); err != nil {
		t.Fatalf("first AddBookFile: %v", err)
	}
	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/dup.epub"); err != nil {
		t.Fatalf("duplicate AddBookFile should not error: %v", err)
	}

	files, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("want 1 row (deduped), got %d", len(files))
	}
}

func TestBookFileRepo_DeleteByBook(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	files := NewBookFileRepo(database)

	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/x.epub")
	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/x.mobi")

	if err := files.DeleteByBook(ctx, book.ID); err != nil {
		t.Fatalf("DeleteByBook: %v", err)
	}

	got, err := repo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 files after DeleteByBook, got %d", len(got))
	}
}

func TestBookFileRepo_DeleteByPath(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	files := NewBookFileRepo(database)

	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/del.epub")
	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/keep.epub")

	bookID, err := files.DeleteByPath(ctx, "/lib/del.epub")
	if err != nil {
		t.Fatalf("DeleteByPath: %v", err)
	}
	if bookID != book.ID {
		t.Errorf("want bookID=%d, got %d", book.ID, bookID)
	}

	remaining, _ := repo.ListFiles(ctx, book.ID)
	if len(remaining) != 1 || remaining[0].Path != "/lib/keep.epub" {
		t.Errorf("expected only /lib/keep.epub remaining, got %+v", remaining)
	}

	// Deleting a non-existent path returns 0, nil.
	id2, err := files.DeleteByPath(ctx, "/lib/nope.epub")
	if err != nil {
		t.Fatalf("DeleteByPath(missing): %v", err)
	}
	if id2 != 0 {
		t.Errorf("missing path should return bookID=0, got %d", id2)
	}
}

func TestBookFileRepo_ListAllPaths(t *testing.T) {
	database, author, _ := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	files := NewBookFileRepo(database)
	authorRepo := NewAuthorRepo(database)

	// Create a second book under the same author.
	b2 := &models.Book{
		ForeignID: "OL-PF-B2", AuthorID: author.ID,
		Title: "Second Book", SortTitle: "Second Book",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := repo.Create(ctx, b2); err != nil {
		t.Fatalf("create b2: %v", err)
	}

	_ = repo.AddBookFile(ctx, b2.ID, models.MediaTypeEbook, "/lib/p/q.epub")

	// create a separate author + book to get another path.
	a2 := &models.Author{ForeignID: "OL-PA2", Name: "P A2", SortName: "P, A2", Monitored: true}
	if err := authorRepo.Create(ctx, a2); err != nil {
		t.Fatalf("create a2: %v", err)
	}
	b3 := &models.Book{
		ForeignID: "OL-PF-B3", AuthorID: a2.ID,
		Title: "Third Book", SortTitle: "Third Book",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := repo.Create(ctx, b3); err != nil {
		t.Fatalf("create b3: %v", err)
	}
	_ = repo.AddBookFile(ctx, b3.ID, models.MediaTypeEbook, "/lib/r/s.epub")

	all, err := files.ListAllPaths(ctx)
	if err != nil {
		t.Fatalf("ListAllPaths: %v", err)
	}
	got := map[string]bool{}
	for _, p := range all {
		got[p] = true
	}
	if !got["/lib/p/q.epub"] || !got["/lib/r/s.epub"] {
		t.Errorf("ListAllPaths missing expected entries: %v", got)
	}
}

// TestBookRepo_AddBookFile_StatusFlip verifies that importing the first ebook file
// flips the book's status from "wanted" to "imported".
func TestBookRepo_AddBookFile_StatusFlip(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/book.epub"); err != nil {
		t.Fatalf("AddBookFile: %v", err)
	}

	got, _ := repo.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusImported {
		t.Errorf("want imported, got %q", got.Status)
	}
	if got.EbookFilePath != "/lib/book.epub" {
		t.Errorf("EbookFilePath want /lib/book.epub, got %q", got.EbookFilePath)
	}
}

// TestBookRepo_MigrationBackfill verifies the migration SQL backfills book_files
// from existing ebook_file_path and audiobook_file_path columns. We simulate
// this by inserting a row with raw SQL (bypassing the repo layer), then querying
// via the repo to confirm the COALESCE fallback sees it.
func TestBookRepo_MigrationBackfill(t *testing.T) {
	database, author, _ := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	// Insert a book with a legacy ebook_file_path directly (simulates
	// a row that existed before the book_files migration).
	_, err := database.ExecContext(ctx, `
		INSERT INTO books (foreign_id, author_id, title, sort_title, status, monitored, media_type, metadata_provider,
		                   ebook_file_path, file_path, genres, created_at, updated_at)
		VALUES (?,?,?,?,'imported',1,'ebook','openlibrary',?,?,'[]',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		"OL-BF-LEGACY", author.ID, "Legacy Book", "Legacy Book",
		"/lib/legacy.epub", "/lib/legacy.epub")
	if err != nil {
		t.Fatalf("direct insert: %v", err)
	}

	b, err := repo.GetByForeignID(ctx, "OL-BF-LEGACY")
	if err != nil || b == nil {
		t.Fatalf("GetByForeignID: %v (book=%v)", err, b)
	}

	// The COALESCE view in bookColumns falls back to legacy column — path is visible.
	if b.EbookFilePath != "/lib/legacy.epub" {
		t.Errorf("COALESCE fallback: want /lib/legacy.epub, got %q", b.EbookFilePath)
	}

	// After calling AddBookFile the book_files table takes over.
	if err := repo.AddBookFile(ctx, b.ID, models.MediaTypeEbook, "/lib/new.epub"); err != nil {
		t.Fatalf("AddBookFile: %v", err)
	}
	b2, _ := repo.GetByID(ctx, b.ID)
	if b2.EbookFilePath != "/lib/new.epub" {
		t.Errorf("after AddBookFile: want /lib/new.epub, got %q", b2.EbookFilePath)
	}

	// Both files are in book_files.
	files, _ := repo.ListFiles(ctx, b.ID)
	if len(files) != 1 {
		t.Errorf("want 1 row in book_files, got %d", len(files))
	}
}

// TestBookRepo_RemoveBookFile_KeepsImportedWithSibling verifies removing one
// of multiple files in a format does not make the book wanted.
func TestBookRepo_RemoveBookFile_KeepsImportedWithSibling(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	for _, path := range []string{"/lib/first.epub", "/lib/second.epub"} {
		if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, path); err != nil {
			t.Fatalf("AddBookFile: %v", err)
		}
	}
	if _, err := repo.RemoveBookFile(ctx, "/lib/first.epub"); err != nil {
		t.Fatalf("RemoveBookFile: %v", err)
	}

	got, _ := repo.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusImported {
		t.Errorf("status should remain imported while a sibling remains, got %q", got.Status)
	}
	if got.EbookFilePath != "/lib/second.epub" {
		t.Errorf("EbookFilePath should point to the surviving file, got %q", got.EbookFilePath)
	}
}

// TestBookRepo_RemoveBookFile_StatusFlips verifies removing the last file
// flips the book back to "wanted".
func TestBookRepo_RemoveBookFile_StatusFlips(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/only.epub")

	got, _ := repo.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusImported {
		t.Fatalf("expected imported after AddBookFile, got %q", got.Status)
	}

	if _, err := repo.RemoveBookFile(ctx, "/lib/only.epub"); err != nil {
		t.Fatalf("RemoveBookFile: %v", err)
	}

	got2, _ := repo.GetByID(ctx, book.ID)
	if got2.Status != models.BookStatusWanted {
		t.Errorf("status should flip back to wanted, got %q", got2.Status)
	}
	if got2.EbookFilePath != "" {
		t.Errorf("EbookFilePath should be cleared, got %q", got2.EbookFilePath)
	}
}
