package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type CustomFormatHandler struct {
	repo *db.CustomFormatRepo
}

func NewCustomFormatHandler(repo *db.CustomFormatRepo) *CustomFormatHandler {
	return &CustomFormatHandler{repo: repo}
}

func (h *CustomFormatHandler) List(w http.ResponseWriter, r *http.Request) {
	formats, err := h.repo.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if formats == nil {
		formats = []models.CustomFormat{}
	}
	writeJSON(w, http.StatusOK, formats)
}

func (h *CustomFormatHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	cf, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if cf == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "custom format not found"})
		return
	}
	writeJSON(w, http.StatusOK, cf)
}

func (h *CustomFormatHandler) Create(w http.ResponseWriter, r *http.Request) {
	var cf models.CustomFormat
	if err := json.NewDecoder(r.Body).Decode(&cf); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if cf.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if cf.Conditions == nil {
		cf.Conditions = []models.CustomCondition{}
	}
	if err := h.repo.Create(r.Context(), &cf); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, cf)
}

func (h *CustomFormatHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "custom format not found"})
		return
	}
	var cf models.CustomFormat
	if err := json.NewDecoder(r.Body).Decode(&cf); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	cf.ID = id
	if cf.Conditions == nil {
		cf.Conditions = []models.CustomCondition{}
	}
	if err := h.repo.Update(r.Context(), &cf); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cf)
}

func (h *CustomFormatHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
