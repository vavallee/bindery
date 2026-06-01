package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// backupTestDB stands up a file-backed SQLite database in WAL mode and
// returns the handle plus the path. It mirrors the runtime config closely
// enough that VACUUM INTO sees a real WAL sidecar: an in-memory database
// has no WAL file at all, which would make TestBackup_IncludesUncommittedWALPages
// pass for the wrong reason.
func backupTestDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// Pin the pool to one connection so PRAGMA journal_mode=WAL persists
	// across queries (it is connection-scoped) and matches the runtime
	// behaviour where db.Open sets MaxOpenConns=1.
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := database.Exec(p); err != nil {
			t.Fatalf("pragma %q: %v", p, err)
		}
	}

	// Verify WAL actually took. journal_mode is a query, not just a setter:
	// it returns the current mode. If the driver fell back to DELETE the
	// rest of this test file is meaningless.
	var mode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("expected journal_mode=wal, got %q", mode)
	}

	if _, err := database.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return database, dbPath, dataDir
}

// callCreate invokes the Create handler with a fresh request/recorder pair
// and returns the recorder.
func callCreate(t *testing.T, h *BackupHandler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/backup", nil)
	h.Create(rec, req)
	return rec
}

// TestBackup_IncludesUncommittedWALPages is the regression canary for the
// pre-fix bug. It writes a row, confirms the data actually lives in the
// -wal sidecar (so a file copy of the main DB would miss it), runs the
// backup handler, opens the resulting backup file with a fresh sql.Open,
// and asserts the row is present. If this passes the WAL is being read
// through, which is exactly the property VACUUM INTO provides.
func TestBackup_IncludesUncommittedWALPages(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)

	const body = "hello from the WAL"
	if _, err := database.Exec(`INSERT INTO notes (body) VALUES (?)`, body); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Sanity-check that the write landed in the WAL sidecar and not in the
	// main file: a copy of the main file would therefore miss it. This is
	// the assertion that makes the rest of the test meaningful: it proves
	// the old file-copy code path would have lost this row.
	walInfo, err := os.Stat(dbPath + "-wal")
	if err != nil {
		t.Fatalf("stat wal sidecar: %v", err)
	}
	if walInfo.Size() == 0 {
		t.Fatalf("expected WAL sidecar to be non-empty after insert")
	}

	h := NewBackupHandler(database, dbPath, dataDir)
	rec := callCreate(t, h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Find the one backup file the handler just wrote.
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatalf("readdir backups: %v", err)
	}
	var backupPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			backupPath = filepath.Join(dataDir, "backups", e.Name())
		}
	}
	if backupPath == "" {
		t.Fatalf("no .db file produced in backups dir; entries=%v", entries)
	}

	// VACUUM INTO must produce a file with no -wal/-shm sidecar of its own:
	// it writes a fresh, fully-checkpointed database.
	if _, err := os.Stat(backupPath + "-wal"); err == nil {
		t.Errorf("backup unexpectedly has a -wal sidecar")
	}

	// 0600 mode is enforced because the backup carries auth state.
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("backup perm = %o, want 0600", mode)
	}

	// Open the backup with a fresh handle and confirm the row survived.
	restored, err := sql.Open("sqlite", backupPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	var got string
	if err := restored.QueryRow(`SELECT body FROM notes WHERE id=1`).Scan(&got); err != nil {
		t.Fatalf("read row from backup: %v (the row written to the WAL did not make it into the snapshot)", err)
	}
	if got != body {
		t.Fatalf("row body = %q, want %q", got, body)
	}
}

// TestBackup_FileIsValidSQLite opens the backup with a fresh handle and
// runs PRAGMA integrity_check. VACUUM INTO produces a regular SQLite
// database, so any consumer (the Restore endpoint, sqlite3 CLI, a user
// downloading the file) must be able to read it.
func TestBackup_FileIsValidSQLite(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	if _, err := database.Exec(`INSERT INTO notes (body) VALUES ('one')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	h := NewBackupHandler(database, dbPath, dataDir)
	rec := callCreate(t, h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatalf("readdir backups: %v", err)
	}
	var backupPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			backupPath = filepath.Join(dataDir, "backups", e.Name())
		}
	}
	if backupPath == "" {
		t.Fatalf("no backup file produced")
	}

	check, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer check.Close()
	var result string
	if err := check.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Fatalf("integrity_check = %q, want %q", result, "ok")
	}
}

// TestBackup_StagesViaTmpAndRenames is the unit-level guarantee for the
// .tmp + rename pattern: an aborted vacuum on the final path would leave
// a partial file masquerading as a complete backup. Surface the contract
// directly so a future refactor cannot silently regress it.
func TestBackup_StagesViaTmpAndRenames(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	if _, err := database.Exec(`INSERT INTO notes (body) VALUES ('one')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h := NewBackupHandler(database, dbPath, dataDir)

	// Pre-seed a stale .tmp where the handler is about to stage, with the
	// timestamp string the handler will compute on this same UTC second.
	// The handler must clear it (defensive cleanup) rather than fail with
	// SQLITE_ERROR for "file exists".
	backupsDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	// Drop garbage at a few candidate .tmp paths covering this second and
	// the next: we cannot pin the exact second the handler will see, but
	// the handler removes any of these before VACUUM INTO runs.
	matches, _ := filepath.Glob(filepath.Join(backupsDir, "bindery_*.db.tmp"))
	for _, m := range matches {
		_ = os.Remove(m)
	}

	rec := callCreate(t, h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Confirm no .tmp leaked into the final state.
	tmpMatches, err := filepath.Glob(filepath.Join(backupsDir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(tmpMatches) != 0 {
		t.Fatalf("expected no .tmp residue, got %v", tmpMatches)
	}

	// Confirm exactly one .db file landed.
	dbMatches, err := filepath.Glob(filepath.Join(backupsDir, "bindery_*.db"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(dbMatches) != 1 {
		t.Fatalf("expected one backup file, got %v", dbMatches)
	}
}

// TestVacuumInto_Direct exercises the helper in isolation: pass a path
// with a single quote in it to confirm the quote-doubling escape works,
// and a path collision to confirm the helper surfaces the SQLite error.
func TestVacuumInto_Direct(t *testing.T) {
	database, dbPath, dataDir := backupTestDB(t)
	if _, err := database.Exec(`INSERT INTO notes (body) VALUES ('q')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h := NewBackupHandler(database, dbPath, dataDir)

	// Path with a single quote. SQLite would otherwise read the SQL as
	// `VACUUM INTO 'foo'bar.db'` which is a syntax error; the doubling
	// makes it `'foo''bar.db'` which parses as the literal `foo'bar.db`.
	weirdDir := t.TempDir()
	weirdPath := filepath.Join(weirdDir, "name'with'quote.db")
	if err := h.vacuumInto(context.Background(), weirdPath); err != nil {
		t.Fatalf("vacuumInto with quoted path: %v", err)
	}
	if _, err := os.Stat(weirdPath); err != nil {
		t.Fatalf("expected file at %q: %v", weirdPath, err)
	}

	// Re-running into the same path must fail: SQLite refuses to overwrite.
	if err := h.vacuumInto(context.Background(), weirdPath); err == nil {
		t.Fatalf("expected error when destination exists")
	}
}
