package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// keepingLocksSnapshot returns a book as a caller would hold it after reading
// it, with the updated_at its guarded write compares against.
func keepingLocksSnapshot(t *testing.T) (*sql.DB, *BookRepo, *models.Book) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-KEEPING-LOCKS-A")
	book := mkBook(t, books, ctx, author.ID, "OL-KEEPING-LOCKS-W", "Pride and Prejudice", models.BookStatusWanted)
	snapshot, err := books.GetByID(ctx, book.ID)
	if err != nil || snapshot == nil || snapshot.UpdatedAtRaw == "" {
		t.Fatalf("snapshot: %v", err)
	}
	return database, books, snapshot
}

// A lock set between the caller's read and its write is kept: the write
// misses, the book is reloaded, and apply runs again against the new lock.
func TestBookRepoUpdateKeepingLocks_KeepsLockSetAfterRead(t *testing.T) {
	_, books, stale := keepingLocksSnapshot(t)
	ctx := context.Background()
	fresh, _ := books.GetByID(ctx, stale.ID)
	fresh.Narrator = "Ada Example"
	fresh.LockField(models.BookFieldNarrator)
	if err := books.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	applied := 0
	err := books.UpdateKeepingLocks(ctx, stale, func(b *models.Book) {
		applied++
		if b.CanWrite(models.BookFieldNarrator) {
			b.Narrator = "Import Narrator"
		}
		b.DurationSeconds = 600
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := books.GetByID(ctx, stale.ID)
	if got.Narrator != "Ada Example" || !got.IsFieldLocked(models.BookFieldNarrator) {
		t.Fatalf("the stale snapshot undid the lock: narrator=%q locked=%v", got.Narrator, got.LockedFields)
	}
	if got.DurationSeconds != 600 || applied != 2 {
		t.Fatalf("retry did not land: duration=%d applied=%d", got.DurationSeconds, applied)
	}
}

// After three missed writes it gives up with ErrBookKeptChanging and writes
// nothing.
func TestBookRepoUpdateKeepingLocks_GivesUpAfterThreeMisses(t *testing.T) {
	database, books, stale := keepingLocksSnapshot(t)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER keep_changing BEFORE UPDATE ON books BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}

	applied := 0
	err := books.UpdateKeepingLocks(ctx, stale, func(b *models.Book) {
		applied++
		b.DurationSeconds = 600
	})
	if !errors.Is(err, ErrBookKeptChanging) {
		t.Fatalf("expected ErrBookKeptChanging, got %v", err)
	}
	if applied != 3 {
		t.Fatalf("apply ran %d times, want 3", applied)
	}
	got, _ := books.GetByID(ctx, stale.ID)
	if got.DurationSeconds != 0 {
		t.Fatalf("a write landed although every one was swallowed: duration=%d", got.DurationSeconds)
	}
}

// A book with no updated_at to compare against is written directly.
func TestBookRepoUpdateKeepingLocks_WritesDirectlyWithoutUpdatedAt(t *testing.T) {
	_, books, book := keepingLocksSnapshot(t)
	ctx := context.Background()
	book.UpdatedAtRaw = ""

	if err := books.UpdateKeepingLocks(ctx, book, func(b *models.Book) { b.DurationSeconds = 600 }); err != nil {
		t.Fatal(err)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.DurationSeconds != 600 {
		t.Fatalf("direct write did not land: duration=%d", got.DurationSeconds)
	}
}
