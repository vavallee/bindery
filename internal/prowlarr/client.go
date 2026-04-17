// Package prowlarr provides a client for the Prowlarr API and a syncer that
// creates/updates/removes Bindery indexer entries from a Prowlarr instance.
package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls the Prowlarr HTTP API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a Prowlarr client.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// remoteIndexer is the shape of each element in GET /api/v1/indexer.
type remoteIndexer struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"` // "usenet" or "torrent"
	SupportsSearch bool   `json:"supportsSearch"`
	Categories     []struct {
		ID int `json:"id"`
	} `json:"categories"`
}

// IndexerInfo holds the information needed to create a Bindery indexer from a
// Prowlarr-managed indexer.
type IndexerInfo struct {
	ProwlarrID     int
	Name           string
	Protocol       string
	TorznabURL     string
	APIKey         string
	SupportsSearch bool
	Categories     []int
}

// FetchIndexers returns all indexers configured in Prowlarr.
func (c *Client) FetchIndexers(ctx context.Context) ([]IndexerInfo, error) {
	data, err := c.get(ctx, "/api/v1/indexer")
	if err != nil {
		return nil, err
	}

	var remotes []remoteIndexer
	if err := json.Unmarshal(data, &remotes); err != nil {
		return nil, fmt.Errorf("decode prowlarr indexers: %w", err)
	}

	infos := make([]IndexerInfo, 0, len(remotes))
	for _, ri := range remotes {
		// Build the Torznab/Newznab URL: {base}/{id}/api
		torznabURL := fmt.Sprintf("%s/%d/api", c.baseURL, ri.ID)

		cats := make([]int, 0, len(ri.Categories))
		for _, cat := range ri.Categories {
			cats = append(cats, cat.ID)
		}

		infos = append(infos, IndexerInfo{
			ProwlarrID:     ri.ID,
			Name:           ri.Name,
			Protocol:       ri.Protocol,
			TorznabURL:     torznabURL,
			APIKey:         c.apiKey,
			SupportsSearch: ri.SupportsSearch,
			Categories:     cats,
		})
	}
	return infos, nil
}

// Test verifies connectivity by fetching the Prowlarr system status.
func (c *Client) Test(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v1/system/status")
	if err != nil {
		return "", fmt.Errorf("could not reach Prowlarr at %s — %w", c.baseURL, err)
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return "", nil
	}
	return status.Version, nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid Prowlarr API key")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}
