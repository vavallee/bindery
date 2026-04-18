package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/prowlarr"
)

type ProwlarrHandler struct {
	instances *db.ProwlarrRepo
	indexers  *db.IndexerRepo
}

func NewProwlarrHandler(instances *db.ProwlarrRepo, indexers *db.IndexerRepo) *ProwlarrHandler {
	return &ProwlarrHandler{instances: instances, indexers: indexers}
}

func (h *ProwlarrHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.instances.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if items == nil {
		items = []models.ProwlarrInstance{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *ProwlarrHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.instances.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *ProwlarrHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.ProwlarrInstance
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if p.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	if p.Name == "" {
		p.Name = "Prowlarr"
	}
	if err := h.instances.Create(r.Context(), &p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *ProwlarrHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.instances.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err := json.NewDecoder(r.Body).Decode(existing); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	existing.ID = id
	if err := h.instances.Update(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (h *ProwlarrHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	// Remove synced indexers before deleting the instance (FK constraint).
	if err := h.indexers.DeleteByProwlarrInstance(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.instances.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProwlarrHandler) Test(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.instances.GetByID(r.Context(), id)
	if err != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	client := prowlarr.New(p.URL, p.APIKey)
	version, err := client.Test(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "false", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "version": version})
}

func (h *ProwlarrHandler) Sync(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.instances.GetByID(r.Context(), id)
	if err != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	client := prowlarr.New(p.URL, p.APIKey)
	syncer := prowlarr.NewSyncer(client, h.indexers, h.instances)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := syncer.Sync(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added":   result.Added,
		"updated": result.Updated,
		"removed": result.Removed,
	})
}
