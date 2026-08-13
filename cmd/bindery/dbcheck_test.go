package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// seedDriftedDatabase creates a real on-disk database carrying orphan rows of
// the kind #1727 left behind, and returns its path.
func seedDriftedDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bindery.db")

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (1, 'OL1A', 'A', 'A')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (1, 'OL1W', 1, 'B', 'B')`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (1, 1, 'ebook', '/l/1.epub')`,
		`DELETE FROM books WHERE id = 1`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// TestRunFKToolReportsThenRepairs covers the recovery path an operator uses
// when an instance is stuck (#1972): db-check must report without changing
// anything, and db-repair must clear the orphans.
func TestRunFKToolReportsThenRepairs(t *testing.T) {
	path := seedDriftedDatabase(t)
	ctx := context.Background()

	if code := runFKTool(path, false); code != 0 {
		t.Fatalf("db-check exit code = %d, want 0", code)
	}

	// The report is read-only: the orphan is still there.
	if got := countViolations(t, ctx, path); got != 1 {
		t.Fatalf("violations after db-check = %d, want 1 (report must not modify)", got)
	}

	if code := runFKTool(path, true); code != 0 {
		t.Fatalf("db-repair exit code = %d, want 0", code)
	}
	if got := countViolations(t, ctx, path); got != 0 {
		t.Errorf("violations after db-repair = %d, want 0", got)
	}
}

// TestRunFKToolCleanDatabase is the no-op case: a healthy database reports
// nothing to do and exits 0.
func TestRunFKToolCleanDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if code := runFKTool(path, true); code != 0 {
		t.Errorf("db-repair on a clean database exit code = %d, want 0", code)
	}
}

func TestRunFKToolMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "does-not-exist.db")
	if code := runFKTool(path, false); code != 1 {
		t.Errorf("db-check on an unopenable path exit code = %d, want 1", code)
	}
}

func countViolations(t *testing.T, ctx context.Context, path string) int {
	t.Helper()
	database, err := db.OpenForMaintenance(ctx, path)
	if err != nil {
		t.Fatalf("open for maintenance: %v", err)
	}
	defer func() { _ = database.Close() }()
	violations, err := db.ForeignKeyViolations(ctx, database)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	return len(violations)
}
