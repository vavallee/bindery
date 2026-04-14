// Package sabnzbd provides a client for the SABnzbd JSON API, used to
// submit NZB URLs and poll queue/history for Usenet downloads.
package sabnzbd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client interacts with the SABnzbd API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a SABnzbd client.
func New(host string, port int, apiKey string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return &Client{
		baseURL: fmt.Sprintf("%s://%s:%d", scheme, host, port),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Test verifies connectivity by fetching categories.
func (c *Client) Test(ctx context.Context) error {
	if _, err := c.GetCategories(ctx); err != nil {
		return fmt.Errorf("could not reach SABnzbd at %s — %w (in Docker use the service/container name, not localhost)", c.baseURL, err)
	}
	return nil
}

// AddURL sends an NZB URL to SABnzbd for download.
func (c *Client) AddURL(ctx context.Context, nzbURL, title, category string, priority int) (*AddURLResponse, error) {
	params := url.Values{
		"mode":     {"addurl"},
		"name":     {nzbURL},
		"nzbname":  {title},
		"cat":      {category},
		"priority": {fmt.Sprintf("%d", priority)},
		"pp":       {"3"}, // repair + unpack + delete archives
	}

	var resp AddURLResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("add url: %w", err)
	}
	if !resp.Status {
		return nil, fmt.Errorf("SABnzbd rejected download")
	}
	return &resp, nil
}

// GetQueue returns the current download queue.
func (c *Client) GetQueue(ctx context.Context) (*QueueData, error) {
	params := url.Values{
		"mode":  {"queue"},
		"start": {"0"},
		"limit": {"100"},
	}

	var resp QueueResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get queue: %w", err)
	}
	return &resp.Queue, nil
}

// GetHistory returns completed/failed downloads.
func (c *Client) GetHistory(ctx context.Context, category string, limit int) (*HistoryData, error) {
	params := url.Values{
		"mode":  {"history"},
		"start": {"0"},
		"limit": {fmt.Sprintf("%d", limit)},
	}
	if category != "" {
		params.Set("cat", category)
	}

	var resp HistoryResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	return &resp.History, nil
}

// GetCategories lists all configured categories.
func (c *Client) GetCategories(ctx context.Context) ([]string, error) {
	params := url.Values{"mode": {"get_cats"}}

	var resp CategoriesResponse
	if err := c.apiCall(ctx, params, &resp); err != nil {
		return nil, fmt.Errorf("get categories: %w", err)
	}
	return resp.Categories, nil
}

// Pause pauses a download by NZO ID.
func (c *Client) Pause(ctx context.Context, nzoID string) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"pause"},
		"value": {nzoID},
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// Resume resumes a paused download.
func (c *Client) Resume(ctx context.Context, nzoID string) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"resume"},
		"value": {nzoID},
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// Delete removes a download from the queue.
func (c *Client) Delete(ctx context.Context, nzoID string, deleteFiles bool) error {
	params := url.Values{
		"mode":  {"queue"},
		"name":  {"delete"},
		"value": {nzoID},
	}
	if deleteFiles {
		params.Set("del_files", "1")
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

// DeleteHistory removes a finished job from SABnzbd's history. When deleteFiles
// is true, SAB also wipes the on-disk completed folder — bindery's importer has
// typically already moved the contents, so callers usually pass false.
func (c *Client) DeleteHistory(ctx context.Context, nzoID string, deleteFiles bool) error {
	params := url.Values{
		"mode":  {"history"},
		"name":  {"delete"},
		"value": {nzoID},
	}
	if deleteFiles {
		params.Set("del_files", "1")
	}
	var resp SimpleResponse
	return c.apiCall(ctx, params, &resp)
}

func (c *Client) apiCall(ctx context.Context, params url.Values, target interface{}) error {
	params.Set("apikey", c.apiKey)
	params.Set("output", "json")

	u := fmt.Sprintf("%s/api?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}
