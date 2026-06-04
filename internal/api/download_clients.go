package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	clients              *db.DownloadClientRepo
	health               *downloader.HealthStore
	downloadDir          string
	audiobookDownloadDir string

	// lifetimeCtx is the process-lifecycle context, cancelled on server
	// shutdown so the health-probe goroutine fired by Create/Update does
	// not outlive the process. Falls back to context.Background(); see #846.
	lifetimeCtx context.Context
}

func NewDownloadClientHandler(clients *db.DownloadClientRepo) *DownloadClientHandler {
	return &DownloadClientHandler{clients: clients}
}

// WithLifetimeCtx attaches the process-lifecycle context so the async
// health-probe goroutines respect shutdown.
func (h *DownloadClientHandler) WithLifetimeCtx(ctx context.Context) *DownloadClientHandler {
	if ctx != nil {
		h.lifetimeCtx = ctx
	}
	return h
}

// bgCtx returns the lifetime context if set, otherwise context.Background().
func (h *DownloadClientHandler) bgCtx() context.Context {
	if h.lifetimeCtx != nil {
		return h.lifetimeCtx
	}
	return context.Background()
}

func (h *DownloadClientHandler) WithHealth(store *downloader.HealthStore) *DownloadClientHandler {
	h.health = store
	return h
}

func (h *DownloadClientHandler) WithStoragePaths(downloadDir, audiobookDownloadDir string) *DownloadClientHandler {
	h.downloadDir = downloadDir
	h.audiobookDownloadDir = audiobookDownloadDir
	return h
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
	h.attachHealth(clients)
	writeJSON(w, http.StatusOK, clients)
}

func (h *DownloadClientHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	client, err := h.clients.GetByID(r.Context(), id)
	if err != nil || client == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}
	h.attachClientHealth(client)
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
	if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.clients.Create(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	h.refreshClientHealthAsync(c)
	h.attachClientHealth(&c)
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
		if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLANLoopback); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	c.ID = id
	if err := h.clients.Update(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Evict the pooled downloader client so the next poll picks up the new
	// credentials immediately. Without this, the scanner would keep using
	// the cached pre-update client (and its now-stale session cookie) until
	// the remote service rejected a request, at which point the per-client
	// re-Login path would burn an extra round-trip. (Wave 3 finding 10.)
	downloader.Evict(id)
	h.refreshClientHealthAsync(c)
	h.attachClientHealth(&c)
	writeJSON(w, http.StatusOK, c)
}

func (h *DownloadClientHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.clients.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Drop the pooled client so its session/cookies and idle connections
	// are released rather than lingering until http.Transport's
	// IdleConnTimeout fires. (Wave 3 finding 10.)
	downloader.Evict(id)
	if h.health != nil {
		h.health.Delete(id)
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
	if err := httpsec.ValidateOutboundURL(downloadClientURL(client), httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := downloader.TestClient(r.Context(), client); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	health := h.refreshClientHealth(r.Context(), client)
	resp := struct {
		Message string                       `json:"message"`
		Health  *models.DownloadClientHealth `json:"health,omitempty"`
	}{
		Message: "Connection verified",
		Health:  health,
	}
	writeJSON(w, http.StatusOK, resp)
}

// TestConfig probes a download-client configuration supplied in the request
// body without persisting it. This backs the inline "Test" button on the
// Add/Edit forms so a user can verify host/port/credentials before saving.
// The response shape mirrors Test (test-by-id) so the UI reuses one path.
func (h *DownloadClientHandler) TestConfig(w http.ResponseWriter, r *http.Request) {
	var c models.DownloadClient
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if c.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	c.Host = sanitizeHost(c.Host)
	if c.Type == "" {
		c.Type = "sabnzbd"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := downloader.TestClient(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Message string `json:"message"`
	}{Message: "Connection verified"})
}

func (h *DownloadClientHandler) attachHealth(clients []models.DownloadClient) {
	for i := range clients {
		h.attachClientHealth(&clients[i])
	}
}

func (h *DownloadClientHandler) attachClientHealth(client *models.DownloadClient) {
	if h.health == nil || client == nil {
		return
	}
	h.health.Attach(client)
}

func (h *DownloadClientHandler) refreshClientHealthAsync(client models.DownloadClient) {
	if h.health == nil {
		return
	}
	if client.Type != "qbittorrent" || !client.Enabled {
		h.health.Delete(client.ID)
		return
	}
	h.health.Set(client.ID, downloader.CheckingHealth())
	go func() {
		// Anchor on the lifetime ctx so shutdown cancels in-flight probes
		// rather than letting them run for the full 15s and then write into
		// the (still live but no-longer-served) health store.
		ctx, cancel := context.WithTimeout(h.bgCtx(), 15*time.Second)
		defer cancel()
		h.health.Set(client.ID, downloader.CheckDownloadClientHealth(ctx, &client, h.downloadDir, h.audiobookDownloadDir))
	}()
}

func (h *DownloadClientHandler) refreshClientHealth(ctx context.Context, client *models.DownloadClient) *models.DownloadClientHealth {
	if h.health == nil || client == nil {
		return nil
	}
	if client.Type != "qbittorrent" || !client.Enabled {
		h.health.Delete(client.ID)
		return nil
	}
	health := downloader.CheckDownloadClientHealth(ctx, client, h.downloadDir, h.audiobookDownloadDir)
	h.health.Set(client.ID, health)
	return &health
}
