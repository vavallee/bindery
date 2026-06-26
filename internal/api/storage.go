package api

import (
	"net/http"

	"github.com/vavallee/bindery/internal/config"
)

// StorageHandler exposes the process-level storage paths loaded from
// environment / config file so the Settings UI can display them without
// asking the user to `docker exec` into the container, together with the
// existence / writability / hardlink-ability health Bindery already computes
// at startup (it previously only logged it). See #1183.
//
// These values are intentionally read-only: the importer captures them at
// startup, so mutating them via the API would drift from the running
// process. Per-library root folders (the editable ones) live under
// /api/v1/rootfolder.
type StorageHandler struct {
	cfg *config.Config
}

func NewStorageHandler(cfg *config.Config) *StorageHandler {
	return &StorageHandler{cfg: cfg}
}

// dirStatus is the per-directory health entry surfaced to the UI.
type dirStatus struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Writable bool   `json:"writable"`
	// Reason is a short, user-facing explanation when the directory is
	// missing or not writable; empty when healthy.
	Reason string `json:"reason,omitempty"`
}

type storageResponse struct {
	DownloadDir          string `json:"downloadDir"`
	AudiobookDownloadDir string `json:"audiobookDownloadDir"`
	LibraryDir           string `json:"libraryDir"`
	AudiobookDir         string `json:"audiobookDir"`

	// Dirs lists the effective directories with their health. Audiobook dirs
	// are omitted when unset (they fall back to the library / download dir).
	Dirs []dirStatus `json:"dirs"`
	// Hardlinkable reports whether the download dir and library dir share a
	// filesystem, so completed downloads can be hard-linked into the library
	// instead of copied. False means imports will copy (slower, double disk).
	Hardlinkable bool `json:"hardlinkable"`
}

// statusFor builds a dirStatus from config.CheckDir.
func statusFor(name, path string) dirStatus {
	h := config.CheckDir(path)
	return dirStatus{
		Name:     name,
		Path:     path,
		Exists:   h.Exists,
		Writable: h.Writable,
		Reason:   h.Reason,
	}
}

// Get handles GET /api/v1/system/storage.
func (h *StorageHandler) Get(w http.ResponseWriter, _ *http.Request) {
	cfg := h.cfg

	dirs := []dirStatus{
		statusFor("download", cfg.DownloadDir),
		statusFor("library", cfg.LibraryDir),
	}
	// Audiobook dirs are optional and fall back to library / download dir; only
	// report them as distinct rows when explicitly configured.
	if cfg.AudiobookDir != "" {
		dirs = append(dirs, statusFor("audiobook", cfg.AudiobookDir))
	}
	if cfg.AudiobookDownloadDir != "" {
		dirs = append(dirs, statusFor("audiobook-download", cfg.AudiobookDownloadDir))
	}

	writeJSON(w, http.StatusOK, storageResponse{
		DownloadDir:          cfg.DownloadDir,
		AudiobookDownloadDir: cfg.AudiobookDownloadDir,
		LibraryDir:           cfg.LibraryDir,
		AudiobookDir:         cfg.AudiobookDir,
		Dirs:                 dirs,
		Hardlinkable:         hardlinkable(cfg.DownloadDir, cfg.LibraryDir),
	})
}
