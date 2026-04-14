// Package qbittorrent provides a client for the qBittorrent WebUI API v2,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client interacts with the qBittorrent WebUI API v2.
// Authentication is cookie-based: Login() obtains a SID cookie which is
// stored in the embedded http.Client's cookie jar and sent automatically on
// subsequent requests.
//
// Field mapping for DownloadClient storage:
//   - APIKey  → password  (qBittorrent uses username/password, not an API key)
//   - URLBase → username  (reused since qBittorrent ignores URL base)
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
	mu       sync.Mutex
	loggedIn bool
}

// New creates a qBittorrent client.
// username and password map to the DownloadClient's URLBase and APIKey fields
// respectively (see comment on the Client struct).
func New(host string, port int, username, password string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}

	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  fmt.Sprintf("%s://%s:%d", scheme, host, port),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second, Jar: jar},
	}
}

// Login authenticates with qBittorrent and stores the SID cookie.
func (c *Client) Login(ctx context.Context) error {
	form := url.Values{
		"username": {c.username},
		"password": {c.password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/auth/login",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	text := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK || text == "Fails." {
		return fmt.Errorf("qBittorrent login failed: %s", text)
	}

	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// Test verifies connectivity by fetching the application version.
func (c *Client) Test(ctx context.Context) error {
	if _, err := c.get(ctx, "/api/v2/app/version"); err != nil {
		return fmt.Errorf("could not reach qBittorrent at %s — %w (in Docker use the service/container name, not localhost)", c.baseURL, err)
	}
	return nil
}

// AddTorrent submits a magnet link or torrent URL to qBittorrent for download.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, category, savePath string) error {
	form := url.Values{"urls": {magnetOrURL}}
	if category != "" {
		form.Set("category", category)
	}
	if savePath != "" {
		form.Set("savepath", savePath)
	}

	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/torrents/add",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build add request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("add torrent: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	text := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("add torrent HTTP %d: %s", resp.StatusCode, text)
	}
	if text != "Ok." {
		return fmt.Errorf("add torrent failed: %s", text)
	}
	return nil
}

// GetTorrents returns all torrents in the given category (empty = all).
func (c *Client) GetTorrents(ctx context.Context, category string) ([]Torrent, error) {
	endpoint := "/api/v2/torrents/info"
	if category != "" {
		endpoint += "?category=" + url.QueryEscape(category)
	}

	data, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var torrents []Torrent
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, fmt.Errorf("decode torrents: %w", err)
	}
	return torrents, nil
}

// DeleteTorrent removes a torrent by hash, optionally deleting its files.
func (c *Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	deleteFilesStr := "false"
	if deleteFiles {
		deleteFilesStr = "true"
	}

	form := url.Values{
		"hashes":      {hash},
		"deleteFiles": {deleteFilesStr},
	}

	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/torrents/delete",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete torrent: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete torrent HTTP %d", resp.StatusCode)
	}
	return nil
}

// ensureLoggedIn logs in if not already authenticated.
func (c *Client) ensureLoggedIn(ctx context.Context) error {
	c.mu.Lock()
	loggedIn := c.loggedIn
	c.mu.Unlock()
	if loggedIn {
		return nil
	}
	return c.Login(ctx)
}

// get performs an authenticated GET request and returns the response body.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		// Session expired — re-login once and retry.
		c.mu.Lock()
		c.loggedIn = false
		c.mu.Unlock()
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return nil, fmt.Errorf("build retry request for %s: %w", path, err)
		}
		resp2, err := c.http.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("GET %s (retry): %w", path, err)
		}
		defer resp2.Body.Close()
		return io.ReadAll(resp2.Body)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
