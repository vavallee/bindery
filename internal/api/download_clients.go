package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/clienthost"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/telemetry"
)

// sanitizeHost normalises a submitted Host field, or reports why it cannot be
// used. It strips a scheme prefix a user may have accidentally included
// (e.g. "http://192.168.1.50" → "192.168.1.50") and rejects a value that
// carries a port, a path, a query or a fragment: the Host field expects a
// bare hostname or IP, the scheme comes from the UseSSL flag, the port from
// the Port field and any reverse-proxy prefix from URLBase.
//
// Until #2203 everything but the scheme prefix was kept verbatim, so a URL
// pasted out of a browser address bar was stored whole and then interpolated
// into another URL. See clienthost.Normalize for what that produced.
func sanitizeHost(host string) (string, error) {
	return clienthost.Normalize(host)
}

// applyHostToClient normalises c.Host in place and writes the 400 that names
// the problem when it cannot be used. Reports whether the caller may proceed.
func applyHostToClient(w http.ResponseWriter, c *models.DownloadClient) bool {
	host, err := sanitizeHost(c.Host)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	c.Host = host
	return true
}

// downloadClientURL assembles the effective URL that would be hit for a
// download client, so httpsec.ValidateOutboundURL can check it. The assembly
// lives in clienthost so the Readarr migration can run the same check on the
// rows it creates without a second copy of the rules (#2349).
func downloadClientURL(c *models.DownloadClient) string {
	return clienthost.URL(c.Host, c.Port, c.UseSSL)
}

type DownloadClientHandler struct {
	clients              *db.DownloadClientRepo
	health               *downloader.HealthStore
	downloadDir          string
	audiobookDownloadDir string
	// downloadPathRemap is the global BINDERY_DOWNLOAD_PATH_REMAP, applied as a
	// fallback after a client's own PathRemap when checking path visibility so
	// the Test action doesn't false-warn for installs that rely on the global
	// remap rather than a per-client one (#1182).
	downloadPathRemap string

	// lifetimeCtx is the process-lifecycle context, cancelled on server
	// shutdown so the health-probe goroutine fired by Create/Update does
	// not outlive the process. Falls back to context.Background(); see #846.
	lifetimeCtx context.Context
	// settings is used only for the setup-funnel first-client marker; nil
	// (as in tests) skips the marker.
	settings *db.SettingsRepo
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

// WithSettings attaches the settings repo, used only to record the
// setup-funnel first-client marker. Nil (as in tests) skips the marker.
func (h *DownloadClientHandler) WithSettings(settings *db.SettingsRepo) *DownloadClientHandler {
	h.settings = settings
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

// WithDownloadPathRemap attaches the global BINDERY_DOWNLOAD_PATH_REMAP so the
// path-visibility check mirrors the importer's resolution (per-client remap
// first, global remap as fallback).
func (h *DownloadClientHandler) WithDownloadPathRemap(remap string) *DownloadClientHandler {
	h.downloadPathRemap = remap
	return h
}

func (h *DownloadClientHandler) List(w http.ResponseWriter, r *http.Request) {
	clients, err := h.clients.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if clients == nil {
		clients = []models.DownloadClient{}
	}
	h.attachHealth(clients)
	writeJSON(w, http.StatusOK, downloadClientResponses(clients))
}

func (h *DownloadClientHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	client, err := h.clients.GetByID(r.Context(), id)
	if err != nil || client == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}
	h.attachClientHealth(client)
	writeJSON(w, http.StatusOK, downloadClientResponse(*client))
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
	if !applyHostToClient(w, &c) {
		return
	}
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
		writeServerError(w, r, err)
		return
	}
	telemetry.MarkFirst(r.Context(), h.settings, telemetry.SettingFirstClientAt)
	h.refreshClientHealthAsync(c)
	h.attachClientHealth(&c)
	writeJSON(w, http.StatusCreated, downloadClientResponse(c))
}

func (h *DownloadClientHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	existing, err := h.clients.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Decode over a copy of the stored row rather than a zero value: JSON
	// decoding only writes the keys the client actually sent, so an omitted
	// field keeps whatever is on disk instead of being reset. Before #2213 a
	// caller that left out "enabled", "useSsl" or "category" silently wiped
	// them on every save. An explicitly sent false still disables a boolean.
	// This mirrors the same fix already made to IndexerHandler.Update.
	c := *existing
	if err := json.Unmarshal(body, &c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	// The credential fields need to know whether a key was present at all, so
	// they are resolved from the raw body rather than from the decoded struct.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := applyDownloadClientCredentials(&c, existing, raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if c.Host != "" {
		if !applyHostToClient(w, &c) {
			return
		}
		if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLANLoopback); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	c.ID = id
	if err := h.clients.Update(r.Context(), &c); err != nil {
		writeServerError(w, r, err)
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
	writeJSON(w, http.StatusOK, downloadClientResponse(c))
}

// downloadClientResponses shapes a list for the wire. See downloadClientResponse.
func downloadClientResponses(clients []models.DownloadClient) []models.DownloadClient {
	out := make([]models.DownloadClient, 0, len(clients))
	for _, c := range clients {
		out = append(out, downloadClientResponse(c))
	}
	return out
}

// downloadClientResponse returns a copy of c with the stored credentials
// blanked and the response-only booleans set, so no handler ever hands a
// password or an API key back to a caller (#2213). Modelled on the same
// treatment importListResponse gives models.ImportList.
func downloadClientResponse(c models.DownloadClient) models.DownloadClient {
	c.APIKeyConfigured = c.APIKey != ""
	c.PasswordConfigured = c.Password != ""
	c.APIKey = ""
	c.Password = ""
	return c
}

// rawDownloadClientBool reads an optional boolean field out of a raw update
// body. An absent key is false; a key holding anything other than a boolean is
// an error rather than a silent false.
func rawDownloadClientBool(raw map[string]json.RawMessage, key string) (bool, error) {
	value, ok := raw[key]
	if !ok {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(value, &b); err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}

// rawDownloadClientNonEmptyString reports whether the body carries key with a
// non-empty string value.
func rawDownloadClientNonEmptyString(raw map[string]json.RawMessage, key string) bool {
	value, ok := raw[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return false
	}
	return s != ""
}

// applyDownloadClientCredentials resolves the write-only credential fields for
// an update (#2213). c has already been decoded over a copy of the stored row,
// so an omitted key already carries the stored value; an explicitly sent empty
// string means "keep the stored value" as well, which is what lets the edit
// form save without ever holding the secret. Removing a credential takes an
// explicit clearApiKey / clearPassword flag, and sending a value together with
// its own clear flag is a contradiction rather than a precedence question.
func applyDownloadClientCredentials(c, existing *models.DownloadClient, raw map[string]json.RawMessage) error {
	clearAPIKey, err := rawDownloadClientBool(raw, "clearApiKey")
	if err != nil {
		return err
	}
	clearPassword, err := rawDownloadClientBool(raw, "clearPassword")
	if err != nil {
		return err
	}
	if clearAPIKey && rawDownloadClientNonEmptyString(raw, "apiKey") {
		return errors.New("apiKey and clearApiKey cannot both be set")
	}
	if clearPassword && rawDownloadClientNonEmptyString(raw, "password") {
		return errors.New("password and clearPassword cannot both be set")
	}

	switch {
	case clearAPIKey:
		c.APIKey = ""
	case c.APIKey == "":
		c.APIKey = existing.APIKey
	}
	switch {
	case clearPassword:
		c.Password = ""
	case c.Password == "":
		c.Password = existing.Password
	}

	// Rows written by a pre-#423 bindery kept a qBittorrent or Transmission
	// password in the api_key column, and the repo mirrors that value back into
	// Password on read. Clearing the password on such a row has to drop the
	// mirror too, otherwise the repo write path copies it straight back and the
	// credential survives the clear. This only fires when the api_key value is
	// literally the password being cleared, so a real API key is left alone.
	if clearPassword && !clearAPIKey && c.APIKey != "" && c.APIKey == existing.Password &&
		!rawDownloadClientNonEmptyString(raw, "apiKey") {
		c.APIKey = ""
	}
	return nil
}

func (h *DownloadClientHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.clients.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
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
	// Re-check the stored Host. Clients saved before #2203 were never
	// validated, and a Host that carries a port or a path still reaches a web
	// server, so the connection probe below happily reports success for a
	// client that can never return JSON. Failing here is the only way an
	// operator learns which field to fix.
	if _, err := sanitizeHost(client.Host); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	pathVis := downloader.CheckCompletedPathVisibility(r.Context(), client, h.downloadDir, h.audiobookDownloadDir, h.downloadPathRemap)
	resp := struct {
		Message        string                       `json:"message"`
		Health         *models.DownloadClientHealth `json:"health,omitempty"`
		PathVisibility *downloader.PathVisibility   `json:"pathVisibility,omitempty"`
	}{
		Message:        "Connection verified",
		Health:         health,
		PathVisibility: pathVisibilityForResponse(pathVis),
	}
	writeJSON(w, http.StatusOK, resp)
}

// pathVisibilityForResponse returns the path-visibility result for the Test
// response, or nil for the PathUnknown case so the field is omitted entirely and
// the UI shows nothing for client types that can't introspect a completed path.
func pathVisibilityForResponse(v downloader.PathVisibility) *downloader.PathVisibility {
	if v.Status == downloader.PathUnknown {
		return nil
	}
	return &v
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
	if !applyHostToClient(w, &c) {
		return
	}
	if c.Type == "" {
		c.Type = "sabnzbd"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	h.hydrateTestConfigCredentials(r.Context(), &c)
	if err := httpsec.ValidateOutboundURL(downloadClientURL(&c), httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := downloader.TestClient(r.Context(), &c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pathVis := downloader.CheckCompletedPathVisibility(r.Context(), &c, h.downloadDir, h.audiobookDownloadDir, h.downloadPathRemap)
	writeJSON(w, http.StatusOK, struct {
		Message        string                     `json:"message"`
		PathVisibility *downloader.PathVisibility `json:"pathVisibility,omitempty"`
	}{Message: "Connection verified", PathVisibility: pathVisibilityForResponse(pathVis)})
}

// hydrateTestConfigCredentials fills a blank credential on a test-a-config
// request from the saved row it belongs to. The edit form no longer receives
// the stored password or API key (#2213), so without this the "Test" button
// would probe unauthenticated whenever the user did not retype the secret.
//
// It deliberately refuses to do so once the request points somewhere else:
// type, host, port, TLS and URL base must all still match the saved row. That
// keeps the endpoint from being turned into a way to read a stored credential
// back out by aiming a probe at a host the caller controls.
func (h *DownloadClientHandler) hydrateTestConfigCredentials(ctx context.Context, c *models.DownloadClient) {
	if c.ID <= 0 || h.clients == nil {
		return
	}
	if c.APIKey != "" && c.Password != "" {
		return
	}
	stored, err := h.clients.GetByID(ctx, c.ID)
	if err != nil || stored == nil {
		return
	}
	if stored.Type != c.Type || stored.Host != c.Host || stored.Port != c.Port ||
		stored.UseSSL != c.UseSSL || strings.TrimSpace(stored.URLBase) != strings.TrimSpace(c.URLBase) {
		return
	}
	if c.APIKey == "" {
		c.APIKey = stored.APIKey
	}
	if c.Password == "" {
		c.Password = stored.Password
	}
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
	if !client.Enabled {
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
		h.health.Set(client.ID, downloader.CheckDownloadClientHealth(ctx, &client, h.downloadDir, h.audiobookDownloadDir, h.downloadPathRemap))
	}()
}

func (h *DownloadClientHandler) refreshClientHealth(ctx context.Context, client *models.DownloadClient) *models.DownloadClientHealth {
	if h.health == nil || client == nil {
		return nil
	}
	if !client.Enabled {
		h.health.Delete(client.ID)
		return nil
	}
	health := downloader.CheckDownloadClientHealth(ctx, client, h.downloadDir, h.audiobookDownloadDir, h.downloadPathRemap)
	h.health.Set(client.ID, health)
	return &health
}
