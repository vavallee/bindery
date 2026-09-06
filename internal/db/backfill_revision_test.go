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

// TestBackfillSearchKeys_SkippedWhileRevisionMatches is the #1660 half of the
// #2346 gate, across all three tables the search box reads.
//
// The stakes differ from the other two backfills: a stale sort_key puts a row
// in the wrong place in a list, but a stale search_key makes the row
// unfindable, and nothing tells the user their library is only partly
// searchable. So this asserts both directions — the scan is skipped while the
// marker is current, and it really does repair every table once it runs.
func TestBackfillSearchKeys_SkippedWhileRevisionMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeySearchKeys); !ok || got != strconv.Itoa(textutil.FoldForSearchRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, textutil.FoldForSearchRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	aliases := NewAuthorAliasRepo(database)
	author := &models.Author{ForeignID: "OL-SK-A", Name: "Jo Nesbø", SortName: "Nesbø, Jo"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-SK-B", AuthorID: author.ID, Title: "Snømannen", SortTitle: "Snømannen"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: author.ID, Name: "Harry Hole"}); err != nil {
		t.Fatal(err)
	}
	// Writes go through the repos, so the keys are already correct here.
	for _, q := range []struct{ table, want string }{
		{"books", "snomannen"},
		{"authors", "jo nesbo"},
		{"author_aliases", "harry hole"},
	} {
		var got string
		if err := database.QueryRow("SELECT search_key FROM " + q.table + " LIMIT 1").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != q.want {
			t.Errorf("%s.search_key written as %q on create, want %q", q.table, got, q.want)
		}
	}

	// Park a wrong key in each table and confirm a restart leaves it alone
	// while the marker is current.
	for _, table := range []string{"books", "authors", "author_aliases"} {
		if _, err := database.Exec("UPDATE " + table + " SET search_key = 'stale-on-purpose'"); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()

	database = openFileDB(t, path)
	for _, table := range []string{"books", "authors", "author_aliases"} {
		var got string
		if err := database.QueryRow("SELECT search_key FROM " + table + " LIMIT 1").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "stale-on-purpose" {
			t.Errorf("%s.search_key = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", table, got)
		}
	}

	// Drop the marker, as bumping FoldForSearchRev effectively does, and the
	// scan must repair every table.
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeySearchKeys); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	for _, q := range []struct{ table, want string }{
		{"books", "snomannen"},
		{"authors", "jo nesbo"},
		{"author_aliases", "harry hole"},
	} {
		var got string
		if err := database.QueryRow("SELECT search_key FROM " + q.table + " LIMIT 1").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != q.want {
			t.Errorf("%s.search_key = %q after the marker was dropped, want %q", q.table, got, q.want)
		}
	}
}

// TestBackfillBookSortKeys_SkippedWhileRevisionMatches is the books-table twin
// of the authors sort-key gate. books.sort_key arrives empty from migration 083
// because SQLite cannot fold, so the first boot has to fill it or the A–Z list
// orders every existing row under the empty string.
func TestBackfillBookSortKeys_SkippedWhileRevisionMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeyBookSortKeys); !ok || got != strconv.Itoa(bookSortKeyRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, bookSortKeyRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-BSK-A", Name: "Sort", SortName: "Sort"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-BSK-B", AuthorID: author.ID, Title: "Ödland", SortTitle: "Ödland"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE books SET sort_key = 'stale-on-purpose' WHERE id = ?", book.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	var key string
	if err := database.QueryRow("SELECT sort_key FROM books WHERE id = ?", book.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "stale-on-purpose" {
		t.Errorf("books.sort_key = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", key)
	}

	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyBookSortKeys); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	if err := database.QueryRow("SELECT sort_key FROM books WHERE id = ?", book.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if want := bookSortKey("Ödland", "Ödland"); key != want {
		t.Errorf("books.sort_key = %q after the marker was dropped, want %q", key, want)
	}
}

// TestFreshDatabaseStampsBackfillsWithoutScanning covers the short-circuit in
// migrate(): a database created by the call that migrates it has no rows an
// older normalizer could have written, so every backfill is recorded as current
// without being run.
//
// The property under test is the one that makes the shortcut safe, not the
// saving itself: the markers must land at exactly the revisions the backfills
// would have reached, or the next boot silently skips a repair that was never
// performed.
func TestFreshDatabaseStampsBackfillsWithoutScanning(t *testing.T) {
	database := openFileDB(t, filepath.Join(t.TempDir(), "bindery.db"))

	for _, want := range []struct {
		key string
		rev int
	}{
		{backfillRevKeyDedup, indexer.CanonicalDedupKeyRev},
		{backfillRevKeySortKeys, authorSortKeyRev},
		{backfillRevKeyBookSortKeys, bookSortKeyRev},
		{backfillRevKeySearchKeys, textutil.FoldForSearchRev},
	} {
		got, ok := settingValue(t, database, want.key)
		if !ok || got != strconv.Itoa(want.rev) {
			t.Errorf("%s after creating the database = %q (present %v), want %d", want.key, got, ok, want.rev)
		}
	}
}

// TestExistingDatabaseIsNotTreatedAsFresh is the guard on the other side of
// that shortcut. A restored backup, a downgrade, or any second open carries its
// migration history, so isFreshDatabase must answer false and let the backfills
// run — those are exactly the files that can hold rows keyed by an older
// normalizer.
func TestExistingDatabaseIsNotTreatedAsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")

	database := openFileDB(t, path)
	if fresh, err := isFreshDatabase(database); err != nil || fresh {
		t.Fatalf("isFreshDatabase on an already-migrated handle = %v, %v; want false, nil", fresh, err)
	}

	// Reopening the file is the case that matters: migrate() runs again, and it
	// must see the recorded history rather than an empty schema.
	reopened := openFileDB(t, path)
	if fresh, err := isFreshDatabase(reopened); err != nil || fresh {
		t.Fatalf("isFreshDatabase after reopening the file = %v, %v; want false, nil", fresh, err)
	}
}

// TestAuthorSortKeyRevisionBumpRepairsLigatureRows is the upgrade path for the
// fold-ordering fix. An existing library holds sort_key values computed when
// FoldNonDecomposableLatin still ran before mark stripping, so a name written
// with U+01E2 kept its ligature and went on sorting after Z. The bump to
// revision 2 is the whole mechanism that repairs them, and a stale marker has
// to behave like a missing one for that to work.
func TestAuthorSortKeyRevisionBumpRepairsLigatureRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	authors := NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL-LIG-1", Name: "Ǣlfric of Eynsham", SortName: "Ǣlfric of Eynsham"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Rewind the row and the marker to what revision 1 would have left behind.
	const revOneKey = "ælfric of eynsham"
	if _, err := database.Exec("UPDATE authors SET sort_key = ? WHERE id = ?", revOneKey, author.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE settings SET value = '1' WHERE key = ?", backfillRevKeySortKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openFileDB(t, path)
	var got string
	if err := reopened.QueryRow("SELECT sort_key FROM authors WHERE id = ?", author.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "aelfric of eynsham" {
		t.Errorf("sort_key after the revision bump = %q, want %q (the stale marker did not force the backfill)", got, "aelfric of eynsham")
	}
	// And the marker moved on, so the next boot pays nothing.
	if v, ok := settingValue(t, reopened, backfillRevKeySortKeys); !ok || v != strconv.Itoa(authorSortKeyRev) {
		t.Errorf("marker after the repair = %q (present %v), want %d", v, ok, authorSortKeyRev)
	}
}
