package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type DelayProfileHandler struct {
	repo *db.DelayProfileRepo
}

func NewDelayProfileHandler(repo *db.DelayProfileRepo) *DelayProfileHandler {
	return &DelayProfileHandler{repo: repo}
}

func (h *DelayProfileHandler) List(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.repo.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if profiles == nil {
		profiles = []models.DelayProfile{}
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (h *DelayProfileHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delay profile not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *DelayProfileHandler) Create(w http.ResponseWriter, r *http.Request) {
	var p models.DelayProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if p.PreferredProtocol == "" {
		p.PreferredProtocol = "usenet"
	}
	if err := h.repo.Create(r.Context(), &p); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *DelayProfileHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delay profile not found"})
		return
	}
	var p models.DelayProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	p.ID = id
	if err := h.repo.Update(r.Context(), &p); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *DelayProfileHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
