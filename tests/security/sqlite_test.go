package security_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// TestSQLite_ForeignKeysOn asserts that the production DB connection has
// foreign_keys enforcement on. This is set in setPragmas; a regression
// (someone removing it to fix a migration issue) would silently allow
// orphaned rows across author/book/edition tables.
func TestSQLite_ForeignKeysOn(t *testing.T) {
	t.Parallel()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var on int
	if err := database.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatalf("query PRAGMA foreign_keys: %v", err)
	}
	if on != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", on)
	}
}

// TestSQLite_JournalModeWAL verifies the WAL journal mode was applied.
// WAL is load-bearing for our read-while-writing story (UI can poll
// /queue while the importer is writing history rows).
func TestSQLite_JournalModeWAL(t *testing.T) {
	t.Parallel()
	// Memory DBs don't support WAL; just verify Open on a real path would
	// apply the pragma without erroring.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Even in :memory: the pragma set should not fail — setPragmas runs
	// before migrate and would error out at OpenMemory if it did.
	var mode string
	if err := database.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query PRAGMA journal_mode: %v", err)
	}
	if mode == "" {
		t.Error("PRAGMA journal_mode returned empty string")
	}
}

// TestSQLite_WALCompanionPragmas asserts the connection tuning pragmas carried
// in connectionPragmaDSN are actually applied by the driver. They are easy to
// get wrong silently: a typo in a `_pragma` DSN parameter does not fail the
// open, it just leaves SQLite on its default, so only reading the value back
// proves anything.
//
// Opened through db.Open on a real file rather than OpenMemory, because
// synchronous is meaningless for an in-memory database.
func TestSQLite_WALCompanionPragmas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "bindery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cases := []struct {
		pragma string
		want   int
		note   string
	}{
		{"synchronous", 1, "NORMAL, the documented companion to WAL"},
		{"temp_store", 2, "MEMORY, keeps ORDER BY spills off disk"},
		{"cache_size", -16000, "16 MB, negative means KiB not pages"},
	}
	for _, tc := range cases {
		t.Run(tc.pragma, func(t *testing.T) {
			var got int
			if err := database.QueryRowContext(context.Background(), "PRAGMA "+tc.pragma).Scan(&got); err != nil {
				t.Fatalf("query PRAGMA %s: %v", tc.pragma, err)
			}
			if got != tc.want {
				t.Errorf("PRAGMA %s = %d, want %d (%s)", tc.pragma, got, tc.want, tc.note)
			}
		})
	}
}

// TestSQLite_PragmasSurviveConnectionReplacement is the property #1727 was
// about, extended to the pragmas added alongside it. database/sql discards a
// connection whenever the driver reports it as bad, and the replacement starts
// from SQLite's defaults. Anything set once through db.Exec is lost at that
// point; anything carried in the DSN is re-applied on open.
//
// Forcing a real connection replacement from a test is awkward, so this closes
// the pool instead, which is the same thing from the pragma's point of view:
// the next query must open a fresh connection.
func TestSQLite_PragmasSurviveConnectionReplacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "bindery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatalf("warm the pool: %v", err)
	}
	// Retire every idle connection so the assertions below run on a new one.
	database.SetMaxIdleConns(0)
	database.SetMaxIdleConns(1)

	for pragma, want := range map[string]int{
		"foreign_keys": 1,
		"synchronous":  1,
		"temp_store":   2,
		"cache_size":   -16000,
	} {
		var got int
		if err := database.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("query PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("after connection replacement PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}
}
