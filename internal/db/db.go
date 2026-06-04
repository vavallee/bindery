// Package db contains the SQLite connection bootstrap, embedded migrations,
// and per-resource repository types backing the rest of Bindery.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vavallee/bindery/internal/indexer"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open creates a new database connection and runs migrations.
func Open(dbPath string) (*sql.DB, error) {
	if err := preflight(dbPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}

	if err := setPragmas(db); err != nil {
		db.Close()
		return nil, annotateCantOpen(dbPath, err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// The DB file holds auth rows (bcrypt hashes, session secrets, API key).
	// Lock its mode to 0600 defensively — umask might otherwise leave it 0644.
	if err := os.Chmod(dbPath, 0o600); err != nil && !os.IsNotExist(err) {
		slog.Warn("chmod database file", "path", dbPath, "error", err)
	}

	slog.Info("database ready", "path", dbPath)
	return db, nil
}

// OpenMemory creates an in-memory database for testing.
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open memory database: %w", err)
	}

	// SQLite in-memory databases are per-connection: a pool with multiple
	// connections would give each goroutine its own empty database. Pin to a
	// single connection so migrations and queries always see the same schema.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := setPragmas(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

// preflight validates that the directory containing dbPath exists, is a
// directory, and is writable by the current process before handing off to
// SQLite. SQLite's own error for "can't create the file" is cryptic
// (`SQLITE_CANTOPEN` → "unable to open database file (14)"), so we check
// first and emit a much clearer message with the resolved absolute path,
// parent-directory permissions, and ownership.
func preflight(dbPath string) error {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	parent := filepath.Dir(abs)

	// Accept parent missing: create it so the first-run flow just works when
	// a host operator forgot to mkdir the mount target.
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create database parent directory %q: %w (check volume is writable by the container user)", parent, err)
	}

	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("stat database parent directory %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("database parent %q is not a directory", parent)
	}

	// Try a round-trip write to catch read-only mounts / wrong-owner PVCs.
	// os.CreateTemp respects directory permissions and gives us EACCES/EROFS
	// exactly where those errors happen.
	probe, err := os.CreateTemp(parent, ".bindery-write-probe-*")
	if err != nil {
		return fmt.Errorf("database parent directory %q is not writable: %w (%s)", parent, err, describeDir(parent, info))
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)

	slog.Debug("database preflight ok", "path", abs, "parent", parent, "perm", info.Mode().Perm().String())
	return nil
}

// annotateCantOpen wraps SQLite's SQLITE_CANTOPEN with a one-line remediation
// hint so operators don't have to look up error codes.
func annotateCantOpen(dbPath string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, "unable to open database file") &&
		!strings.Contains(msg, "SQLITE_CANTOPEN") &&
		!errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EROFS) {
		return err
	}
	abs, _ := filepath.Abs(dbPath)
	parent := filepath.Dir(abs)
	info, statErr := os.Stat(parent)
	hint := fmt.Sprintf("check that %q exists and is writable by the container user", parent)
	if statErr == nil {
		hint = describeDir(parent, info)
	}
	return fmt.Errorf("open database %q: %w — %s", abs, err, hint)
}

func setPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("set %s: %w", p, err)
		}
	}
	return nil
}

// migrationVersion parses the canonical version number from a migration
// filename's numeric prefix (e.g. "011_calibre_mode.sql" -> 11). The version
// is the filename number, NOT the position in the sorted slice: the migration
// set has a gap at 010, so index-based numbering would offset every migration
// from 011 onward and silently skip a future 010_*.sql file.
func migrationVersion(filename string) (int, error) {
	prefix, _, ok := strings.Cut(filename, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q has no numeric prefix", filename)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q has non-numeric prefix %q: %w", filename, prefix, err)
	}
	return v, nil
}

func migrate(database *sql.DB) error {
	// Create a migrations tracking table
	_, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Read all migration files
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename (001_, 002_, etc.)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Refuse to start if two migration files share a numeric prefix. The apply
	// loop keys on version number (SELECT COUNT(*) FROM schema_migrations WHERE
	// version = ?) so a second NNN_*.sql file is silently skipped on any DB
	// that already applied the first — the schema change in the skipped file
	// is lost. This bit the 043 collision incident on 2026-05-26; the fail-
	// loud guard prevents recurrence.
	if err := assertUniqueMigrationVersions(entries); err != nil {
		return err
	}

	// Older Bindery builds recorded schema_migrations.version as the 0-based
	// slice index + 1 instead of the filename number. Reconcile any such DB to
	// filename-based versions exactly once, before the apply loop, so neither
	// scheme re-runs nor skips a migration. See reconcileMigrationVersions.
	if err := reconcileMigrationVersions(database, entries); err != nil {
		return err
	}

	for _, entry := range entries {
		v, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}

		// Check if already applied
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", v).Scan(&count); err != nil {
			return fmt.Errorf("check migration %d: %w", v, err)
		}
		if count > 0 {
			continue
		}

		// Pre-flight integrity check before the multi-user migration.
		if entry.Name() == "025_multiuser.sql" {
			if err := multiuserPreFlight(database); err != nil {
				return err
			}
		}

		content, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		if err := applyMigration(database, v, entry.Name(), string(content)); err != nil {
			return err
		}
		slog.Info("applied migration", "version", v, "file", entry.Name())
	}

	// Post-migration Go-side backfill. Migration 051's SQL backfill is a coarse
	// approximation of indexer.CanonicalDedupKey (SQLite cannot run the Go
	// normalizer); recompute the exact key for any row whose stored key is
	// blank or no longer matches the canonical function (#940). Idempotent and
	// cheap: a no-op once every row already holds the canonical key.
	if err := backfillBookDedupKeys(database); err != nil {
		return fmt.Errorf("backfill book dedup keys: %w", err)
	}

	return nil
}

// backfillBookDedupKeys recomputes books.dedup_key for every row whose stored
// value differs from indexer.CanonicalDedupKey(title). It runs on every startup
// after migrations so that:
//   - legacy rows created before migration 051 get a correct key (the SQL
//     backfill only produced a coarse lowercase/subtitle approximation);
//   - a future change to the canonical normalizer re-canonicalizes existing
//     rows on the next boot rather than leaving them permanently stale.
//
// It deliberately does NOT merge duplicate rows — reconciling existing dupes is
// risky (file ownership, edition links, provenance) and is left as a follow-up.
// Writing keys is safe: at worst two pre-existing dupes now share a key, which
// only makes future imports bind to one of them instead of creating a third.
func backfillBookDedupKeys(database *sql.DB) error {
	type pending struct {
		id  int64
		key string
	}
	// Read phase in its own scope so rows is closed (via defer) BEFORE we open
	// the write transaction — holding an open read cursor across Begin() risks
	// a "database is locked" on SQLite.
	updates, err := func() ([]pending, error) {
		rows, err := database.Query("SELECT id, title, COALESCE(dedup_key, '') FROM books")
		if err != nil {
			return nil, fmt.Errorf("read books for dedup backfill: %w", err)
		}
		defer rows.Close()
		var out []pending
		for rows.Next() {
			var id int64
			var title, stored string
			if err := rows.Scan(&id, &title, &stored); err != nil {
				return nil, fmt.Errorf("scan book for dedup backfill: %w", err)
			}
			if want := indexer.CanonicalDedupKey(title); want != stored {
				out = append(out, pending{id: id, key: want})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate books for dedup backfill: %w", err)
		}
		return out, nil
	}()
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin dedup backfill tx: %w", err)
	}
	stmt, err := tx.Prepare("UPDATE books SET dedup_key = ? WHERE id = ?")
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare dedup backfill: %w", err)
	}
	defer stmt.Close()
	for _, u := range updates {
		if _, err := stmt.Exec(u.key, u.id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update dedup_key for book %d: %w", u.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dedup backfill: %w", err)
	}
	slog.Info("backfilled book dedup keys", "rows", len(updates))
	return nil
}

// reconcileMigrationVersions detects a schema_migrations table that was written
// by the legacy index-based runner and rewrites its rows to filename-based
// versions. The legacy runner stored version = (sorted slice index)+1; the
// current runner stores version = the filename numeric prefix. Because the
// migration set has a gap at 010, the two schemes diverge for every migration
// from 011 onward.
//
// The reconciliation is safe and idempotent:
//   - It only fires when the highest recorded version is consistent with the
//     index scheme (<= migration count) AND inconsistent with the filename
//     scheme (a row exists whose version is not a valid filename number).
//   - The index->filename mapping is the sorted slice itself, so it is exact.
//   - Once rewritten, all versions are valid filename numbers and the guard
//     no longer fires.
//
// A fresh database has no schema_migrations rows and is left untouched.
// assertUniqueMigrationVersions returns an error when two migration files share
// the same numeric prefix. Without this guard the apply loop silently skips
// the second file on any DB that already recorded that version, losing its
// schema change. See the 043 collision incident on 2026-05-26.
func assertUniqueMigrationVersions(entries []os.DirEntry) error {
	byVersion := make(map[int]string, len(entries))
	for _, entry := range entries {
		v, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		if existing, ok := byVersion[v]; ok {
			return fmt.Errorf("duplicate migration version %d: %q and %q share the same numeric prefix; rename one before booting", v, existing, entry.Name())
		}
		byVersion[v] = entry.Name()
	}
	return nil
}

func reconcileMigrationVersions(database *sql.DB, entries []os.DirEntry) error {
	// Build the set of valid filename-based versions, and the index->filename
	// mapping (1-based index, matching the legacy +1 numbering).
	filenameVersions := make(map[int]bool, len(entries))
	indexToFilename := make(map[int]int, len(entries))
	for i, entry := range entries {
		fv, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		filenameVersions[fv] = true
		indexToFilename[i+1] = fv
	}

	// Read in a helper so the result set is fully closed before the rewrite
	// transaction below — the pool is single-connection, so an open query
	// would deadlock database.Begin().
	recorded, err := readRecordedVersions(database)
	if err != nil {
		return err
	}

	if len(recorded) == 0 {
		return nil // fresh DB — nothing to reconcile
	}

	// If every recorded version is already a valid filename number, the DB is
	// either fresh-on-new-runner or already reconciled. Leave it alone.
	allFilenameBased := true
	for _, v := range recorded {
		if !filenameVersions[v] {
			allFilenameBased = false
			break
		}
	}
	if allFilenameBased {
		return nil
	}

	// At least one row is not a valid filename version. Only treat it as a
	// legacy index-based DB if every recorded version is a valid index
	// (1..len(entries)); otherwise the table is corrupt in a way we must not
	// silently "fix".
	for _, v := range recorded {
		if v < 1 || v > len(entries) {
			return fmt.Errorf(
				"schema_migrations contains version %d that is neither a valid "+
					"migration filename number nor a valid legacy index (1..%d); "+
					"refusing to reconcile a corrupt migrations table",
				v, len(entries))
		}
	}

	// Every row is a valid legacy index. Rewrite them to filename versions in
	// a single transaction. Rewrite in descending order so an intermediate
	// (index, filename) pair can never collide with a not-yet-rewritten row.
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin migration-version reconciliation: %w", err)
	}
	for i := len(recorded) - 1; i >= 0; i-- {
		oldV := recorded[i]
		newV := indexToFilename[oldV]
		if newV == oldV {
			continue
		}
		if _, err := tx.Exec(
			"UPDATE schema_migrations SET version = ? WHERE version = ?", newV, oldV,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("reconcile migration version %d -> %d: %w", oldV, newV, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration-version reconciliation: %w", err)
	}
	slog.Info("reconciled schema_migrations to filename-based versions", "rows", len(recorded))
	return nil
}

// readRecordedVersions returns every version in schema_migrations, ascending.
// It exists as a separate function so the result set is closed (via defer)
// before any caller starts a write transaction — the pool is single-connection.
func readRecordedVersions(database *sql.DB) ([]int, error) {
	rows, err := database.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations for reconciliation: %w", err)
	}
	defer rows.Close()
	var recorded []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_migrations version: %w", err)
		}
		recorded = append(recorded, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return recorded, nil
}

// applyMigration runs a single migration's statements and records it in
// schema_migrations, all inside one transaction so a crash or error mid-way
// leaves the database unchanged (no partial DDL, no missing version row).
//
// SQLite cannot change PRAGMA foreign_keys inside a transaction, so migrations
// that need FK enforcement disabled for a table rebuild (007, 034) carry a
// bare `PRAGMA foreign_keys=OFF/ON` line. Those lines are stripped from the
// transactional body; instead FK enforcement is toggled OFF before BEGIN and
// unconditionally restored to ON afterwards via defer — so a failed migration
// can never leave FK enforcement off on the pooled connection.
func applyMigration(database *sql.DB, version int, name, content string) (err error) {
	statements, togglesForeignKeys := parseMigration(content)

	if togglesForeignKeys {
		if _, e := database.Exec("PRAGMA foreign_keys=OFF"); e != nil {
			return fmt.Errorf("migration %d: disable foreign keys: %w", version, e)
		}
		// Restore FK enforcement no matter how this function returns. The
		// pooled connection (MaxOpenConns=1) is shared for the process
		// lifetime, so leaving it OFF would silently disable FK checks.
		defer func() {
			if _, e := database.Exec("PRAGMA foreign_keys=ON"); e != nil && err == nil {
				err = fmt.Errorf("migration %d: restore foreign keys: %w", version, e)
			}
		}()
	}

	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("migration %d: begin transaction: %w", version, err)
	}
	// Roll back on any early return. A no-op after a successful Commit.
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, stmt := range statements {
		if _, e := tx.Exec(stmt); e != nil {
			return fmt.Errorf("migration %d statement: %w\nSQL: %s", version, e, stmt)
		}
	}

	if togglesForeignKeys {
		// Documented SQLite table-rebuild ordering: verify referential
		// integrity before committing the FK-disabled rebuild.
		var fkRows int
		row := tx.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check")
		if e := row.Scan(&fkRows); e != nil {
			return fmt.Errorf("migration %d: foreign_key_check: %w", version, e)
		}
		if fkRows > 0 {
			return fmt.Errorf("migration %d: foreign_key_check found %d violation(s)", version, fkRows)
		}
	}

	if _, e := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); e != nil {
		return fmt.Errorf("record migration %d: %w", version, e)
	}

	if e := tx.Commit(); e != nil {
		return fmt.Errorf("migration %d: commit: %w", version, e)
	}
	return nil
}

// parseMigration splits a migration file's "Up" section into individual
// executable statements. Bare `PRAGMA foreign_keys=ON/OFF` statements are
// removed from the returned slice (they cannot run inside a transaction) and
// reported via the second return value so the caller can toggle the pragma
// outside the transaction.
func parseMigration(content string) (statements []string, togglesForeignKeys bool) {
	sqlStr := content
	if idx := strings.Index(sqlStr, "-- +migrate Down"); idx >= 0 {
		sqlStr = sqlStr[:idx]
	}
	sqlStr = strings.Replace(sqlStr, "-- +migrate Up", "", 1)

	for _, stmt := range strings.Split(sqlStr, ";") {
		// Strip comment-only lines
		lines := strings.Split(stmt, "\n")
		var cleaned []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			cleaned = append(cleaned, line)
		}
		stmt = strings.TrimSpace(strings.Join(cleaned, "\n"))
		if stmt == "" {
			continue
		}
		// PRAGMA foreign_keys is a no-op inside a transaction; pull it out of
		// the transactional body and signal the caller to handle it.
		if isForeignKeysPragma(stmt) {
			togglesForeignKeys = true
			continue
		}
		statements = append(statements, stmt)
	}
	return statements, togglesForeignKeys
}

// isForeignKeysPragma reports whether stmt is a bare `PRAGMA foreign_keys=...`
// statement, tolerating case and whitespace variations.
func isForeignKeysPragma(stmt string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(stmt), ""))
	return strings.HasPrefix(normalized, "pragmaforeign_keys=")
}

// multiuserPreFlight checks that the database is in a consistent state before
// the multi-user migration (025) runs. Aborts with a repair hint if data rows
// exist in user-owned tables but there is no user row to own them.
func multiuserPreFlight(database *sql.DB) error {
	// Only check tables that hold exclusively user-created data. quality_profiles
	// and metadata_profiles are seeded by earlier migrations so they may have
	// rows even on a fresh install with no users.
	tables := []string{"authors", "books", "downloads", "root_folders"}
	var userCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		return fmt.Errorf("pre-flight: count users: %w", err)
	}
	if userCount > 0 {
		return nil // at least one user exists; backfill to id=1 is safe
	}
	for _, tbl := range tables {
		var n int
		//nolint:gosec // table name is a static literal from the slice above
		if err := database.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil { //nolint:gosec
			return fmt.Errorf("pre-flight: count %s: %w", tbl, err)
		}
		if n > 0 {
			return fmt.Errorf(
				"025_multiuser.sql pre-flight: table %q has %d row(s) but users table is empty — "+
					"create at least one user before upgrading",
				tbl, n,
			)
		}
	}
	return nil
}
