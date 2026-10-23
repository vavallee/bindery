package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// seedManyOrphans inserts n book_files rows whose book does not exist, with
// foreign keys off, so the drift scan has enough work to be reliably slower
// than a few milliseconds.
func seedManyOrphans(t *testing.T, database *sql.DB, n int) {
	t.Helper()
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ` + itoa(n) + `)
		 INSERT INTO book_files (book_id, format, path) SELECT 1000000 + i, 'ebook', '/l/' || i FROM seq`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// The scan must honour its context. Swapping QueryContext(ctx for
// QueryContext(context.Background() left every other test green (#3356).
func TestForeignKeyDriftReportHonoursDeadline(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seedManyOrphans(t, database, 100000)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = ForeignKeyDriftReport(ctx, database)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("scan took %v to notice the deadline", d)
	}
	// Connection released afterwards.
	if _, err := ForeignKeyDriftReport(context.Background(), database); err != nil {
		t.Fatalf("pool wedged after deadline: %v", err)
	}
}

// Same on the separate read only connection used in production, and it must
// produce the same counts as the shared pool form.
func TestForeignKeyDriftReportAtFileDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seedManyOrphans(t, database, 100000)

	got, err := ForeignKeyDriftReportAt(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChildTable != "book_files" || got[0].ParentTable != "books" || got[0].Violations != 100000 || got[0].OnDelete != "CASCADE" {
		t.Fatalf("got %#v", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ForeignKeyDriftReportAt(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("scan took %v to notice the deadline", d)
	}
}

// The scan leaves the data alone.
func TestForeignKeyDriftReportAtLeavesDataAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seedManyOrphans(t, database, 10)
	if _, err := ForeignKeyDriftReportAt(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM book_files`).Scan(&n); err != nil || n != 10 {
		t.Errorf("rows = %d (err %v), want 10", n, err)
	}
}
