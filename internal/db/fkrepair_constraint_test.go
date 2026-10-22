package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func execAll(t *testing.T, database *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// The classifier looks at the driver's result code, one case per kind of
// constraint SQLite can refuse a statement on, and says no to anything else.
func TestIsConstraintErrorByKind(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	execAll(t, database,
		`CREATE TABLE k_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE k_nn (id INTEGER PRIMARY KEY, v INTEGER NOT NULL)`,
		`CREATE TABLE k_ck (id INTEGER PRIMARY KEY, v INTEGER CHECK (v > 0))`,
		`CREATE TABLE k_fk (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES k_parent(id))`,
		`CREATE TABLE k_tr (id INTEGER PRIMARY KEY)`,
		`CREATE TRIGGER k_tr_guard BEFORE INSERT ON k_tr BEGIN SELECT RAISE(ABORT, 'protected'); END`,
		`CREATE TABLE k_uq (id INTEGER PRIMARY KEY, v TEXT UNIQUE)`,
		`INSERT INTO k_uq (v) VALUES ('a')`,
	)
	for name, stmt := range map[string]string{
		"not null":    `INSERT INTO k_nn (v) VALUES (NULL)`,
		"check":       `INSERT INTO k_ck (v) VALUES (0)`,
		"foreign key": `INSERT INTO k_fk (pid) VALUES (999)`,
		"trigger":     `INSERT INTO k_tr (id) VALUES (1)`,
		"unique":      `INSERT INTO k_uq (v) VALUES ('a')`,
	} {
		_, err := database.Exec(stmt)
		if err == nil {
			t.Fatalf("%s: the statement was accepted", name)
		}
		if !isConstraintError(err) {
			t.Errorf("%s: %v was not classified as a constraint error", name, err)
		}
	}

	_, notConstraint := database.Exec(`SELECT * FROM no_such_table`)
	if notConstraint == nil || isConstraintError(notConstraint) {
		t.Errorf("a missing table must not read as a constraint error: %v", notConstraint)
	}
	// Message text alone is not enough: only the driver's code counts.
	if isConstraintError(context.Canceled) || isConstraintError(nil) || isConstraintError(sql.ErrNoRows) {
		t.Error("a non driver error was classified as a constraint error")
	}
}

// Each kind of refusal is a skip in a real repair, not an abort.
func TestRepairSkipsEveryKindOfConstraint(t *testing.T) {
	database, path := openRepairFixture(t, 2)
	execAll(t, database,
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_nn (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`CREATE TABLE zz_ck (id INTEGER PRIMARY KEY, pid INTEGER CHECK (pid IS NOT NULL) REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`CREATE TABLE zz_mid (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE CASCADE)`,
		`CREATE TABLE zz_leaf (id INTEGER PRIMARY KEY, mid INTEGER REFERENCES zz_mid(id) ON DELETE RESTRICT)`,
		`CREATE TABLE zz_tr (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE CASCADE)`,
		`CREATE TRIGGER zz_tr_guard BEFORE DELETE ON zz_tr BEGIN SELECT RAISE(ABORT, 'protected'); END`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_nn (id, pid) VALUES (1, 77)`,
		`INSERT INTO zz_ck (id, pid) VALUES (1, 77)`,
		`INSERT INTO zz_mid (id, pid) VALUES (1, 77)`,
		`INSERT INTO zz_leaf (id, mid) VALUES (1, 1)`,
		`INSERT INTO zz_tr (id, pid) VALUES (1, 77)`,
		`PRAGMA foreign_keys = ON`,
	)
	report, err := RepairForeignKeyDrift(context.Background(), database, RepairOptions{Path: path, BatchSize: 100})
	if err != nil {
		t.Fatalf("a refused row stopped the repair: %v", err)
	}
	joined := strings.Join(report.SkipNotes, "|")
	for _, tbl := range []string{"zz_nn", "zz_ck", "zz_mid", "zz_tr"} {
		if !strings.Contains(joined, tbl) {
			t.Errorf("no skip note for %s in %v", tbl, report.SkipNotes)
		}
	}
	if report.Deleted == 0 {
		t.Error("the repairable rows were not repaired")
	}
}

// Only a constraint refusal is a skip. Any other error (here a trigger that
// calls a function that does not exist) aborts the repair and rolls back its
// whole batch: earlier rows of that batch are NOT committed.
func TestRepairAbortsAndRollsBackOnANonConstraintError(t *testing.T) {
	database, path := openRepairFixture(t, 3)
	execAll(t, database,
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_x (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE CASCADE)`,
		`CREATE TRIGGER zz_x_boom BEFORE DELETE ON zz_x BEGIN SELECT no_such_function(); END`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_x (id, pid) VALUES (1, 77)`,
		`PRAGMA foreign_keys = ON`,
	)
	before := countRows(t, database, `SELECT COUNT(*) FROM book_files`)

	for _, mode := range []string{"batched", "cli"} {
		var report RepairReport
		var err error
		if mode == "batched" {
			report, err = RepairForeignKeyDrift(context.Background(), database, RepairOptions{Path: path, BatchSize: 100})
		} else {
			// The CLI path has no enclosing transaction, so only the rows
			// before the failing one are committed; the point of this case is
			// that the error is returned, not swallowed.
			v, verr := ForeignKeyViolations(context.Background(), database)
			if verr != nil {
				t.Fatal(verr)
			}
			_, err = RepairForeignKeyViolations(context.Background(), database, v)
		}
		if err == nil {
			t.Fatalf("%s: a non constraint error was swallowed", mode)
		}
		if mode == "batched" {
			if report.Deleted != 0 || report.Nulled != 0 {
				t.Errorf("batched: the report counts rolled back work: %+v", report)
			}
			if got := countRows(t, database, `SELECT COUNT(*) FROM book_files`); got != before {
				t.Errorf("batched: book_files %d -> %d: rows of the failed batch were committed", before, got)
			}
			if got := countRows(t, database, `SELECT COUNT(*) FROM zz_x`); got != 1 {
				t.Errorf("batched: zz_x rows = %d, want the failing row still there", got)
			}
		}
	}
}

// SET NULL on a NOT NULL column is refused for every row, so the preview must
// not count those rows as fixable.
func TestForeignKeyDriftFixableExcludesNotNullSetNull(t *testing.T) {
	database, path := openRepairFixture(t, 0)
	execAll(t, database,
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_nn (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`CREATE TABLE zz_ok (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_nn (id, pid) VALUES (1, 9), (2, 9), (3, 9)`,
		`INSERT INTO zz_ok (id, pid) VALUES (1, 9), (2, 9)`,
		`PRAGMA foreign_keys = ON`,
	)
	ctx := context.Background()
	found, err := ForeignKeyDriftReportAt(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := ForeignKeyDriftFixableAt(ctx, path, found)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range found {
		switch d.ChildTable {
		case "zz_nn":
			if d.Violations != 3 || counts[i] != 0 {
				t.Errorf("zz_nn violations %d fixable %d, want 3 and 0", d.Violations, counts[i])
			}
		case "zz_ok":
			if counts[i] != 2 {
				t.Errorf("zz_ok fixable %d, want 2", counts[i])
			}
		}
	}
}
