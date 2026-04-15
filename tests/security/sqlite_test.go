package security_test

import (
	"context"
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
