package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// TestBackfillOLDuplicateTitles_StripsMarkerFromStoredTitles is the stored
// data half of the OpenLibrary "(Duplicate of OL...)" cleanup. Ingest now
// strips the merge queue note, but rows synced before that keep it until the
// startup pass rewrites them. A title the user locked is theirs and stays.
func TestBackfillOLDuplicateTitles_StripsMarkerFromStoredTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeyOLDuplicateTitle); !ok || got != strconv.Itoa(olDuplicateTitleRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, olDuplicateTitleRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-DUP-A", Name: "Pierce Brown", SortName: "Brown, Pierce"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	create := func(foreignID, title string, locked []string) int64 {
		t.Helper()
		b := &models.Book{ForeignID: foreignID, AuthorID: author.ID, Title: "placeholder", Status: models.BookStatusWanted}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		// Rewind with SQL: the point is a row written before ingest cleaned it.
		lockedJSON := "[]"
		if len(locked) > 0 {
			lockedJSON = `["` + locked[0] + `"]`
		}
		if _, err := database.Exec("UPDATE books SET title = ?, sort_title = ?, locked_fields = ? WHERE id = ?",
			title, title, lockedJSON, b.ID); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	dirty := create("OL-DUP-1", "Light Bringer (Duplicate of OL31211W)", nil)
	clean := create("OL-DUP-2", "Red Rising", nil)
	locked := create("OL-DUP-3", "Golden Son (Duplicate of OL26431102W)", []string{models.BookFieldTitle})
	database.Close()

	// A current marker means no scan, like the other backfills.
	database = openFileDB(t, path)
	if got := bookTitle(t, database, dirty); got != "Light Bringer (Duplicate of OL31211W)" {
		t.Errorf("title = %q after a second open, want it untouched while the marker is current", got)
	}
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyOLDuplicateTitle); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	var title, sortTitle, dedupKey, sortKey, searchKey string
	if err := database.QueryRow("SELECT title, sort_title, dedup_key, sort_key, search_key FROM books WHERE id = ?", dirty).
		Scan(&title, &sortTitle, &dedupKey, &sortKey, &searchKey); err != nil {
		t.Fatal(err)
	}
	if title != "Light Bringer" || sortTitle != "Light Bringer" {
		t.Errorf("title = %q, sort_title = %q, want both %q", title, sortTitle, "Light Bringer")
	}
	if want := indexer.CanonicalDedupKey("Light Bringer"); dedupKey != want {
		t.Errorf("dedup_key = %q, want %q", dedupKey, want)
	}
	if want := bookSortKey("Light Bringer", "Light Bringer"); sortKey != want {
		t.Errorf("sort_key = %q, want %q", sortKey, want)
	}
	if want := textutil.FoldForSearch("Light Bringer"); searchKey != want {
		t.Errorf("search_key = %q, want %q", searchKey, want)
	}
	if got := bookTitle(t, database, clean); got != "Red Rising" {
		t.Errorf("clean title = %q, want it unchanged", got)
	}
	if got := bookTitle(t, database, locked); got != "Golden Son (Duplicate of OL26431102W)" {
		t.Errorf("locked title = %q, want the user's locked value kept", got)
	}
}

func bookTitle(t *testing.T, database *sql.DB, id int64) string {
	t.Helper()
	var title string
	if err := database.QueryRow("SELECT title FROM books WHERE id = ?", id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title
}
