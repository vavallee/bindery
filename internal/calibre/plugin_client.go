package calibre

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/pathmap"
)

// ErrAlreadyInCalibre is returned by PluginClient.Add when the plugin
// reports the book is already present (HTTP 409 Conflict). Callers that
// treat duplicate pushes as idempotent — e.g. the "Push all to Calibre"
// bulk sync — can errors.Is-check this sentinel instead of parsing the
// response body.
var ErrAlreadyInCalibre = errors.New("plugin client: book already in Calibre library")

const pluginCapabilityBookMetadata = "book_metadata"

// PluginClient calls the Bindery Bridge Calibre plugin's HTTP API
// (protocol /v1/, see bindery-plugins/docs/protocol.md). It implements the
// importer's calibreAdder interface so the scanner can swap it in when the
// operator selects mode=plugin.
type PluginClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// remap translates Bindery-side library paths to the prefix the plugin's
	// container sees before they go over the wire (#1346). nil = passthrough.
	remap *pathmap.Remapper

	capMu                 sync.Mutex
	capabilitiesLoaded    bool
	supportsBookMetadata  bool
	metadataWarningLogged bool
}

// NewPluginClient builds a client against the plugin's base URL (e.g.
// "http://calibre.default.svc:8099"). Trailing slashes are trimmed so
// callers can pass either form.
func NewPluginClient(baseURL, apiKey string) *PluginClient {
	return &PluginClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// WithPushPathRemap installs a pathmap "from:to[,from:to]" translation applied
// to every file path sent to the plugin (#1346). Bindery hands the Bridge the
// path it stores a book at and the plugin opens that path on ITS side of the
// container boundary; when the two containers mount the library at different
// points (the recurring Unraid case) every push fails with "No such file or
// directory". The spec is parsed once here; empty or all-malformed input
// leaves the client in passthrough mode. Returns the client for chaining.
func (c *PluginClient) WithPushPathRemap(spec string) *PluginClient {
	if r := pathmap.Parse(spec); !r.Empty() {
		c.remap = r
	}
	return c
}

// pushPathFor returns the path to put on the wire for filePath: remapped when
// a translation is configured, verbatim otherwise.
func (c *PluginClient) pushPathFor(filePath string) string {
	if c.remap == nil {
		return filePath
	}
	return c.remap.Apply(filePath)
}

// Add POSTs the file path and Bindery metadata to the plugin and returns the
// Calibre book id.
// Retries once on 503 (library swap in progress); all other non-2xx
// statuses surface immediately.
func (c *PluginClient) Add(ctx context.Context, filePath string, meta Metadata) (int64, error) {
	legacyPayload := false
	if !meta.empty() {
		supported, err := c.supportsMetadata(ctx)
		if err != nil {
			c.warnMetadataUnavailable("plugin client: metadata capability probe failed; sending metadata and will retry legacy payload if rejected", "error", err)
		} else if !supported {
			c.warnMetadataUnavailable("plugin client: plugin does not advertise metadata support; upgrade Bindery Bridge to export metadata")
			legacyPayload = true
		}
	}
	// Translate once, up front: addWithRetry recurses for the 503 and
	// legacy-payload retries and must carry the already-translated path.
	return c.addWithRetry(ctx, c.pushPathFor(filePath), meta, 1, legacyPayload)
}

func (c *PluginClient) addWithRetry(ctx context.Context, filePath string, meta Metadata, retries int, legacyPayload bool) (int64, error) {
	body, _ := json.Marshal(pluginAddRequest{Path: filePath, Metadata: &meta, Legacy: legacyPayload})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/books", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bindery plugin-api/v1")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("plugin client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable && retries > 0 {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		return c.addWithRetry(ctx, filePath, meta, retries-1, legacyPayload)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return 0, fmt.Errorf("plugin client: authentication failed — check api_key in Settings → Calibre")
	}

	var result struct {
		ID        int64  `json:"id"`
		Duplicate bool   `json:"duplicate"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil && resp.StatusCode < 400 {
		return 0, fmt.Errorf("plugin client: decode response: %w", err)
	}
	if !legacyPayload && !meta.empty() && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
		slog.Warn("plugin client: metadata payload rejected; retrying legacy path-only payload",
			"status", resp.StatusCode, "error", result.Error)
		return c.addWithRetry(ctx, filePath, Metadata{}, retries, true)
	}
	if resp.StatusCode == http.StatusConflict {
		// 409 → book is already in the Calibre library. Surface the
		// existing id (when the plugin includes it) so the caller can
		// persist the linkage, but wrap ErrAlreadyInCalibre so idempotent
		// callers can distinguish this from a real failure.
		return result.ID, ErrAlreadyInCalibre
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("plugin client: server error %d: %s", resp.StatusCode, result.Error)
	}
	return result.ID, nil
}

type pluginAddRequest struct {
	Path     string    `json:"path"`
	Metadata *Metadata `json:"metadata,omitempty"`
	Legacy   bool      `json:"-"`
}

func (r pluginAddRequest) MarshalJSON() ([]byte, error) {
	if r.Legacy || r.Metadata == nil || r.Metadata.empty() {
		return json.Marshal(struct {
			Path string `json:"path"`
		}{Path: r.Path})
	}
	return json.Marshal(struct {
		Path     string   `json:"path"`
		Metadata Metadata `json:"metadata"`
	}{Path: r.Path, Metadata: *r.Metadata})
}

type pluginHealth struct {
	PluginVersion  string   `json:"plugin_version"`
	CalibreVersion string   `json:"calibre_version"`
	Library        string   `json:"library"`
	Capabilities   []string `json:"capabilities"`
}

func (c *PluginClient) supportsMetadata(ctx context.Context) (bool, error) {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	if c.capabilitiesLoaded {
		return c.supportsBookMetadata, nil
	}
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return false, err
	}
	c.cacheCapabilitiesLocked(h)
	return c.supportsBookMetadata, nil
}

func (c *PluginClient) warnMetadataUnavailable(msg string, args ...any) {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	if c.metadataWarningLogged {
		return
	}
	c.metadataWarningLogged = true
	slog.Warn(msg, args...)
}

func (c *PluginClient) cacheCapabilities(h pluginHealth) {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	c.cacheCapabilitiesLocked(h)
}

func (c *PluginClient) cacheCapabilitiesLocked(h pluginHealth) {
	c.capabilitiesLoaded = true
	c.supportsBookMetadata = false
	for _, cap := range h.Capabilities {
		if cap == pluginCapabilityBookMetadata {
			c.supportsBookMetadata = true
			return
		}
	}
}

// Health probes GET /v1/health and returns a human-readable version
// string for the Settings → Test button.
func (c *PluginClient) Health(ctx context.Context) (string, error) {
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return "", err
	}
	c.cacheCapabilities(h)
	return fmt.Sprintf("calibredb plugin v%s (Calibre %s)", h.PluginVersion, h.CalibreVersion), nil
}

// Library probes the plugin's active Calibre library path. The returned path is
// from the Calibre/plugin runtime, so direct path comparison is only reliable
// when Bindery and Calibre mount the same library at the same container path.
func (c *PluginClient) Library(ctx context.Context) (string, error) {
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return "", err
	}
	c.cacheCapabilities(h)
	return h.Library, nil
}

func (c *PluginClient) fetchHealth(ctx context.Context) (pluginHealth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health", nil)
	if err != nil {
		return pluginHealth{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "bindery plugin-api/v1")
	resp, err := c.http.Do(req)
	if err != nil {
		return pluginHealth{}, fmt.Errorf("plugin client: health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return pluginHealth{}, fmt.Errorf("plugin client: authentication failed — check api_key in Settings → Calibre")
	}
	if resp.StatusCode >= 400 {
		return pluginHealth{}, fmt.Errorf("plugin client: health: server error %d", resp.StatusCode)
	}
	var h pluginHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return pluginHealth{}, fmt.Errorf("plugin client: decode health: %w", err)
	}
	return h, nil
}
