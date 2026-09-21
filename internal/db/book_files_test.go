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

// TestBookFileRepo_ListByBooks verifies the batch lookup groups rows by
// book_id in one query (#2480: replaces an N+1 ListByBook-per-book call in
// the manual-import scan's confident-match format check).
func TestBookFileRepo_ListByBooks(t *testing.T) {
	database, author, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	files := NewBookFileRepo(database)

	_ = repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/one.epub")

	b2 := &models.Book{
		ForeignID: "OL-LB-B2", AuthorID: author.ID,
		Title: "Second Book", SortTitle: "Second Book",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := repo.Create(ctx, b2); err != nil {
		t.Fatalf("create b2: %v", err)
	}
	_ = repo.AddBookFile(ctx, b2.ID, models.MediaTypeEbook, "/lib/two.epub")
	_ = repo.AddBookFile(ctx, b2.ID, models.MediaTypeAudiobook, "/lib/two.m4b")

	// A third book with no files at all should simply be absent from the map.
	b3 := &models.Book{
		ForeignID: "OL-LB-B3", AuthorID: author.ID,
		Title: "Third Book", SortTitle: "Third Book",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := repo.Create(ctx, b3); err != nil {
		t.Fatalf("create b3: %v", err)
	}

	got, err := files.ListByBooks(ctx, []int64{book.ID, b2.ID, b3.ID})
	if err != nil {
		t.Fatalf("ListByBooks: %v", err)
	}
	if len(got[book.ID]) != 1 || got[book.ID][0].Path != "/lib/one.epub" {
		t.Errorf("book1 files = %+v, want just one.epub", got[book.ID])
	}
	if len(got[b2.ID]) != 2 {
		t.Errorf("book2 files = %+v, want 2 rows", got[b2.ID])
	}
	if _, ok := got[b3.ID]; ok {
		t.Errorf("book3 has no files and should be absent from the map, got %+v", got[b3.ID])
	}

	empty, err := files.ListByBooks(ctx, nil)
	if err != nil {
		t.Fatalf("ListByBooks(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListByBooks(nil) = %+v, want empty map", empty)
	}
}

// TestBookFileRepo_Fingerprint verifies the (count, maxID) snapshot changes on
// every mutating method, is stable across pure reads, and — the point of
// reading it from the table rather than an in-process counter (#2480 review)
// — also changes when a row disappears through a path that never goes through
// BookFileRepo at all, such as the books(id) ON DELETE CASCADE FK a book
// delete triggers.
func TestBookFileRepo_Fingerprint(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	files := NewBookFileRepo(database)
	books := NewBookRepo(database)

	count0, maxID0, err := files.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	if err := files.Add(ctx, book.ID, models.MediaTypeEbook, "/lib/v.epub"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	count1, maxID1, err := files.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if count1 == count0 && maxID1 == maxID0 {
		t.Errorf("Fingerprint did not change after Add: (%d, %d)", count1, maxID1)
	}

	if _, err := files.ListByBook(ctx, book.ID); err != nil {
		t.Fatalf("ListByBook: %v", err)
	}
	if count2, maxID2, err := files.Fingerprint(ctx); err != nil {
		t.Fatalf("Fingerprint: %v", err)
	} else if count2 != count1 || maxID2 != maxID1 {
		t.Errorf("Fingerprint changed on a pure read: (%d, %d) -> (%d, %d)", count1, maxID1, count2, maxID2)
	}

	if _, err := files.DeleteByPath(ctx, "/lib/v.epub"); err != nil {
		t.Fatalf("DeleteByPath: %v", err)
	}
	count3, maxID3, err := files.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if count3 == count1 && maxID3 == maxID1 {
		t.Errorf("Fingerprint did not change after DeleteByPath: (%d, %d)", count3, maxID3)
	}

	// A book delete removes book_files rows via ON DELETE CASCADE — not
	// through any BookFileRepo method — which is exactly the path an
	// in-process mutation counter cannot see (#2480 review repro: seed a
	// tracked file, delete the book keeping the file, and the cache stayed
	// stale). Reading the fingerprint from the table itself must still catch it.
	if err := files.Add(ctx, book.ID, models.MediaTypeEbook, "/lib/v2.epub"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	count4, maxID4, err := files.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatalf("Delete book: %v", err)
	}
	count5, maxID5, err := files.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if count5 == count4 && maxID5 == maxID4 {
		t.Errorf("Fingerprint did not change after the owning book was deleted (FK cascade): (%d, %d)", count5, maxID5)
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
