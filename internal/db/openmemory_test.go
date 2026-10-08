package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
)

// OpenMemory hands out copies of one migrated template; a write to one copy
// must not show up in the next, and every pragma the DSN sets must still hold
// on the copy.
func TestOpenMemory_CopiesAreIndependent(t *testing.T) {
	a, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Exec(`INSERT INTO settings (key, value) VALUES ('openmemory_probe', 'x')`); err != nil {
		t.Fatal(err)
	}

	b, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'openmemory_probe'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("row written to one OpenMemory database leaked into another")
	}
	for _, p := range connectionPragmas() {
		name, want, _ := strings.Cut(strings.TrimSuffix(p, ")"), "(")
		var got string
		if err := b.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", name, got, want)
		}
	}
	if got, want := len(versionSetForTest(t, b)), countMigrationFiles(t); got != want {
		t.Fatalf("schema_migrations has %d versions, want %d", got, want)
	}
}

func countMigrationFiles(t *testing.T) int {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// A failed template build is reported by every OpenMemory call, not retried
// into a half-migrated database.
//
// It swaps the package's template for the duration, so it must not call
// t.Parallel: Go holds parallel tests back until every sequential one has
// finished, which is what keeps the swap invisible to them.
func TestOpenMemory_TemplateErrorIsReturned(t *testing.T) {
	db, err := OpenMemory() // the template is built by now
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	saved, savedErr := tmpl, tmplErr
	t.Cleanup(func() { tmpl, tmplErr = saved, savedErr })
	tmplErr = errors.New("migration failed")
	if db, err := OpenMemory(); err == nil || db != nil {
		t.Fatalf("OpenMemory = %v, %v; want the template error", db, err)
	}
	// A template that cannot be loaded fails the call instead of handing out
	// an empty database.
	tmpl, tmplErr = nil, nil
	if db, err := OpenMemory(); err == nil || db != nil {
		t.Fatalf("OpenMemory with an unloadable template = %v, %v; want an error", db, err)
	}
}

func TestWithRawConn_ClosedDB(t *testing.T) {
	db, err := openMemoryConn()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := withRawConn(db, func(memConn) error { return nil }); err == nil {
		t.Fatal("withRawConn on a closed database succeeded")
	}
}

// registerPlainDriver keeps sql.Register to one call per process, so
// the test also passes under -count greater than 1.
var registerPlainDriver sync.Once

// A driver connection that cannot serialize is an error, not a panic.
func TestWithRawConn_DriverWithoutSerialize(t *testing.T) {
	registerPlainDriver.Do(func() { sql.Register("openmemory-plain", plainDriver{}) })
	db, err := sql.Open("openmemory-plain", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = withRawConn(db, func(memConn) error { t.Fatal("fn called for a driver without Serialize"); return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot serialize") {
		t.Fatalf("err = %v, want a cannot-serialize error", err)
	}
}

type plainDriver struct{}

func (plainDriver) Open(string) (driver.Conn, error) { return plainConn{}, nil }

type plainConn struct{}

func (plainConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (plainConn) Close() error                        { return nil }
func (plainConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
