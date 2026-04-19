// Package api contains the HTTP handlers served under /api/v1 by the
// chi router. Each file groups handlers for a single resource.
package api

import (
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

// backupFilenameRe matches files produced by Create — "bindery_YYYYMMDD_HHMMSS.db".
// Restore/Delete reject anything else so path-traversal tricks like "..%2Fetc"
// or unrelated files under the backup dir can't be referenced by untrusted input.
var backupFilenameRe = regexp.MustCompile(`^bindery_\d{8}_\d{6}\.db$`)

type BackupHandler struct {
	dbPath  string
	dataDir string
}

func NewBackupHandler(dbPath, dataDir string) *BackupHandler {
	return &BackupHandler{dbPath: dbPath, dataDir: dataDir}
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

// Create copies the current SQLite DB to the backups directory.
func (h *BackupHandler) Create(w http.ResponseWriter, r *http.Request) {
	dir := h.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create backup directory"})
		return
	}

	timestamp := time.Now().UTC().Format("20060102_150405")
	destName := fmt.Sprintf("bindery_%s.db", timestamp)
	destPath := filepath.Join(dir, destName)

	if err := copyFile(h.dbPath, destPath); err != nil {
		slog.Error("backup failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed: " + err.Error()})
		return
	}

	info, _ := os.Stat(destPath)
	var size int64
	if info != nil {
		size = info.Size()
	}

	slog.Info("backup created", "file", destName, "size", size)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":    destName,
		"size":    size,
		"modTime": time.Now().UTC(),
	})
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
