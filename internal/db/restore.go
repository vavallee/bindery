package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// RestorePendingSuffix names the staging file a backup restore writes beside
// the live database. internal/api's restore handler copies the chosen backup
// to <dbPath>+RestorePendingSuffix; ApplyPendingRestore swaps it in on the
// next start, before anything opens the pool.
//
// The staging step exists because the obvious implementation is wrong in a
// way that is invisible until the next restart. Bindery runs SQLite in WAL
// mode (see setPragmas), so the live database is three files: the main file,
// <db>-wal and <db>-shm. Copying a backup straight over the main file while
// the process holds the pool open truncates the main file underneath open
// connections, and leaves the old WAL in place. SQLite validates WAL frames
// against the WAL header's own salt, not against the main file, so those
// stale frames are still "valid" and get replayed over the restored pages at
// the next checkpoint. The result is a database that is part backup, part
// whatever was live, or one that no longer passes integrity_check. Writes
// that land between the restore and the restart go into that same WAL and
// survive it too.
//
// Staging sidesteps all of that: nothing touches the live database while the
// process is running, and the swap happens at the one moment when there is
// provably no open connection and no meaningful WAL.
const RestorePendingSuffix = ".restore-pending"

// RestoreFailedSuffix names where a staged restore is parked when it fails
// its startup check. Keeping the file (rather than deleting it) lets an
// operator inspect what they tried to restore; renaming it (rather than
// leaving it in place) stops every subsequent start retrying the same bad
// file forever.
const RestoreFailedSuffix = ".restore-failed"

// sqliteHeaderMagic is the first 16 bytes of every SQLite database file.
var sqliteHeaderMagic = []byte("SQLite format 3\x00")

// PendingRestorePath returns the staging path for dbPath.
func PendingRestorePath(dbPath string) string { return dbPath + RestorePendingSuffix }

// sqliteFileURI builds a `file:` DSN for path. The `file:` prefix is required:
// modernc.org/sqlite only forwards DSN query parameters to sqlite3_open_v2
// when the DSN is a URI, and truncates the DSN at the first "?" otherwise, so
// a plain "<path>?mode=ro" silently opens read-write and creates the file if
// it is missing. "?", "#" and "%" are percent-encoded because SQLite's own URI
// parser gives them syntactic meaning inside the path.
func sqliteFileURI(path, query string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return "file:" + r.Replace(path) + "?" + query
}

// CheckSQLiteFile reports whether path is a readable, structurally sound
// SQLite database. It opens the file read-only (so a check can never create,
// modify or recover the file it is inspecting) and runs both integrity_check
// and quick_check.
//
// Running both is deliberate rather than redundant: quick_check skips the
// index-content verification integrity_check does, so it is the cheaper of
// the two, but integrity_check on a large database can return "ok" for a file
// whose freelist quick_check flags. Two passes over a file about to replace
// the live database is a cheap price.
//
// The header check in front of the pragmas closes the empty-file hole:
// SQLite treats a zero-byte file as a valid empty database and answers "ok"
// to both pragmas, so a truncated-to-nothing copy would otherwise pass.
func CheckSQLiteFile(ctx context.Context, path string) error {
	f, err := os.Open(path) // #nosec G304 -- path is either <dbPath>+RestorePendingSuffix or a backups/ entry already matched against backupFilenameRe; never a raw request value
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	header := make([]byte, len(sqliteHeaderMagic))
	n, err := f.Read(header)
	_ = f.Close()
	if err != nil || n != len(sqliteHeaderMagic) || string(header) != string(sqliteHeaderMagic) {
		return fmt.Errorf("%q is not a SQLite database file", path)
	}

	database, err := sql.Open("sqlite", sqliteFileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return fmt.Errorf("open %q read-only: %w", path, err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	for _, pragma := range []string{"PRAGMA integrity_check", "PRAGMA quick_check"} {
		var result string
		if err := database.QueryRowContext(ctx, pragma).Scan(&result); err != nil {
			return fmt.Errorf("%s on %q: %w", pragma, path, err)
		}
		if !strings.EqualFold(result, "ok") {
			return fmt.Errorf("%s on %q reported %q", pragma, path, result)
		}
	}
	return nil
}

// ApplyPendingRestore swaps a staged backup into place, if one is waiting.
// Open calls it before it opens the pool; that ordering is the whole point,
// so any other caller must do the same.
//
// A staged file that does not survive CheckSQLiteFile is moved aside to
// <dbPath>+RestoreFailedSuffix and startup continues on the untouched live
// database. Refusing to start would be the wrong call: a bad staged file
// would then brick the instance, and the live database is by definition still
// fine, since nothing has written to it.
//
// The sidecars go before the rename, not after. A crash in between leaves the
// old main file with no WAL (losing at most the uncheckpointed tail, which
// the restore was about to discard anyway) and the staged file still present,
// so the next start finishes the job. Renaming first and crashing before the
// sidecar removal would leave the restored file next to a WAL belonging to
// the database it replaced, which is the corruption this whole path exists to
// avoid.
func ApplyPendingRestore(dbPath string) error {
	pending := PendingRestorePath(dbPath)
	info, err := os.Stat(pending)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat pending restore %q: %w", pending, err)
	}
	if info.IsDir() {
		return fmt.Errorf("pending restore %q is a directory", pending)
	}

	// context.Background() rather than a caller-supplied context: this runs
	// before Open returns, so there is nothing to cancel it from, and a
	// half-applied restore is worse than a slow one.
	if checkErr := CheckSQLiteFile(context.Background(), pending); checkErr != nil {
		failed := dbPath + RestoreFailedSuffix
		_ = os.Remove(failed)
		if renameErr := os.Rename(pending, failed); renameErr != nil {
			slog.Error("pending database restore failed its check and could not be moved aside",
				"pending", pending, "error", checkErr, "rename_error", renameErr)
			return nil
		}
		slog.Error("pending database restore failed its check; live database left untouched",
			"pending", pending, "moved_to", failed, "error", checkErr)
		return nil
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q before restore: %w", dbPath+suffix, err)
		}
	}
	if err := os.Rename(pending, dbPath); err != nil {
		return fmt.Errorf("swap pending restore %q into %q: %w", pending, dbPath, err)
	}
	// The database holds bcrypt hashes, session secrets and the API key.
	// The staged copy was written 0600 too, but re-assert it so a file that
	// arrived by some other route cannot widen the live mode.
	if err := os.Chmod(dbPath, 0o600); err != nil {
		slog.Warn("chmod restored database file", "path", dbPath, "error", err)
	}
	slog.Warn("applied pending database restore", "path", dbPath, "bytes", info.Size())
	return nil
}
