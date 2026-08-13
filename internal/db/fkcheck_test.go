package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// seedOrphanRows creates an author/book/book_files chain and then deletes the
// parents with foreign key enforcement off, reproducing the drift #1727 left on
// long-running instances: rows whose ON DELETE CASCADE never fired.
func seedOrphanRows(t *testing.T, database *sql.DB) {
	t.Helper()
	stmts := []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (1, 'OL1A', 'A', 'A')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (1, 'OL1W', 1, 'B', 'B')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (2, 'OL2W', 1, 'C', 'C')`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (1, 1, 'ebook', '/l/1.epub')`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (2, 2, 'ebook', '/l/2.epub')`,
		`INSERT INTO downloads (id, guid, book_id, title, nzb_url) VALUES (1, 'g1', 1, 'R', 'http://x')`,
		// The deletes that should have cascaded, but did not.
		`DELETE FROM books WHERE id = 1`,
		`DELETE FROM authors WHERE id = 1`,
		`PRAGMA foreign_keys = ON`,
	}
	for _, s := range stmts {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

// fkToggleMigration wraps body in the PRAGMA pair that makes the migration
// runner treat it as a table rebuild, so the integrity gate applies.
func fkToggleMigration(body string) string {
	return "-- +migrate Up\nPRAGMA foreign_keys = OFF;\n" + body + "\nPRAGMA foreign_keys = ON;\n"
}

// TestApplyMigrationAllowsPreExistingViolations is the #1972 regression. A
// migration that rebuilds a table used to be gated on a database-WIDE
// foreign_key_check, so orphan rows anywhere — including drift that predated
// the migration by many releases — aborted it, and the instance then refused to
// start on every restart. Pre-existing violations must be a warning, not a
// reason to lose the instance.
func TestApplyMigrationAllowsPreExistingViolations(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	seedOrphanRows(t, database)

	before, err := foreignKeyViolationCounts(database)
	if err != nil {
		t.Fatalf("baseline count: %v", err)
	}
	if before["book_files"] == 0 || before["books"] == 0 {
		t.Fatalf("fixture did not create orphans: %v", before)
	}

	content := fkToggleMigration(`CREATE TABLE fk_gate_probe (id INTEGER PRIMARY KEY);`)
	if err := applyMigration(database, 9001, "9001_probe.sql", content); err != nil {
		t.Fatalf("migration aborted by unrelated pre-existing drift: %v", err)
	}

	// The migration committed and the drift is untouched — nothing was deleted
	// behind the operator's back.
	var applied int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 9001`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("schema_migrations rows for 9001 = %d, want 1", applied)
	}
	after, err := foreignKeyViolationCounts(database)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after["book_files"] != before["book_files"] {
		t.Errorf("pre-existing violations changed: before %v, after %v", before, after)
	}
}

// TestApplyMigrationRejectsIntroducedViolations is the other half of #1972: the
// gate must still catch a rebuild that actually breaks referential integrity,
// even on a database that already carries unrelated drift.
func TestApplyMigrationRejectsIntroducedViolations(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	seedOrphanRows(t, database)

	// A second, intact author/book/file chain the buggy migration will break.
	for _, s := range []string{
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (2, 'OL2A', 'D', 'D')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (3, 'OL3W', 2, 'E', 'E')`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (3, 3, 'ebook', '/l/3.epub')`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	// With foreign keys off, this delete orphans book_files row 3.
	content := fkToggleMigration(`DELETE FROM books WHERE id = 3;`)
	err = applyMigration(database, 9002, "9002_buggy.sql", content)
	if err == nil {
		t.Fatal("migration that introduced foreign-key violations was allowed to commit")
	}
	if !strings.Contains(err.Error(), "book_files") {
		t.Errorf("error does not name the affected table: %v", err)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error does not tell the operator their data is intact: %v", err)
	}

	// Rolled back: the migration is not recorded and the book is still there.
	var applied, books int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 9002`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Errorf("failed migration was recorded in schema_migrations")
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM books WHERE id = 3`).Scan(&books); err != nil {
		t.Fatal(err)
	}
	if books != 1 {
		t.Errorf("rolled-back migration still removed book 3")
	}
}

func TestCheckForeignKeyDelta(t *testing.T) {
	tests := []struct {
		name          string
		before, after map[string]int
		wantErr       bool
		wantIn        string
	}{
		{
			name:   "clean database stays clean",
			before: map[string]int{},
			after:  map[string]int{},
		},
		{
			name:   "pre-existing drift is not fatal",
			before: map[string]int{"book_files": 600, "downloads": 831},
			after:  map[string]int{"book_files": 600, "downloads": 831},
		},
		{
			name:    "new violations are fatal and named",
			before:  map[string]int{"book_files": 600},
			after:   map[string]int{"book_files": 600, "editions": 3},
			wantErr: true,
			wantIn:  "editions=3 (was 0)",
		},
		{
			name:    "growth in an already-dirty table is fatal",
			before:  map[string]int{"book_files": 600},
			after:   map[string]int{"book_files": 601},
			wantErr: true,
			wantIn:  "book_files=601 (was 600)",
		},
		{
			// A rebuild that renames a table carries its existing violations to
			// the new name. Nothing broke, so nothing may abort.
			name:   "rename carries violations without introducing any",
			before: map[string]int{"old_name": 12},
			after:  map[string]int{"new_name": 12},
		},
		{
			name:   "a migration that fixes violations is fine",
			before: map[string]int{"book_files": 600},
			after:  map[string]int{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkForeignKeyDelta(72, tc.before, tc.after)
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not contain %q", err, tc.wantIn)
			}
		})
	}
}

// TestRepairForeignKeyViolations covers the offline repair path: it must replay
// the ON DELETE action the schema declares, not blanket-delete. Deleting a
// download row because the book it once pointed at is gone would throw away
// history the schema says should merely lose its reference.
func TestRepairForeignKeyViolations(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	seedOrphanRows(t, database)
	// An intact chain that must survive the repair untouched.
	for _, s := range []string{
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (2, 'OL2A', 'D', 'D')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (3, 'OL3W', 2, 'E', 'E')`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (3, 3, 'ebook', '/l/3.epub')`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	ctx := context.Background()

	violations, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Fatal("fixture produced no violations")
	}
	summary := SummariseViolations(violations)
	if len(summary) == 0 || !strings.Contains(strings.Join(summary, "|"), "book_files -> books") {
		t.Errorf("summary %v does not describe the book_files orphan", summary)
	}

	report, err := RepairForeignKeyViolations(ctx, database, violations)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if report.Remaining != 0 {
		t.Errorf("violations remaining after repair = %d, want 0", report.Remaining)
	}
	if report.Deleted == 0 {
		t.Error("expected the ON DELETE CASCADE orphans to be removed")
	}
	if report.Nulled == 0 {
		t.Error("expected the ON DELETE SET NULL reference to be cleared")
	}

	// The download row survives with its reference cleared, rather than being
	// deleted along with the book.
	var downloads int
	if err := database.QueryRow(`SELECT COUNT(*) FROM downloads WHERE id = 1 AND book_id IS NULL`).Scan(&downloads); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Error("repair deleted download history instead of clearing its dangling book reference")
	}

	// The orphaned book_files rows are gone — including row 2, which the
	// cascade from its own orphaned book takes with it — and the intact chain
	// is untouched.
	var files int
	if err := database.QueryRow(`SELECT COUNT(*) FROM book_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("book_files after repair = %d, want 1 (only the intact row)", files)
	}
	var intact int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM book_files f JOIN books b ON b.id = f.book_id WHERE f.id = 3 AND b.author_id = 2`,
	).Scan(&intact); err != nil {
		t.Fatal(err)
	}
	if intact != 1 {
		t.Error("repair removed rows that were referentially sound")
	}
}

// TestRepairSkipsUnsafeForeignKeys covers the deliberately conservative half of
// the repair: where the schema declares no ON DELETE action, guessing risks
// throwing away real data, so the row is left alone and reported by name.
func TestRepairSkipsUnsafeForeignKeys(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// authors.owner_user_id REFERENCES users(id) carries no ON DELETE clause.
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO authors (id, foreign_id, name, sort_name, owner_user_id) VALUES (1, 'OL1A', 'A', 'A', 999)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	ctx := context.Background()
	violations, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(violations))
	}

	report, err := RepairForeignKeyViolations(ctx, database, violations)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if report.Skipped != 1 || report.Deleted != 0 || report.Nulled != 0 {
		t.Errorf("report = %+v, want the row skipped and nothing touched", report)
	}
	if report.Remaining != 1 {
		t.Errorf("remaining = %d, want 1 — a skipped row is still a violation", report.Remaining)
	}
	if len(report.SkipNotes) != 1 || !strings.Contains(report.SkipNotes[0], "NO ACTION") {
		t.Errorf("skip notes %v do not explain why the row was left alone", report.SkipNotes)
	}
	var authors int
	if err := database.QueryRow(`SELECT COUNT(*) FROM authors WHERE id = 1`).Scan(&authors); err != nil {
		t.Fatal(err)
	}
	if authors != 1 {
		t.Error("repair deleted a row it had no safe rule for")
	}
}

// TestOpenForMaintenanceSkipsMigrations is the contract the recovery path
// depends on: the instance that needs db-check is the one that cannot get past
// a migration, so opening for maintenance must not run any.
func TestOpenForMaintenanceSkipsMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	fresh, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := fresh.Exec(`DELETE FROM schema_migrations WHERE version = 1`); err != nil {
		t.Fatalf("clear migration marker: %v", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	maint, err := OpenForMaintenance(ctx, path)
	if err != nil {
		t.Fatalf("open for maintenance: %v", err)
	}
	defer maint.Close()

	// If migrations had run, the marker we removed would be back.
	var marker int
	if err := maint.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != 0 {
		t.Error("OpenForMaintenance ran migrations; it must not")
	}
}

// TestMigrate072SurvivesUnrelatedOrphans is the reported failure in miniature:
// migration 072 rebuilds the Calibre tables, and a database carrying orphan
// rows in book_files/books/downloads must still upgrade.
func TestMigrate072SurvivesUnrelatedOrphans(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	seedOrphanRows(t, database)
	restorePreFixCalibreTables(t, database)

	v072 := migrationVersionForTest(t, "072_calibre_provenance_series_link.sql")
	if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version = ?`, v072); err != nil {
		t.Fatalf("clear migration 072 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("migration 072 aborted by unrelated orphan drift (#1972): %v", err)
	}
}
