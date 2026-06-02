// Package api contains the HTTP handlers served under /api/v1 by the
// chi router. Each file groups handlers for a single resource.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// backupFilenameRe matches files produced by Create, "bindery_YYYYMMDD_HHMMSS.db".
// Restore/Delete reject anything else so path-traversal tricks like "..%2Fetc"
// or unrelated files under the backup dir can't be referenced by untrusted input.
var backupFilenameRe = regexp.MustCompile(`^bindery_\d{8}_\d{6}\.db$`)

type BackupHandler struct {
	db      *sql.DB
	dbPath  string
	dataDir string
}

// NewBackupHandler wires the backup endpoints. The *sql.DB is required so
// Create can take a WAL-consistent snapshot via VACUUM INTO instead of a
// naive file copy of the main database file (which would miss any writes
// still resident in the WAL).
func NewBackupHandler(database *sql.DB, dbPath, dataDir string) *BackupHandler {
	return &BackupHandler{db: database, dbPath: dbPath, dataDir: dataDir}
}

func (h *BackupHandler) backupDir() string {
	return filepath.Join(h.dataDir, "backups")
}

// List returns all backup files in the backup directory.
func (h *BackupHandler) List(w http.ResponseWriter, r *http.Request) {
	dir := h.backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []map[string]any{})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	type backupFile struct {
		Name    string    `json:"name"`
		Size    int64     `json:"size"`
		ModTime time.Time `json:"modTime"`
	}

	var files []backupFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, backupFile{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	if files == nil {
		files = []backupFile{}
	}
	writeJSON(w, http.StatusOK, files)
}

// Create writes a WAL-consistent snapshot of the live SQLite database into
// the backups directory.
//
// The previous implementation copied h.dbPath byte-for-byte. SQLite runs in
// WAL mode (see internal/db/db.go setPragmas), which means every write goes
// into <db>-wal first and only migrates into the main file on checkpoint.
// A file copy of the main file therefore silently omits any write that has
// not yet been checkpointed: a user restoring such a backup loses recent
// data with no warning. VACUUM INTO reads the live database through SQLite's
// query layer (so it sees the WAL pages), and writes a fresh, self-contained
// database file in a single read transaction. The output is a regular
// SQLite file with no -wal/-shm sidecar, so the existing Restore path (a
// file copy back to h.dbPath) keeps working.
//
// Cost: VACUUM INTO rebuilds the database into a new file, so it is O(db size)
// rather than the O(file size) of the old copy. For a multi-gigabyte library
// this is measured in seconds, not milliseconds. The endpoint is user-initiated
// (no scheduled job) so the cost lands on the user who pressed the button.
func (h *BackupHandler) Create(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	dir := h.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create backup directory"})
		return
	}

	timestamp := start.UTC().Format("20060102_150405")
	destName := fmt.Sprintf("bindery_%s.db", timestamp)
	destPath := filepath.Join(dir, destName)
	// Stage the snapshot under a .tmp name and rename on success. SQLite's
	// VACUUM INTO refuses to overwrite an existing file, and an aborted
	// vacuum (process crash, disk full) on the final path would leave a
	// partial file masquerading as a real backup. The rename is atomic on
	// POSIX so observers either see the old absence or the complete file.
	tmpPath := destPath + ".tmp"
	// Defensive: a previous failed Create in the same UTC second would leave
	// the .tmp lying around and re-fail this call with SQLITE_ERROR.
	_ = os.Remove(tmpPath)

	if err := h.vacuumInto(r.Context(), tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("backup failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed: " + err.Error()})
		return
	}
	// Match the live DB file's 0600 mode. VACUUM INTO honours umask, which on
	// many distros leaves the file world-readable, exposing bcrypt hashes,
	// session secrets, and the API key.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("backup chmod failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed: " + err.Error()})
		return
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("backup rename failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed: " + err.Error()})
		return
	}

	info, _ := os.Stat(destPath)
	var size int64
	if info != nil {
		size = info.Size()
	}

	// Log duration so an operator can see when backup cost is creeping up.
	// VACUUM INTO is O(db size) because it rewrites the entire database into
	// a fresh file (this is the correctness trade vs. the prior plain file
	// copy that silently omitted WAL pages). On a few-hundred-MB database
	// this is sub-second; on multi-GB databases it can be tens of seconds.
	// If anyone later wires the endpoint to a scheduled job, the duration
	// log is the heads-up to size the schedule appropriately.
	duration := time.Since(start)
	slog.Info("backup created", "file", destName, "size", size, "duration_ms", duration.Milliseconds())
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":       destName,
		"size":       size,
		"modTime":    time.Now().UTC(),
		"durationMs": duration.Milliseconds(),
	})
}

// vacuumInto runs `VACUUM INTO 'dst'` against the live database. SQLite does
// not accept bind parameters in VACUUM INTO, so the destination is embedded
// in the SQL with single-quotes escaped. dst is a server-generated path
// rooted at h.backupDir(), never user input, so injection is not possible.
func (h *BackupHandler) vacuumInto(ctx context.Context, dst string) error {
	if h.db == nil {
		return fmt.Errorf("backup handler has no database handle")
	}
	stmt := `VACUUM INTO '` + strings.ReplaceAll(dst, `'`, `''`) + `'` // #nosec G202 -- dst is server-generated (backupDir + timestamp), never user input; SQLite refuses bind params in VACUUM INTO so interpolation is required
	if _, err := h.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("vacuum into %q: %w", dst, err)
	}
	return nil
}

// Restore copies a backup file back to the DB path. Dangerous — requires confirmation header.
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if !backupFilenameRe.MatchString(filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid backup filename"})
		return
	}

	if r.Header.Get("X-Confirm-Restore") != "true" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "set X-Confirm-Restore: true header to confirm restore",
		})
		return
	}

	srcPath := filepath.Join(h.backupDir(), filename)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup file not found"})
		return
	}

	if err := copyFile(srcPath, h.dbPath); err != nil {
		slog.Error("restore failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "restore failed: " + err.Error()})
		return
	}

	slog.Warn("database restored from backup", "file", filename)
	writeJSON(w, http.StatusOK, map[string]string{"message": "database restored — restart the server"})
}

// Delete removes a backup file.
func (h *BackupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if !backupFilenameRe.MatchString(filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid backup filename"})
		return
	}

	path := filepath.Join(h.backupDir(), filename)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return out.Sync()
}
