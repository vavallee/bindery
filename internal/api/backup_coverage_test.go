package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupListCoverage pins the listing contract: a missing backups
// directory is an empty list, not an error, and only regular *.db files are
// reported (sidecars, staging files and subdirectories are skipped).
func TestBackupListCoverage(t *testing.T) {
	t.Run("missing directory is empty list", func(t *testing.T) {
		h := NewBackupHandler(nil, "", t.TempDir())
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/backup", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("body = %q, want []", rec.Body.String())
		}
	})

	t.Run("empty directory is empty list", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
			t.Fatal(err)
		}
		h := NewBackupHandler(nil, "", dataDir)
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/backup", nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("status = %d body = %q, want 200 []", rec.Code, rec.Body.String())
		}
	})

	t.Run("filters non-db entries", func(t *testing.T) {
		dataDir := t.TempDir()
		dir := filepath.Join(dataDir, "backups")
		if err := os.MkdirAll(filepath.Join(dir, "nested.db"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"bindery_20260101_000000.db":     "abc",
			"bindery_20260101_000000.db.tmp": "x",
			"notes.txt":                      "x",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		h := NewBackupHandler(nil, "", dataDir)
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/backup", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var got []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "bindery_20260101_000000.db" || got[0].Size != 3 {
			t.Fatalf("listing = %+v, want only the one .db file with size 3", got)
		}
	})

	t.Run("unreadable path is 500", func(t *testing.T) {
		dataDir := t.TempDir()
		// backups exists but is a file, so ReadDir fails with ENOTDIR.
		if err := os.WriteFile(filepath.Join(dataDir, "backups"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewBackupHandler(nil, "", dataDir)
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/backup", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

// TestBackupCreateCoverageFailures covers the failure branches: a backups path
// that cannot be created and a handler without a database. Neither may leave a
// file that List would later report as a backup.
func TestBackupCreateCoverageFailures(t *testing.T) {
	t.Run("backup dir cannot be created", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "backups"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewBackupHandler(nil, "", dataDir)
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/backup", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "failed to create backup directory") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	t.Run("no database handle", func(t *testing.T) {
		dataDir := t.TempDir()
		h := NewBackupHandler(nil, "", dataDir)
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/backup", strings.NewReader(`{"label":"pre import"}`)))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("failed backup left files behind: %v", entries)
		}
	})
}

// TestBackupDeleteAndRestoreCoverageValidation covers the filename and
// confirmation gates on the two destructive endpoints.
func TestBackupDeleteAndRestoreCoverageValidation(t *testing.T) {
	dataDir := t.TempDir()
	h := NewBackupHandler(nil, filepath.Join(dataDir, "bindery.db"), dataDir)

	rec := httptest.NewRecorder()
	h.Delete(rec, backupURLRequest(http.MethodDelete, "bindery_missing.db"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, backupURLRequest(http.MethodDelete, "../bindery.db"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete traversal: status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Restore(rec, backupURLRequest(http.MethodPost, "evil.db"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid backup filename") {
		t.Fatalf("restore bad name: status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Restore(rec, backupURLRequest(http.MethodPost, "bindery_20260101_000000.db"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "X-Confirm-Restore") {
		t.Fatalf("restore unconfirmed: status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = callRestore(t, h, "bindery_20260101_000000.db")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("restore missing: status = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bindery.db.restore-pending")); !os.IsNotExist(err) {
		t.Fatalf("a refused restore staged a pending file: %v", err)
	}
}
