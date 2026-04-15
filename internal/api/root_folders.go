package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type RootFolderHandler struct {
	folders *db.RootFolderRepo
}

func NewRootFolderHandler(folders *db.RootFolderRepo) *RootFolderHandler {
	return &RootFolderHandler{folders: folders}
}

func (h *RootFolderHandler) List(w http.ResponseWriter, r *http.Request) {
	folders, err := h.folders.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if folders == nil {
		folders = []models.RootFolder{}
	}
	// Refresh free-space figures on every list call so the UI always sees current disk state.
	for i := range folders {
		if free, err := freeSpace(folders[i].Path); err == nil {
			folders[i].FreeSpace = free
			_ = h.folders.UpdateFreeSpace(r.Context(), folders[i].ID, free)
		}
	}
	writeJSON(w, http.StatusOK, folders)
}

func (h *RootFolderHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path does not exist or is not accessible: " + err.Error()})
		return
	}
	if !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is not a directory"})
		return
	}

	folder, err := h.folders.Create(r.Context(), req.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if free, err := freeSpace(folder.Path); err == nil {
		folder.FreeSpace = free
		_ = h.folders.UpdateFreeSpace(r.Context(), folder.ID, free)
	}

	writeJSON(w, http.StatusCreated, folder)
}

func (h *RootFolderHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.folders.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// freeSpace returns the available bytes on the filesystem hosting path.
func freeSpace(path string) (int64, error) {
	return statFreeSpace(path)
}
