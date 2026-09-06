// Package api contains the HTTP handlers served under /api/v1 by the
// chi router. Each file groups handlers for a single resource.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/vavallee/bindery/internal/db"
)

// backupFilenameRe matches files produced by Create: the timestamp name
// "bindery_YYYYMMDD_HHMMSS.db" and the optional-label form
// "bindery_YYYYMMDD_HHMMSS_<label>.db". The charset is restricted to
// [A-Za-z0-9_-] with a fixed prefix and suffix, so Restore/Delete reject any
// path-traversal trick ("..%2Fetc") or unrelated file — the name can contain no
// slash, backslash, or dot other than the ".db" extension. Matching a manually
// renamed backup that stays within this shape is intentional: List shows those
// files, so Restore/Delete must be able to act on them too.
var backupFilenameRe = regexp.MustCompile(`^bindery_[A-Za-z0-9_-]+\.db$`)

// maxBackupLabelLen caps a sanitized backup label so the filename stays a sane
// length.
const maxBackupLabelLen = 40

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

// sanitizeBackupLabel reduces a user-supplied backup label to the safe charset
// allowed inside a backup filename ([A-Za-z0-9_-]): every other character (space,
// slash, dot, unicode) becomes '-', runs of separators collapse, leading and
// trailing separators are trimmed, and the result is capped at maxBackupLabelLen.
// Returns "" when nothing usable remains, in which case the backup keeps its
// plain timestamp name. This is what keeps a label from smuggling a path
// separator or ".." into the filename.
func sanitizeBackupLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	lastSep := false
	for _, r := range raw {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '_' || r == '-':
			if !lastSep {
				b.WriteRune(r)
				lastSep = true
			}
		default:
			if !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	out := strings.Trim(b.String(), "_-")
	if len(out) > maxBackupLabelLen {
		out = strings.Trim(out[:maxBackupLabelLen], "_-")
	}
	return out
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
		writeServerError(w, r, err)
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
// SQLite file with no -wal/-shm sidecar, which is what lets Restore stage it
// as a plain file copy and swap it in at the next start.
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

	label := ""
	if r.Body != nil {
		// Optional {"label": "..."} body — a user-supplied tag folded into the
		// filename so backups can be recognised at a glance (e.g. "pre-import").
		// A missing/empty/malformed body is fine: the backup keeps its plain
		// timestamp name. Cap the read so a huge body can't be forced through.
		var body struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err == nil {
			label = sanitizeBackupLabel(body.Label)
		}
	}

	timestamp := start.UTC().Format("20060102_150405")
	destName := fmt.Sprintf("bindery_%s.db", timestamp)
	if label != "" {
		destName = fmt.Sprintf("bindery_%s_%s.db", timestamp, label)
	}
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
		writeServerError(w, r, err)
		return
	}
	// Match the live DB file's 0600 mode. VACUUM INTO honours umask, which on
	// many distros leaves the file world-readable, exposing bcrypt hashes,
	// session secrets, and the API key.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("backup chmod failed", "error", err)
		writeServerError(w, r, err)
		return
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("backup rename failed", "error", err)
		writeServerError(w, r, err)
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

// Restore stages a backup file to be swapped in at the next start. Dangerous,
// so it requires the X-Confirm-Restore: true header.
//
// It does NOT write the live database. Bindery runs SQLite in WAL mode
// (internal/db setPragmas), so the live database is the main file plus
// <db>-wal and <db>-shm. The previous implementation copied the backup
// straight over the main file while this process still held the pool open,
// which truncated the file underneath open connections and left the old WAL
// intact. SQLite validates WAL frames against the WAL header's salt rather
// than against the main file, so those stale frames stayed valid and were
// replayed over the restored pages at the next checkpoint. An admin was told
// the restore succeeded and then got back a blend of the backup and whatever
// was live, or a file that failed integrity_check.
//
// Instead: verify the backup opens read-only and passes integrity_check and
// quick_check, copy it to <dbPath>.restore-pending, and let
// db.ApplyPendingRestore swap it in on the next start, when there is provably
// no open connection. The response says so, so nobody assumes the running
// process is already serving the restored data.
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

	// Validate before staging, so a corrupt backup is rejected while the admin
	// is still looking at the response rather than on a restart they may not
	// connect to the restore. db.ApplyPendingRestore re-checks at startup: this
	// file sits on disk in between and the check there is what keeps a bad
	// file away from the live database.
	if err := db.CheckSQLiteFile(r.Context(), srcPath); err != nil {
		slog.Warn("restore refused: backup failed its integrity check", "file", filename, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "backup file is not a valid SQLite database, refusing to restore it",
		})
		return
	}

	// Stage under a .tmp name and rename, the same shape Create uses: a
	// process death mid-copy would otherwise leave a half-written pending
	// file that the next start would try to apply.
	pendingPath := db.PendingRestorePath(h.dbPath)
	tmpPath := pendingPath + ".tmp"
	_ = os.Remove(tmpPath)
	if err := copyFile(srcPath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("restore staging failed", "error", err)
		writeServerError(w, r, err)
		return
	}
	// Match the live DB file's mode: the staged copy carries the same bcrypt
	// hashes, session secrets and API key.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("restore staging chmod failed", "error", err)
		writeServerError(w, r, err)
		return
	}
	if err := os.Rename(tmpPath, pendingPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Error("restore staging rename failed", "error", err)
		writeServerError(w, r, err)
		return
	}

	slog.Warn("database restore staged", "file", filename, "pending", pendingPath)
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "restore staged: restart the server to swap this backup in",
	})
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
		writeServerError(w, r, err)
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
