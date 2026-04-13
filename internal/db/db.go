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
	"strings"
	"syscall"

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

	slog.Info("database ready", "path", dbPath)
	return db, nil
}

// OpenMemory creates an in-memory database for testing.
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open memory database: %w", err)
	}

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

// describeDir renders the parent directory's permissions + ownership in a
// form most Linux operators read fluently (matches `ls -ld`'s mode column
// plus a "uid:gid" tail). We skip username lookup to keep this cheap and
// dependency-free; numeric IDs are unambiguous anyway.
func describeDir(path string, info os.FileInfo) string {
	uid, gid := "?", "?"
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid = fmt.Sprintf("%d", st.Uid)
		gid = fmt.Sprintf("%d", st.Gid)
	}
	return fmt.Sprintf("%s mode=%s owner=%s:%s — ensure this directory is writable by the UID running bindery (distroless nonroot is 65532)",
		path, info.Mode().Perm().String(), uid, gid)
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

	for version, entry := range entries {
		v := version + 1 // 1-indexed

		// Check if already applied
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", v).Scan(&count); err != nil {
			return fmt.Errorf("check migration %d: %w", v, err)
		}
		if count > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		sqlStr := string(content)
		if idx := strings.Index(sqlStr, "-- +migrate Down"); idx >= 0 {
			sqlStr = sqlStr[:idx]
		}
		sqlStr = strings.Replace(sqlStr, "-- +migrate Up", "", 1)

		// Execute each statement individually
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
			if _, err := database.Exec(stmt); err != nil {
				return fmt.Errorf("migration %d statement: %w\nSQL: %s", v, err, stmt)
			}
		}

		if _, err := database.Exec("INSERT INTO schema_migrations (version) VALUES (?)", v); err != nil {
			return fmt.Errorf("record migration %d: %w", v, err)
		}
		slog.Info("applied migration", "version", v, "file", entry.Name())
	}

	return nil
}
