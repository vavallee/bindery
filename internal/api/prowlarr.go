package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/prowlarr"
)

// SettingProwlarrSearchTimeoutSeconds is the key for the prowlarr HTTP timeout setting.
const SettingProwlarrSearchTimeoutSeconds = "prowlarr.search_timeout_seconds"

type ProwlarrHandler struct {
	instances *db.ProwlarrRepo
	indexers  *db.IndexerRepo
	settings  *db.SettingsRepo
}

func NewProwlarrHandler(instances *db.ProwlarrRepo, indexers *db.IndexerRepo) *ProwlarrHandler {
	return &ProwlarrHandler{instances: instances, indexers: indexers}
}

// WithSettings attaches a settings repo so the handler can read the configurable
// prowlarr.search_timeout_seconds value.
func (h *ProwlarrHandler) WithSettings(s *db.SettingsRepo) *ProwlarrHandler {
	h.settings = s
	return h
}

// prowlarrClientTimeout returns the configured search timeout, defaulting to
// the value baked into prowlarr.New (60 s) when no setting exists.
func (h *ProwlarrHandler) prowlarrClientTimeout(ctx context.Context) time.Duration {
	if h.settings != nil {
		if s, _ := h.settings.Get(ctx, SettingProwlarrSearchTimeoutSeconds); s != nil {
			if secs, err := strconv.Atoi(s.Value); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return 60 * time.Second
}

// newClient constructs a Prowlarr API client using the (possibly user-configured) timeout.
func (h *ProwlarrHandler) newClient(ctx context.Context, url, apiKey string) *prowlarr.Client {
	return prowlarr.NewWithTimeout(url, apiKey, h.prowlarrClientTimeout(ctx))
}

// LoadProwlarrTimeout reads prowlarr.search_timeout_seconds from settings,
// returning 60 s when the key is absent or unparseable.
func LoadProwlarrTimeout(ctx context.Context, s *db.SettingsRepo) time.Duration {
	if s != nil {
		if setting, _ := s.Get(ctx, SettingProwlarrSearchTimeoutSeconds); setting != nil {
			if secs, err := strconv.Atoi(setting.Value); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return 60 * time.Second
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
	if err := httpsec.ValidateOutboundURL(p.URL, httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid indexer URL: " + err.Error()})
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
	previousKey := existing.APIKey
	if err := json.NewDecoder(r.Body).Decode(existing); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	existing.ID = id
	if err := httpsec.ValidateOutboundURL(existing.URL, httpsec.PolicyLANLoopback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid indexer URL: " + err.Error()})
		return
	}
	if err := h.instances.Update(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if existing.APIKey != previousKey && h.indexers != nil {
		if _, err := h.indexers.UpdateAPIKeyByProwlarrInstance(r.Context(), id, existing.APIKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
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
	client := h.newClient(r.Context(), p.URL, p.APIKey)
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

	client := h.newClient(r.Context(), p.URL, p.APIKey)
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
