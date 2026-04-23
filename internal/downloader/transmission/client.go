// Package transmission provides a client for the Transmission BitTorrent daemon RPC API,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/downloader/urlbase"
)

// Client interacts with the Transmission RPC API.
// Authentication is done via HTTP Basic Auth if credentials are provided.
type Client struct {
	baseURL   string
	rpcURL    *url.URL
	initErr   error
	username  string
	password  string
	http      *http.Client
	sessionID string
	mu        sync.Mutex
}

// New creates a Transmission client.
// username and password are optional for Transmission RPC authentication.
// urlBase is the optional reverse-proxy subpath appended before Transmission's
// /transmission/rpc endpoint.
func New(host string, port int, username, password, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}

	client := &Client{
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}

	rpcURL, err := buildRPCURL(scheme, host, port, urlBase)
	if err != nil {
		client.initErr = err
	} else {
		client.rpcURL = rpcURL
		client.baseURL = rpcURL.String()
	}

	client.http.CheckRedirect = client.checkRedirect

	return client
}

// Test verifies connectivity by fetching session information.
func (c *Client) Test(ctx context.Context) error {
	req, err := c.buildRequest(ctx, "session-get", map[string]interface{}{})
	if err != nil {
		return err
	}
	_, err = c.doRequest(req)
	return err
}

// AddTorrent submits a magnet link or torrent URL to Transmission for download.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, downloadDir string) (int64, error) {
	args := map[string]interface{}{
		"filename": magnetOrURL,
	}
	if downloadDir != "" {
		args["download-dir"] = downloadDir
	}

	req, err := c.buildRequest(ctx, "torrent-add", args)
	if err != nil {
		return 0, err
	}
	respBody, err := c.doRequest(req)
	if err != nil {
		return 0, err
	}

	var resp TorrentAddResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return 0, fmt.Errorf("decode add torrent response: %w", err)
	}

	if resp.Result != "success" {
		return 0, fmt.Errorf("add torrent failed: %s", resp.Result)
	}

	// Return the ID of the added torrent (prefer newly added, fall back to duplicate)
	if resp.Arguments.TorrentAdded.ID != 0 {
		return resp.Arguments.TorrentAdded.ID, nil
	}
	if resp.Arguments.TorrentDuplicate.ID != 0 {
		return resp.Arguments.TorrentDuplicate.ID, nil
	}

	return 0, fmt.Errorf("no torrent ID returned")
}

// GetTorrents returns torrents in the given download directory (empty = all).
// On a shared Transmission instance, pass the client's configured download
// directory so Bindery only sees its own torrents.
func (c *Client) GetTorrents(ctx context.Context, downloadDir string) ([]Torrent, error) {
	args := map[string]interface{}{
		"fields": []string{"id", "hashString", "name", "totalSize", "downloadedEver",
			"leftUntilDone", "status", "errorString", "rateDownload", "rateUpload", "eta",
			"percentDone", "downloadDir", "labels"},
	}

	req, err := c.buildRequest(ctx, "torrent-get", args)
	if err != nil {
		return nil, err
	}
	respBody, err := c.doRequest(req)
	if err != nil {
		return nil, err
	}

	var resp TorrentGetResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("decode get torrents response: %w", err)
	}

	if resp.Result != "success" {
		return nil, fmt.Errorf("get torrents failed: %s", resp.Result)
	}

	// Filter by download directory if provided
	if downloadDir != "" {
		filtered := make([]Torrent, 0)
		for _, t := range resp.Arguments.Torrents {
			if t.DownloadDir == downloadDir {
				filtered = append(filtered, t)
			}
		}
		return filtered, nil
	}

	return resp.Arguments.Torrents, nil
}

// RemoveTorrent removes a torrent by ID.
func (c *Client) RemoveTorrent(ctx context.Context, torrentID int64, deleteFiles bool) error {
	args := map[string]interface{}{
		"ids": []int64{torrentID},
	}
	if deleteFiles {
		args["delete-local-data"] = true
	}

	req, err := c.buildRequest(ctx, "torrent-remove", args)
	if err != nil {
		return err
	}
	_, err = c.doRequest(req)
	return err
}

// buildRequest constructs a Transmission RPC request.
func (c *Client) buildRequest(ctx context.Context, method string, args map[string]interface{}) (*http.Request, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	if c.rpcURL == nil {
		return nil, fmt.Errorf("transmission RPC URL is not configured")
	}

	payload := map[string]interface{}{
		"method":    method,
		"arguments": args,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal transmission request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build transmission request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Add session ID if we have it
	c.mu.Lock()
	if c.sessionID != "" {
		req.Header.Set("X-Transmission-Session-Id", c.sessionID)
	}
	c.mu.Unlock()

	// Add Basic Auth if credentials are provided
	if c.username != "" || c.password != "" {
		authStr := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+authStr)
	}

	return req, nil
}

func buildRPCURL(scheme, host string, port int, urlBase string) (*url.URL, error) {
	if err := validateHost(host); err != nil {
		return nil, err
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid Transmission port %d", port)
	}

	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   urlbase.Normalize(urlBase) + "/transmission/rpc",
	}, nil
}

func validateHost(host string) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("transmission host is empty")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@") {
		return fmt.Errorf("transmission host must be a bare hostname or IP address")
	}
	return nil
}

// doRequest sends a request and handles the 409 conflict response (session ID update).
func (c *Client) doRequest(req *http.Request) ([]byte, error) {
	if err := c.validateRequestTarget(req.URL); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req) // #nosec G107 G704 -- URL validated by validateRequestTarget; redirect policy enforced on client
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))

	// Handle 409 Conflict - need to set session ID and retry
	if resp.StatusCode == http.StatusConflict {
		sessionID := resp.Header.Get("X-Transmission-Session-Id")
		if sessionID != "" {
			c.mu.Lock()
			c.sessionID = sessionID
			c.mu.Unlock()

			// Retry the request with the new session ID
			req2, err := c.copyRequest(req)
			if err != nil {
				return nil, err
			}
			if err := c.validateRequestTarget(req2.URL); err != nil {
				return nil, err
			}
			resp2, err := c.http.Do(req2) // #nosec G107 G704 -- retry with same validated RPC target; only session header updated
			if err != nil {
				return nil, fmt.Errorf("retry request: %w", err)
			}
			defer resp2.Body.Close()

			body, _ = io.ReadAll(io.LimitReader(resp2.Body, 1024*1024))
			resp = resp2
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("transmission HTTP %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func (c *Client) validateRequestTarget(target *url.URL) error {
	if target == nil {
		return fmt.Errorf("request target is nil")
	}
	if c.rpcURL == nil {
		return fmt.Errorf("transmission RPC URL is not configured")
	}
	if target.Scheme != c.rpcURL.Scheme || target.Host != c.rpcURL.Host || target.Path != c.rpcURL.Path {
		return fmt.Errorf("refusing transmission request to unexpected target: %s", target.Redacted())
	}
	return nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after too many redirects")
	}
	return c.validateRequestTarget(req.URL)
}

// copyRequest creates a copy of the request with a fresh body.
func (c *Client) copyRequest(orig *http.Request) (*http.Request, error) {
	if orig.GetBody == nil {
		return nil, fmt.Errorf("cannot retry request: missing request body factory")
	}
	body, err := orig.GetBody()
	if err != nil {
		return nil, fmt.Errorf("rebuild retry body: %w", err)
	}

	req := orig.Clone(orig.Context())
	req.Body = body
	req.ContentLength = orig.ContentLength

	// Update session ID header
	c.mu.Lock()
	if c.sessionID != "" {
		req.Header.Set("X-Transmission-Session-Id", c.sessionID)
	}
	c.mu.Unlock()

	return req, nil
}
