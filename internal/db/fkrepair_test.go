package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openRepairFixture opens a migrated database FILE (the streaming source needs
// a path), seeds the standard orphan set and adds extra book_files orphans, so
// a small batch size spans several batches. It returns the pool and the path.
func openRepairFixture(t *testing.T, extraOrphans int) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bindery.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	seedOrphanRows(t, database)
	for i := 0; i < extraOrphans; i++ {
		stmts := []string{
			`PRAGMA foreign_keys = OFF`,
			fmt.Sprintf(`INSERT INTO book_files (book_id, format, path) VALUES (%d, 'ebook', '/l/x%d.epub')`, 1000+i, i),
			`PRAGMA foreign_keys = ON`,
		}
		for _, s := range stmts {
			if _, err := database.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	return database, path
}

func countRows(t *testing.T, database *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := database.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The batched repair resolves rows the way db-repair does: CASCADE removes,
// SET NULL clears, with the same totals, and the work is split into several
// transactions rather than one.
func TestRepairForeignKeyDriftBatches(t *testing.T) {
	for _, withPath := range []bool{true, false} {
		t.Run(fmt.Sprintf("path=%v", withPath), func(t *testing.T) {
			database, path := openRepairFixture(t, 25)
			if !withPath {
				path = ""
			}
			batches := 0
			report, err := RepairForeignKeyDrift(context.Background(), database, RepairOptions{
				Path: path, BatchSize: 10, AfterBatch: func(int) { batches++ },
			})
			if err != nil {
				t.Fatal(err)
			}
			if batches < 3 {
				t.Errorf("batches = %d, want the repair split into at least 3 transactions", batches)
			}
			// book_files: 27 orphans (1 cascade from book 1, 1 from book 2, 25
			// extra); downloads: 1 SET NULL; books -> authors: 1 cascade.
			if report.Deleted == 0 || report.Nulled != 1 {
				t.Errorf("report = %+v, want deletes and exactly one cleared reference", report)
			}
			if got := countRows(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`); got != 0 {
				t.Errorf("violations after repair = %d, want 0", got)
			}
			if got := countRows(t, database, `SELECT COUNT(*) FROM downloads WHERE id = 1 AND book_id IS NULL`); got != 1 {
				t.Error("download history was deleted instead of having its reference cleared")
			}
		})
	}
}

// Same outcome as the CLI path for identical input: the two entry points share
// one per row implementation.
func TestRepairForeignKeyDriftMatchesCLIRepair(t *testing.T) {
	a, pathA := openRepairFixture(t, 7)
	b, _ := openRepairFixture(t, 7)
	got, err := RepairForeignKeyDrift(context.Background(), a, RepairOptions{Path: pathA, BatchSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	violations, err := ForeignKeyViolations(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	want, err := RepairForeignKeyViolations(context.Background(), b, violations)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deleted != want.Deleted || got.Nulled != want.Nulled || got.Skipped != want.Skipped {
		t.Errorf("batched repair %+v differs from db-repair %+v", got, want)
	}
}

// A cancel stops the repair at a batch boundary: committed batches stay,
// nothing else was touched, and the pool connection is free afterwards.
func TestRepairForeignKeyDriftCancelledBetweenBatches(t *testing.T) {
	database, path := openRepairFixture(t, 40)
	before := countRows(t, database, `SELECT COUNT(*) FROM book_files`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report, err := RepairForeignKeyDrift(ctx, database, RepairOptions{
		Path: path, BatchSize: 10,
		AfterBatch: func(n int) {
			if n == 1 {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if report.Deleted+report.Nulled == 0 {
		t.Error("the first committed batch should be reflected in the report")
	}
	after := countRows(t, database, `SELECT COUNT(*) FROM book_files`)
	if after == 0 || after >= before {
		t.Errorf("book_files %d -> %d: want some, but not all, orphans removed", before, after)
	}

	done := make(chan struct{})
	go func() {
		_ = database.QueryRow(`SELECT 1`).Scan(new(int))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the pool connection was not released after the cancel")
	}

	// Safe to run again: it finishes the job.
	if _, err := RepairForeignKeyDrift(context.Background(), database, RepairOptions{Path: path, BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`); got != 0 {
		t.Errorf("violations after the second run = %d", got)
	}
}

// Between batches the app can use the connection: a query issued from the
// after batch hook (where the repair holds no connection) completes at once,
// even though the violation stream is still open.
func TestRepairForeignKeyDriftReleasesConnectionBetweenBatches(t *testing.T) {
	database, path := openRepairFixture(t, 30)
	_, err := RepairForeignKeyDrift(context.Background(), database, RepairOptions{
		Path: path, BatchSize: 5,
		AfterBatch: func(int) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := database.QueryRowContext(ctx, `SELECT 1`).Scan(new(int)); err != nil {
				t.Errorf("the app could not use the pool between batches: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The violation list is a snapshot, so a repair works from rowids that may be
// stale. A parent that came back since the scan must save its child: the row
// is no longer an orphan and must not be deleted.
func TestRepairDoesNotTouchARowThatIsNoLongerAnOrphan(t *testing.T) {
	database, _ := openRepairFixture(t, 0)
	ctx := context.Background()
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (50, 4242, 'ebook', '/l/late.epub')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	// Between the scan and the repair the parent is created.
	for _, s := range []string{
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (5, 'OL5A', 'N', 'N')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (4242, 'OL4242W', 5, 'T', 'T')`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RepairForeignKeyViolations(ctx, database, stale); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM book_files WHERE id = 50`); got != 1 {
		t.Error("the repair deleted a row whose parent exists again")
	}
}

// A relationship with no declared ON DELETE action is reported and left alone,
// and the preview classifier agrees.
func TestRepairForeignKeyDriftLeavesUnfixableRowsAlone(t *testing.T) {
	database, path := openRepairFixture(t, 0)
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO authors (id, foreign_id, name, sort_name, owner_user_id) VALUES (9, 'OL9A', 'Z', 'Z', 999)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	report, err := RepairForeignKeyDrift(context.Background(), database, RepairOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || len(report.SkipNotes) != 1 {
		t.Errorf("report = %+v, want the one NO ACTION row skipped", report)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM authors WHERE id = 9`); got != 1 {
		t.Error("a row with no safe action was deleted")
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`); got != 1 {
		t.Errorf("violations left = %d, want the one unfixable row", got)
	}
	for in, want := range map[string]string{
		"CASCADE": DriftActionDelete, "cascade": DriftActionDelete, "SET NULL": DriftActionClear,
		"NO ACTION": DriftActionKeep, "RESTRICT": DriftActionKeep, "SET DEFAULT": DriftActionKeep, "": DriftActionKeep,
	} {
		if got := DriftAction(in); got != want {
			t.Errorf("DriftAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// The same guard protects the SET NULL branch: a parent that came back after
// the scan must keep its child's reference.
func TestRepairDoesNotClearAReferenceWhoseParentCameBack(t *testing.T) {
	database, _ := openRepairFixture(t, 0)
	ctx := context.Background()
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO downloads (id, guid, book_id, title, nzb_url) VALUES (50, 'late', 4242, 'R', 'http://x')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`INSERT INTO authors (id, foreign_id, name, sort_name) VALUES (5, 'OL5A', 'N', 'N')`,
		`INSERT INTO books (id, foreign_id, author_id, title, sort_title) VALUES (4242, 'OL4242W', 5, 'T', 'T')`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	report, err := RepairForeignKeyViolations(ctx, database, stale)
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, database, `SELECT COUNT(*) FROM downloads WHERE id = 50 AND book_id = 4242`); got != 1 {
		t.Error("the repair cleared the reference of a row whose parent exists again")
	}
	// The seeded download (parent still missing) was cleared, the late one was not.
	if report.Nulled != 1 {
		t.Errorf("nulled = %d, want only the one genuine orphan", report.Nulled)
	}
}

// seedConstraintTraps adds rows the schema's rule cannot be applied to: a SET
// NULL column that is NOT NULL, and a CASCADE row that something RESTRICTs.
func seedConstraintTraps(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, s := range []string{
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_nn (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`CREATE TABLE zz_mid (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE CASCADE)`,
		`CREATE TABLE zz_leaf (id INTEGER PRIMARY KEY, mid INTEGER REFERENCES zz_mid(id) ON DELETE RESTRICT)`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_nn (id, pid) VALUES (1, 77)`,
		`INSERT INTO zz_mid (id, pid) VALUES (1, 78)`,
		`INSERT INTO zz_leaf (id, mid) VALUES (1, 1)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// One row a constraint refuses must not roll back its batch or make every
// rerun fail at the same row: it is skipped with a note, and the rest of the
// cleanup goes through.
func TestRepairSkipsRowsAConstraintRefuses(t *testing.T) {
	for _, mode := range []string{"batched", "cli"} {
		t.Run(mode, func(t *testing.T) {
			database, path := openRepairFixture(t, 3)
			seedConstraintTraps(t, database)
			ctx := context.Background()

			run := func() RepairReport {
				t.Helper()
				if mode == "batched" {
					r, err := RepairForeignKeyDrift(ctx, database, RepairOptions{Path: path, BatchSize: 100})
					if err != nil {
						t.Fatalf("repair stopped on a refused row: %v", err)
					}
					return r
				}
				v, err := ForeignKeyViolations(ctx, database)
				if err != nil {
					t.Fatal(err)
				}
				r, err := RepairForeignKeyViolations(ctx, database, v)
				if err != nil {
					t.Fatalf("repair stopped on a refused row: %v", err)
				}
				return r
			}
			first := run()
			if first.Skipped < 2 {
				t.Errorf("skipped = %d, want both refused rows skipped", first.Skipped)
			}
			joined := strings.Join(first.SkipNotes, "|")
			if !strings.Contains(joined, "zz_nn") || !strings.Contains(joined, "zz_mid") {
				t.Errorf("skip notes %v do not name the refused tables", first.SkipNotes)
			}
			// The repairable rows in the same run were still repaired.
			if first.Deleted == 0 {
				t.Error("the rest of the cleanup did not proceed")
			}
			if got := countRows(t, database, `SELECT COUNT(*) FROM book_files WHERE book_id >= 1000`); got != 0 {
				t.Errorf("%d repairable orphans left behind", got)
			}
			// The refused rows are untouched, and a rerun is clean, not an error.
			if got := countRows(t, database, `SELECT COUNT(*) FROM zz_nn WHERE pid = 77`) + countRows(t, database, `SELECT COUNT(*) FROM zz_mid WHERE id = 1`); got != 2 {
				t.Error("a refused row was modified")
			}
			second := run()
			if second.Skipped < 2 {
				t.Errorf("rerun skipped = %d, want the same rows skipped again", second.Skipped)
			}
		})
	}
}

// The preview counts only rows a repair can resolve: a WITHOUT ROWID table
// cannot be addressed by rowid, so its rows are reported but not fixable.
func TestForeignKeyDriftFixableCountsWhatARepairCanDo(t *testing.T) {
	database, path := openRepairFixture(t, 2)
	for _, s := range []string{
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_wr (a INTEGER PRIMARY KEY, pid INTEGER REFERENCES zz_parent(id) ON DELETE CASCADE) WITHOUT ROWID`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_wr (a, pid) VALUES (1, 9), (2, 9)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	ctx := context.Background()
	found, err := ForeignKeyDriftReportAt(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for name, get := range map[string]func() ([]int, error){
		"path": func() ([]int, error) { return ForeignKeyDriftFixableAt(ctx, path, found) },
		"pool": func() ([]int, error) { return ForeignKeyDriftFixable(ctx, database, found) },
	} {
		counts, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(counts) != len(found) {
			t.Fatalf("%s: %d counts for %d relationships", name, len(counts), len(found))
		}
		for i, d := range found {
			switch d.ChildTable {
			case "zz_wr":
				if d.Violations != 2 || counts[i] != 0 {
					t.Errorf("%s: zz_wr violations %d fixable %d, want 2 and 0", name, d.Violations, counts[i])
				}
			case "book_files":
				if counts[i] != d.Violations || d.Violations == 0 {
					t.Errorf("%s: book_files fixable %d of %d, want all", name, counts[i], d.Violations)
				}
			case "downloads":
				if counts[i] != d.Violations {
					t.Errorf("%s: downloads fixable %d of %d, want all", name, counts[i], d.Violations)
				}
			}
		}
	}
}
