package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// writeSQLiteFile creates a small, valid SQLite database at path containing a
// single notes row, and returns its raw bytes.
func writeSQLiteFile(t *testing.T, path, body string) []byte {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO notes (body) VALUES (?)`, body); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test-local temp path
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return raw
}

func readNote(t *testing.T, path string) string {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	defer func() { _ = database.Close() }()
	var body string
	if err := database.QueryRow(`SELECT body FROM notes LIMIT 1`).Scan(&body); err != nil {
		t.Fatalf("select from %q: %v", path, err)
	}
	return body
}

// TestApplyPendingRestore_NoPendingFile is the common case: startup must not
// care that the staging path is absent.
func TestApplyPendingRestore_NoPendingFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bindery.db")
	writeSQLiteFile(t, dbPath, "live")

	if err := ApplyPendingRestore(dbPath); err != nil {
		t.Fatalf("ApplyPendingRestore: %v", err)
	}
	if got := readNote(t, dbPath); got != "live" {
		t.Fatalf("live db changed: got %q", got)
	}
}

// TestApplyPendingRestore_SwapsAndClearsSidecars covers the happy path and the
// property the whole change exists for: the stale -wal and -shm belonging to
// the replaced database must be gone, otherwise SQLite replays their frames
// over the restored pages at the next checkpoint.
func TestApplyPendingRestore_SwapsAndClearsSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bindery.db")
	writeSQLiteFile(t, dbPath, "live")
	writeSQLiteFile(t, PendingRestorePath(dbPath), "from-backup")

	// Stand in for the WAL sidecars the running process left behind.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	if err := ApplyPendingRestore(dbPath); err != nil {
		t.Fatalf("ApplyPendingRestore: %v", err)
	}

	if got := readNote(t, dbPath); got != "from-backup" {
		t.Fatalf("expected restored contents, got %q", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", suffix, err)
		}
	}
	if _, err := os.Stat(PendingRestorePath(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("expected pending file to be consumed, stat err=%v", err)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat restored db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected restored db mode 0600, got %v", perm)
	}
}

// TestApplyPendingRestore_RejectsBadFile: a staged file that is not a sound
// SQLite database must leave the live database exactly as it was, be parked
// under .restore-failed so the next start does not retry it, and must not
// stop the process starting.
func TestApplyPendingRestore_RejectsBadFile(t *testing.T) {
	cases := map[string][]byte{
		"garbage":  []byte("this is definitely not a database"),
		"empty":    {},
		"headerok": append([]byte("SQLite format 3\x00"), make([]byte, 4096)...),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "bindery.db")
			liveBytes := writeSQLiteFile(t, dbPath, "live")
			if err := os.WriteFile(PendingRestorePath(dbPath), content, 0o600); err != nil {
				t.Fatalf("write pending: %v", err)
			}

			if err := ApplyPendingRestore(dbPath); err != nil {
				t.Fatalf("ApplyPendingRestore should not fail startup: %v", err)
			}

			after, err := os.ReadFile(dbPath) // #nosec G304 -- test-local temp path
			if err != nil {
				t.Fatalf("read live db: %v", err)
			}
			if string(after) != string(liveBytes) {
				t.Fatalf("live database was modified by a rejected restore")
			}
			if got := readNote(t, dbPath); got != "live" {
				t.Fatalf("live db contents changed: %q", got)
			}
			if _, err := os.Stat(PendingRestorePath(dbPath)); !os.IsNotExist(err) {
				t.Fatalf("expected pending file to be moved aside, stat err=%v", err)
			}
			if _, err := os.Stat(dbPath + RestoreFailedSuffix); err != nil {
				t.Fatalf("expected %s to exist: %v", RestoreFailedSuffix, err)
			}
		})
	}
}

// TestApplyPendingRestore_TruncatedDatabase uses a real database cut in half,
// which is the shape a half-copied backup actually has: a valid header, a
// plausible page count in it, and missing pages.
func TestApplyPendingRestore_TruncatedDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bindery.db")
	writeSQLiteFile(t, dbPath, "live")

	donor := filepath.Join(dir, "donor.db")
	raw := writeSQLiteFile(t, donor, "from-backup")
	if len(raw) < 4096 {
		t.Fatalf("donor database unexpectedly small (%d bytes)", len(raw))
	}
	if err := os.WriteFile(PendingRestorePath(dbPath), raw[:len(raw)/2], 0o600); err != nil {
		t.Fatalf("write truncated pending: %v", err)
	}

	if err := ApplyPendingRestore(dbPath); err != nil {
		t.Fatalf("ApplyPendingRestore should not fail startup: %v", err)
	}
	if got := readNote(t, dbPath); got != "live" {
		t.Fatalf("live db contents changed: %q", got)
	}
	if _, err := os.Stat(dbPath + RestoreFailedSuffix); err != nil {
		t.Fatalf("expected %s to exist: %v", RestoreFailedSuffix, err)
	}
}

// TestCheckSQLiteFile_ReadOnlyDoesNotCreate covers the missing-file case. Note
// that CheckSQLiteFile rejects a missing file at its os.Open header read,
// before the DSN is built at all, so this test says nothing about the DSN
// itself — TestSQLiteFileURI_IsReallyReadOnly is what pins that.
func TestCheckSQLiteFile_ReadOnlyDoesNotCreate(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.db")
	if err := CheckSQLiteFile(context.Background(), missing); err == nil {
		t.Fatalf("expected an error for a missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("CheckSQLiteFile created %q", missing)
	}
}

// TestSQLiteFileURI_IsReallyReadOnly guards the DSN construction, which is easy
// to "simplify" back into a plain "<path>?mode=ro" that silently does the wrong
// thing. modernc.org/sqlite always calls sqlite3_open_v2 with
// SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE and only forwards mode= to SQLite
// when the DSN is a file: URI (it truncates a non-URI DSN at the first "?"
// instead), so without the file: prefix the check would open the file it is
// inspecting read-write and could recover or rewrite it.
func TestSQLiteFileURI_IsReallyReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sound.db")
	writeSQLiteFile(t, path, "live")

	database, err := sql.Open("sqlite", sqliteFileURI(path, "mode=ro"))
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	if _, err := database.Exec(`INSERT INTO notes (body) VALUES ('should not land')`); err == nil {
		t.Fatalf("wrote through a supposedly read-only handle; mode=ro was dropped")
	}
	if got := readNote(t, path); got != "live" {
		t.Fatalf("file changed under the read-only check: %q", got)
	}
}

// TestCheckSQLiteFile_AwkwardPath pins the percent-encoding half of
// sqliteFileURI. "?", "#" and "%" are syntax to SQLite's URI parser, so a data
// directory carrying any of them in its name would otherwise make the check
// look at the wrong path (or fail to parse), and a valid backup would be parked
// as .restore-failed on every start.
func TestCheckSQLiteFile_AwkwardPath(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "bindery data #1 ?x %y")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Build the file at a plain path first: writeSQLiteFile opens a non-URI
	// DSN and cannot itself cope with a "?" in the path.
	seed := filepath.Join(base, "seed.db")
	writeSQLiteFile(t, seed, "live")
	path := filepath.Join(dir, "bindery.db")
	if err := os.Rename(seed, path); err != nil {
		t.Fatalf("rename into awkward dir: %v", err)
	}

	if err := CheckSQLiteFile(context.Background(), path); err != nil {
		t.Fatalf("CheckSQLiteFile(%q) = %v", path, err)
	}

	// And a garbage file at an equally awkward path is still rejected.
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write bad file: %v", err)
	}
	if err := CheckSQLiteFile(context.Background(), bad); err == nil {
		t.Fatalf("CheckSQLiteFile accepted garbage at %q", bad)
	}
}

// TestOpen_AppliesPendingRestore proves the swap is wired into the real
// startup path, not just callable on its own.
func TestOpen_AppliesPendingRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bindery.db")

	live, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := live.Exec(`INSERT INTO settings (key, value) VALUES ('restore.marker', 'live')`); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Build the "backup": a second migrated database with a different marker.
	donorPath := filepath.Join(dir, "donor.db")
	donor, err := Open(donorPath)
	if err != nil {
		t.Fatalf("donor open: %v", err)
	}
	if _, err := donor.Exec(`INSERT INTO settings (key, value) VALUES ('restore.marker', 'from-backup')`); err != nil {
		t.Fatalf("insert donor marker: %v", err)
	}
	if _, err := donor.Exec(`VACUUM INTO '` + PendingRestorePath(dbPath) + `'`); err != nil {
		t.Fatalf("vacuum into pending: %v", err)
	}
	if err := donor.Close(); err != nil {
		t.Fatalf("donor close: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	var marker string
	if err := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'restore.marker'`).Scan(&marker); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker != "from-backup" {
		t.Fatalf("expected the staged backup to be swapped in, marker=%q", marker)
	}
}
