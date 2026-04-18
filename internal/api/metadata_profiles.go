package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type MetadataProfileHandler struct {
	repo *db.MetadataProfileRepo
}

func NewMetadataProfileHandler(repo *db.MetadataProfileRepo) *MetadataProfileHandler {
	return &MetadataProfileHandler{repo: repo}
}

func (h *MetadataProfileHandler) List(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.repo.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if profiles == nil {
		profiles = []models.MetadataProfile{}
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (h *MetadataProfileHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metadata profile not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *MetadataProfileHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.MetadataProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if p.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if p.AllowedLanguages == "" {
		p.AllowedLanguages = "eng"
	}
	if p.UnknownLanguageBehavior != models.UnknownLanguageFail {
		p.UnknownLanguageBehavior = models.UnknownLanguagePass
	}
	if err := h.repo.Create(r.Context(), &p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *MetadataProfileHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metadata profile not found"})
		return
	}
	var p models.MetadataProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	p.ID = id
	if p.UnknownLanguageBehavior != models.UnknownLanguageFail {
		p.UnknownLanguageBehavior = models.UnknownLanguagePass
	}
	if err := h.repo.Update(r.Context(), &p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *MetadataProfileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
