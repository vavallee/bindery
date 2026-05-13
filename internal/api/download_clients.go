package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// sanitizeHost strips any scheme prefix a user may have accidentally included
// (e.g. "http://192.168.1.50" → "192.168.1.50"). The Host field expects a
// bare hostname or IP; the scheme is determined by the UseSSL flag.
func sanitizeHost(host string) string {
	if after, ok := strings.CutPrefix(host, "https://"); ok {
		return after
	}
	if after, ok := strings.CutPrefix(host, "http://"); ok {
		return after
	}
	return host
}

// downloadClientURL assembles the effective URL that would be hit for a
// download client, so httpsec.ValidateOutboundURL can check it.
func downloadClientURL(c *models.DownloadClient) string {
	scheme := "http"
	if c.UseSSL {
		scheme = "https"
	}
	port := c.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("%s://%s:%d/", scheme, c.Host, port)
}

type DownloadClientHandler struct {
	clients *db.DownloadClientRepo
}

func NewDownloadClientHandler(clients *db.DownloadClientRepo) *DownloadClientHandler {
	return &DownloadClientHandler{clients: clients}
}

func (h *DownloadClientHandler) List(w http.ResponseWriter, r *http.Request) {
	clients, err := h.clients.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if clients == nil {
		clients = []models.DownloadClient{}
	}
	writeJSON(w, http.StatusOK, clients)
}

func (h *DownloadClientHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	client, err := h.clients.GetByID(r.Context(), id)
	if err != nil || client == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}
	writeJSON(w, http.StatusOK, client)
}

func (h *DownloadClientHandler) Create(w http.ResponseWriter, r *http.Request) {
	var c models.DownloadClient
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if c.Name == "" || c.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and host required"})
		return
	}
	c.Host = sanitizeHost(c.Host)
	if c.Type == "" {
		c.Type = "sabnzbd"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	if c.Category == "" {
		c.Category = "books"
	}
	if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLAN); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.clients.Create(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *DownloadClientHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	existing, err := h.clients.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}

	var c models.DownloadClient
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if c.Host != "" {
		c.Host = sanitizeHost(c.Host)
		if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLAN); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	c.ID = id
	if err := h.clients.Update(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *DownloadClientHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.clients.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DownloadClientHandler) Test(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	client, err := h.clients.GetByID(r.Context(), id)
	if err != nil || client == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}
	if err := downloader.TestClient(r.Context(), client); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Connection verified"})
}
