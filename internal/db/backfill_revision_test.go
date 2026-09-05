package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// openFileDB opens the on-disk database at path and closes it at test end.
// The revision markers only mean anything across process restarts, so these
// tests need a file rather than OpenMemory.
func openFileDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return database
}

func settingValue(t *testing.T, database *sql.DB, key string) (string, bool) {
	t.Helper()
	var v string
	err := database.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read setting %s: %v", key, err)
	}
	return v, true
}

// TestBackfillBookDedupKeys_SkippedWhileRevisionMatches is the #2346 test.
//
// The backfill is observable through its own repair: park a deliberately wrong
// dedup_key on a row and see whether the next startup rewrites it. With the
// marker current it must not (that is the skipped scan); with the marker gone
// it must (the backfill is still there and still idempotent).
func TestBackfillBookDedupKeys_SkippedWhileRevisionMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeyDedup); !ok || got != strconv.Itoa(indexer.CanonicalDedupKeyRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, indexer.CanonicalDedupKeyRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-REV-A", Name: "Rev", SortName: "Rev"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-REV-B", AuthorID: author.ID, Title: "The Canonical Title", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET dedup_key = 'stale-on-purpose' WHERE id = ?", book.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// Second startup: the marker still matches, so the table is never scanned
	// and the wrong key survives.
	database = openFileDB(t, path)
	var key string
	if err := database.QueryRow("SELECT dedup_key FROM books WHERE id = ?", book.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "stale-on-purpose" {
		t.Errorf("dedup_key = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", key)
	}

	// Dropping the marker is the documented way to force a recompute, and
	// stands in for bumping CanonicalDedupKeyRev.
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyDedup); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	if err := database.QueryRow("SELECT dedup_key FROM books WHERE id = ?", book.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if want := indexer.CanonicalDedupKey("The Canonical Title"); key != want {
		t.Errorf("dedup_key = %q after the marker was dropped, want %q", key, want)
	}
	if got, ok := settingValue(t, database, backfillRevKeyDedup); !ok || got != strconv.Itoa(indexer.CanonicalDedupKeyRev) {
		t.Errorf("marker after the repair run = %q (present %v), want %d", got, ok, indexer.CanonicalDedupKeyRev)
	}
}

// TestBackfillAuthorSortKeys_SkippedWhileRevisionMatches is the same shape for
// the authors table.
func TestBackfillAuthorSortKeys_SkippedWhileRevisionMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeySortKeys); !ok || got != strconv.Itoa(authorSortKeyRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, authorSortKeyRev)
	}

	authors := NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL-REV-A2", Name: "Östergaard, Åse", SortName: "Östergaard, Åse"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE authors SET sort_key = 'stale-on-purpose' WHERE id = ?", author.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	var key string
	if err := database.QueryRow("SELECT sort_key FROM authors WHERE id = ?", author.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "stale-on-purpose" {
		t.Errorf("sort_key = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", key)
	}

	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeySortKeys); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	if err := database.QueryRow("SELECT sort_key FROM authors WHERE id = ?", author.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if want := authorSortKey("Östergaard, Åse"); key != want {
		t.Errorf("sort_key = %q after the marker was dropped, want %q", key, want)
	}
}

// TestBackfillRevisionCurrent_FailsOpen pins the "a bad marker costs a scan,
// never a stale key" rule.
func TestBackfillRevisionCurrent_FailsOpen(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const key = "backfill.test_rev"
	if backfillRevisionCurrent(database, key, 1) {
		t.Error("a missing marker must not count as current")
	}

	for _, stored := range []string{"", "  ", "banana", "1.5", "2"} {
		markRaw(t, database, key, stored)
		if backfillRevisionCurrent(database, key, 1) {
			t.Errorf("stored marker %q must not count as revision 1", stored)
		}
	}

	markBackfillRevision(database, key, 1)
	if !backfillRevisionCurrent(database, key, 1) {
		t.Error("a marker written at revision 1 must count as current")
	}
	if backfillRevisionCurrent(database, key, 2) {
		t.Error("a marker at revision 1 must not satisfy revision 2")
	}

	// Whitespace around an otherwise valid value is tolerated.
	markRaw(t, database, key, " 1 ")
	if !backfillRevisionCurrent(database, key, 1) {
		t.Error("a padded but valid marker must count as current")
	}
}

func markRaw(t *testing.T, database *sql.DB, key, value string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		t.Fatalf("write raw marker %q: %v", value, err)
	}
}
