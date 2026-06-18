package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/config"
)

// getStorage drives the handler and decodes the response.
func getStorage(t *testing.T, cfg *config.Config) storageResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	NewStorageHandler(cfg).Get(rec, httptest.NewRequest(http.MethodGet, "/system/storage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got storageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return got
}

func findDir(dirs []dirStatus, name string) (dirStatus, bool) {
	for _, d := range dirs {
		if d.Name == name {
			return d, true
		}
	}
	return dirStatus{}, false
}

func TestStorageHandler_HealthForWritableDir(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{DownloadDir: tmp, LibraryDir: tmp}
	got := getStorage(t, cfg)

	d, ok := findDir(got.Dirs, "library")
	if !ok {
		t.Fatalf("library dir missing from %+v", got.Dirs)
	}
	if !d.Exists || !d.Writable {
		t.Errorf("writable temp dir: exists=%v writable=%v reason=%q, want both true", d.Exists, d.Writable, d.Reason)
	}
	if d.Reason != "" {
		t.Errorf("healthy dir should have empty reason, got %q", d.Reason)
	}
}

func TestStorageHandler_HealthForMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := &config.Config{DownloadDir: "/d", LibraryDir: missing}
	got := getStorage(t, cfg)

	d, ok := findDir(got.Dirs, "library")
	if !ok {
		t.Fatalf("library dir missing from %+v", got.Dirs)
	}
	if d.Exists {
		t.Errorf("nonexistent path reported exists=true: %+v", d)
	}
	if d.Reason == "" {
		t.Error("nonexistent path should carry a failing reason")
	}
}

func TestStorageHandler_HardlinkableSameRoot(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{DownloadDir: tmp, LibraryDir: filepath.Join(tmp, "lib")}
	got := getStorage(t, cfg)
	// download dir and library share the same temp root, so they are on the
	// same device and hardlink-able.
	if !got.Hardlinkable {
		t.Errorf("expected hardlinkable=true for paths sharing %s", tmp)
	}
}

func TestStorageHandler_HardlinkableFieldPresentForMissingPaths(t *testing.T) {
	// When a path can't be stat'd, sameDevice returns false; assert the field
	// is a definite boolean (best-effort cross-filesystem coverage).
	cfg := &config.Config{DownloadDir: "/nonexistent-aaa", LibraryDir: "/nonexistent-bbb"}
	got := getStorage(t, cfg)
	if got.Hardlinkable {
		t.Errorf("unstattable paths must not report hardlinkable=true")
	}
}

func TestStorageHandler_AudiobookDirsOmittedWhenUnset(t *testing.T) {
	cfg := &config.Config{DownloadDir: "/d", LibraryDir: "/l"}
	got := getStorage(t, cfg)
	if _, ok := findDir(got.Dirs, "audiobook"); ok {
		t.Error("audiobook dir should be omitted when unset")
	}
	if _, ok := findDir(got.Dirs, "audiobook-download"); ok {
		t.Error("audiobook-download dir should be omitted when unset")
	}
	if len(got.Dirs) != 2 {
		t.Errorf("expected 2 dirs (download, library), got %d: %+v", len(got.Dirs), got.Dirs)
	}
}

func TestStorageHandler_Get(t *testing.T) {
	cfg := &config.Config{
		DownloadDir:          "/downloads",
		AudiobookDownloadDir: "/audiobook-downloads",
		LibraryDir:           "/books",
		AudiobookDir:         "/audiobooks",
	}
	h := NewStorageHandler(cfg)

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/system/storage", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got storageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.DownloadDir != "/downloads" || got.AudiobookDownloadDir != "/audiobook-downloads" ||
		got.LibraryDir != "/books" || got.AudiobookDir != "/audiobooks" {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestStorageHandler_EmptyAudiobookDirPassesThrough(t *testing.T) {
	cfg := &config.Config{DownloadDir: "/d", LibraryDir: "/l", AudiobookDir: ""}
	h := NewStorageHandler(cfg)

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/system/storage", nil))

	var got storageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AudiobookDir != "" {
		t.Errorf("AudiobookDir = %q, want empty so the UI can fall back to LibraryDir", got.AudiobookDir)
	}
}

func TestStorageHandler_EmptyAudiobookDownloadDirPassesThrough(t *testing.T) {
	cfg := &config.Config{DownloadDir: "/d", AudiobookDownloadDir: "", LibraryDir: "/l", AudiobookDir: ""}
	h := NewStorageHandler(cfg)

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/system/storage", nil))

	var got storageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AudiobookDownloadDir != "" {
		t.Errorf("AudiobookDownloadDir = %q, want empty so the UI can fall back to DownloadDir", got.AudiobookDownloadDir)
	}
}
