package calibre

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrAlreadyInCalibre is returned by PluginClient.Add when the plugin
// reports the book is already present (HTTP 409 Conflict). Callers that
// treat duplicate pushes as idempotent — e.g. the "Push all to Calibre"
// bulk sync — can errors.Is-check this sentinel instead of parsing the
// response body.
var ErrAlreadyInCalibre = errors.New("plugin client: book already in Calibre library")

// PluginClient calls the Bindery Bridge Calibre plugin's HTTP API
// (protocol /v1/, see bindery-plugins/docs/protocol.md). It implements the
// importer's calibreAdder interface so the scanner can swap it in when the
// operator selects mode=plugin.
type PluginClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
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

// Add POSTs the file path to the plugin and returns the Calibre book id.
// Retries once on 503 (library swap in progress); all other non-2xx
// statuses surface immediately.
func (c *PluginClient) Add(ctx context.Context, filePath string) (int64, error) {
	return c.addWithRetry(ctx, filePath, 1)
}

func (c *PluginClient) addWithRetry(ctx context.Context, filePath string, retries int) (int64, error) {
	body, _ := json.Marshal(map[string]string{"path": filePath})
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
		return c.addWithRetry(ctx, filePath, retries-1)
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

// Health probes GET /v1/health and returns a human-readable version
// string for the Settings → Test button.
func (c *PluginClient) Health(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "bindery plugin-api/v1")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("plugin client: health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("plugin client: authentication failed — check api_key in Settings → Calibre")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("plugin client: health: server error %d", resp.StatusCode)
	}
	var h struct {
		PluginVersion  string `json:"plugin_version"`
		CalibreVersion string `json:"calibre_version"`
		Library        string `json:"library"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return "", fmt.Errorf("plugin client: decode health: %w", err)
	}
	return fmt.Sprintf("calibredb plugin v%s (Calibre %s)", h.PluginVersion, h.CalibreVersion), nil
}
