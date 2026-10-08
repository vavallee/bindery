package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// secondBook adds another book by the same author for the ownership tests.
func secondBook(t *testing.T, database *sql.DB, authorID int64, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: "OL-" + strings.ReplaceAll(title, " ", "-"), AuthorID: authorID,
		Title: title, SortTitle: title, Monitored: true,
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
	}
	if err := NewBookRepo(database).Create(context.Background(), b); err != nil {
		t.Fatalf("create book %q: %v", title, err)
	}
	return b
}

// orphanBookRows deletes a book with foreign_keys off so its book_files rows
// survive, the state a lost pragma left behind (#1727).
func orphanBookRows(t *testing.T, database *sql.DB, bookID int64) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{"PRAGMA foreign_keys=OFF", "DELETE FROM books WHERE id = ?", "PRAGMA foreign_keys=ON"} {
		var err error
		if strings.Contains(q, "?") {
			_, err = database.ExecContext(ctx, q, bookID)
		} else {
			_, err = database.ExecContext(ctx, q)
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// TestBookRepo_AddBookFile_PathOwnedByOtherBook is #2937: the path is unique
// and the insert was OR IGNORE, so adding a path another book tracks recorded
// nothing and returned nil. It must now fail and name the owner.
func TestBookRepo_AddBookFile_PathOwnedByOtherBook(t *testing.T) {
	database, author, owner := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	other := secondBook(t, database, author.ID, "Volume Three")
	const path = "/lib/vol3.epub"

	if err := repo.AddBookFile(ctx, owner.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatalf("owner AddBookFile: %v", err)
	}
	err := repo.AddBookFile(ctx, other.ID, models.MediaTypeEbook, path)
	if err == nil {
		t.Fatal("AddBookFile onto a path another book tracks returned nil and recorded nothing")
	}
	for _, want := range []string{owner.Title, fmt.Sprintf("id %d", owner.ID)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the owning book (%s)", err, want)
		}
	}
	if files, _ := repo.ListFiles(ctx, other.ID); len(files) != 0 {
		t.Errorf("other book gained files %+v", files)
	}
	if files, _ := repo.ListFiles(ctx, owner.ID); len(files) != 1 || files[0].Path != path {
		t.Errorf("owner's row changed: %+v", files)
	}
}

// TestBookRepo_AddBookFile_SameBookReAdd: re-adding a path the same book
// already tracks stays an idempotent success, which retries and rescans rely on.
func TestBookRepo_AddBookFile_SameBookReAdd(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	for i := 0; i < 2; i++ {
		if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/same.epub"); err != nil {
			t.Fatalf("AddBookFile #%d: %v", i+1, err)
		}
	}
	if files, _ := repo.ListFiles(ctx, book.ID); len(files) != 1 {
		t.Errorf("want 1 row after a same-book re-add, got %+v", files)
	}
	got, _ := repo.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusImported {
		t.Errorf("status = %q, want imported", got.Status)
	}
}

// TestBookRepo_AddBookFile_OrphanedOwnerRepointed: a row whose book no longer
// exists is stale, so the add takes it over instead of failing.
func TestBookRepo_AddBookFile_OrphanedOwnerRepointed(t *testing.T) {
	database, author, gone := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	live := secondBook(t, database, author.ID, "Volume Three")
	const path = "/lib/vol3.epub"

	if err := repo.AddBookFile(ctx, gone.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatalf("seed AddBookFile: %v", err)
	}
	orphanBookRows(t, database, gone.ID)

	if err := repo.AddBookFile(ctx, live.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatalf("AddBookFile over an orphaned row: %v", err)
	}
	files, _ := repo.ListFiles(ctx, live.ID)
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("live book files = %+v, want the re-pointed row", files)
	}
	got, _ := repo.GetByID(ctx, live.ID)
	if got.Status != models.BookStatusImported {
		t.Errorf("status = %q, want imported", got.Status)
	}
}

// TestBookFileRepo_TrackOutcomes pins what Track reports for each case, so a
// caller can tell an insert from a same-book no-op from an orphan takeover.
func TestBookFileRepo_TrackOutcomes(t *testing.T) {
	database, author, book := openTestDB(t)
	ctx := context.Background()
	files := NewBookFileRepo(database)
	other := secondBook(t, database, author.ID, "Other")

	res, err := files.Track(ctx, book.ID, models.MediaTypeEbook, "/lib/a.epub")
	if err != nil || res.Outcome != TrackInserted || !res.Created() {
		t.Fatalf("first Track = %+v, %v; want inserted", res, err)
	}
	res, err = files.Track(ctx, book.ID, models.MediaTypeEbook, "/lib/a.epub")
	if err != nil || res.Outcome != TrackAlreadyTracked || res.Created() {
		t.Fatalf("same-book Track = %+v, %v; want already tracked", res, err)
	}
	_, err = files.Track(ctx, other.ID, models.MediaTypeEbook, "/lib/a.epub")
	var owned *PathOwnedError
	if !errors.As(err, &owned) || owned.OwnerBookID != book.ID || owned.OwnerTitle != book.Title {
		t.Fatalf("cross-book Track err = %v, want *PathOwnedError naming %d %q", err, book.ID, book.Title)
	}
	orphanBookRows(t, database, book.ID)
	res, err = files.Track(ctx, other.ID, models.MediaTypeEbook, "/lib/a.epub")
	if err != nil || res.Outcome != TrackReclaimedOrphan || res.PreviousBookID != book.ID {
		t.Fatalf("orphan Track = %+v, %v; want reclaimed from %d", res, err, book.ID)
	}
}

// TestBookRepo_MoveBookFile moves a row between two live books (Fix match)
// and refreshes both statuses: the source goes back to Wanted, the target
// becomes Imported.
func TestBookRepo_MoveBookFile(t *testing.T) {
	database, author, from := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	to := secondBook(t, database, author.ID, "Volume Three")
	const path = "/lib/vol3.epub"
	if err := repo.AddBookFile(ctx, from.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}

	prev, err := repo.MoveBookFile(ctx, path, to.ID, models.MediaTypeEbook)
	if err != nil {
		t.Fatalf("MoveBookFile: %v", err)
	}
	if prev != from.ID {
		t.Errorf("previous owner = %d, want %d", prev, from.ID)
	}
	if files, _ := repo.ListFiles(ctx, to.ID); len(files) != 1 || files[0].Path != path {
		t.Errorf("target files = %+v", files)
	}
	if files, _ := repo.ListFiles(ctx, from.ID); len(files) != 0 {
		t.Errorf("source still has %+v", files)
	}
	if got, _ := repo.GetByID(ctx, from.ID); got.Status != models.BookStatusWanted {
		t.Errorf("source status = %q, want wanted", got.Status)
	}
	if got, _ := repo.GetByID(ctx, to.ID); got.Status != models.BookStatusImported {
		t.Errorf("target status = %q, want imported", got.Status)
	}

	// Moving onto the book that already owns it, and onto an untracked
	// path, both succeed.
	if prev, err := repo.MoveBookFile(ctx, path, to.ID, models.MediaTypeEbook); err != nil || prev != to.ID {
		t.Errorf("same-book move = %d, %v", prev, err)
	}
	if prev, err := repo.MoveBookFile(ctx, "/lib/new.epub", to.ID, models.MediaTypeEbook); err != nil || prev != 0 {
		t.Errorf("untracked move = %d, %v", prev, err)
	}
}

// TestBookFileRepo_PathOwnedVariantsOnAnOrphanRow pins the split: a row left by
// a deleted book still counts as owned for the delete guards (refusing to
// unlink is the safe answer), and does not for the pre-checks of a write that
// Track would let take the row over (adoption, the existing file bind).
func TestBookFileRepo_PathOwnedVariantsOnAnOrphanRow(t *testing.T) {
	database, author, gone := openTestDB(t)
	ctx := context.Background()
	files := NewBookFileRepo(database)
	live := secondBook(t, database, author.ID, "Live")
	other := secondBook(t, database, author.ID, "Other")
	const orphanPath, livePath = "/lib/orphan.epub", "/lib/live.epub"

	if err := files.Add(ctx, gone.ID, models.MediaTypeEbook, orphanPath); err != nil {
		t.Fatal(err)
	}
	if err := files.Add(ctx, other.ID, models.MediaTypeEbook, livePath); err != nil {
		t.Fatal(err)
	}
	orphanBookRows(t, database, gone.ID)

	for _, c := range []struct {
		name            string
		path            string
		exclude         int64
		wantAny, wantLv bool
	}{
		{"orphan row", orphanPath, live.ID, true, false},
		{"orphan row, exclude none", orphanPath, 0, true, false},
		{"live other owner", livePath, live.ID, true, true},
		{"own row", livePath, other.ID, false, false},
		{"untracked", "/lib/none.epub", live.ID, false, false},
	} {
		gotAny, err := files.PathOwnedByOtherBook(ctx, c.path, c.exclude)
		if err != nil {
			t.Fatal(err)
		}
		gotLv, err := files.PathOwnedByLiveOtherBook(ctx, c.path, c.exclude)
		if err != nil {
			t.Fatal(err)
		}
		if gotAny != c.wantAny || gotLv != c.wantLv {
			t.Errorf("%s: PathOwnedByOtherBook = %v, PathOwnedByLiveOtherBook = %v; want %v, %v",
				c.name, gotAny, gotLv, c.wantAny, c.wantLv)
		}
	}
}
