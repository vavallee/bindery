package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleBackup streams a consistent snapshot of the installs database. Uses
// SQLite's VACUUM INTO so the snapshot is taken under a transaction (no torn
// reads against concurrent ping writes) and lands as a fully self-contained
// file with no WAL sidecar. The endpoint is token-gated identically to
// /api/stats and is intended to be pulled by a scheduled GitHub workflow.
func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.statsToken == "" {
		http.Error(w, "backup endpoint not configured", http.StatusForbidden)
		return
	}
	if !s.statsTokenValid(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Stage the snapshot in the DB's own directory, not os.MkdirTemp's
	// default of /tmp: the container runs with readOnlyRootFilesystem, so
	// /tmp is not writable and MkdirTemp there fails every call. The DB
	// directory is the data volume and is writable by definition.
	dir, err := os.MkdirTemp(s.dbDir, "bindery-backup-*")
	if err != nil {
		slog.Error("backup: mkdir", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			slog.Warn("backup: cleanup tempdir", "error", removeErr)
		}
	}()
	dst := filepath.Join(dir, "telemetry.db")

	// VACUUM INTO acquires a read transaction, copies pages out, and produces
	// a single self-contained SQLite file. Safer than streaming /data/telemetry.db
	// directly (which would race the WAL). Path is parameterized as a literal
	// string in the SQL because VACUUM INTO does not accept bind parameters.
	// The destination path is a server-generated tempdir path with single-quotes
	// escaped, so SQL injection is not possible here.
	vacuumSQL := `VACUUM INTO '` + strings.ReplaceAll(dst, `'`, `''`) + `'` //nolint:gosec // G202: path is server-generated with quotes escaped
	if _, err := s.db.ExecContext(r.Context(),
		vacuumSQL,
	); err != nil {
		slog.Error("backup: vacuum", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(dst) // #nosec G304 — dst is a server-generated tempdir path.
	if err != nil {
		slog.Error("backup: open", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		slog.Error("backup: stat", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := "bindery-telemetry-" + time.Now().UTC().Format("2006-01-02") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	if _, err := io.Copy(w, f); err != nil {
		slog.Warn("backup: write response", "error", err)
	}
}
