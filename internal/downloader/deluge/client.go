// Package deluge provides a client for the Deluge Web UI JSON-RPC API,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/downloader/urlbase"
)

// hashPollTimeout is the maximum time to wait for a newly-added torrent's hash
// to appear in the torrent list.
var hashPollTimeout = 30 * time.Second

// Client interacts with the Deluge Web UI JSON-RPC API.
// Authentication is cookie-based: Login() posts auth.login which sets a
// session cookie stored in the embedded http.Client's cookie jar.
//
// Field mapping for DownloadClient storage:
//   - Password → password  (Deluge Web UI uses a single password, no username)
//   - Category  → label    (applied via the label plugin after adding)
type Client struct {
	baseURL  string
	password string
	http     *http.Client
	mu       sync.Mutex
	loggedIn bool
	reqID    atomic.Int64
}

type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int64  `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     int64           `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// New creates a Deluge client. urlBase is the optional reverse-proxy
// subpath that is appended between host:port and the json endpoint.
func New(host string, port int, password, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  fmt.Sprintf("%s://%s:%d%s", scheme, host, port, urlbase.Normalize(urlBase)),
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second, Jar: jar},
	}
}

// Login authenticates with the Deluge Web UI.
func (c *Client) Login(ctx context.Context) error {
	var result bool
	if err := c.call(ctx, false, "auth.login", []any{c.password}, &result); err != nil {
		return fmt.Errorf("deluge login: %w", err)
	}
	if !result {
		return fmt.Errorf("deluge login failed: wrong password")
	}
	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// Test verifies connectivity by checking that the Web UI is reachable and
// the password is correct.
func (c *Client) Test(ctx context.Context) error {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return fmt.Errorf("could not reach Deluge at %s — %w (in Docker use the service/container name, not localhost)", c.baseURL, err)
	}
	return nil
}

// AddTorrent submits a magnet link or torrent URL and returns the torrent hash.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, label string) (string, error) {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return "", err
	}

	var hash string
	if strings.HasPrefix(strings.ToLower(magnetOrURL), "magnet:") {
		h, err := c.addMagnet(ctx, magnetOrURL)
		if err != nil {
			return "", err
		}
		hash = h
	} else {
		h, err := c.addTorrentURL(ctx, magnetOrURL, label)
		if err != nil {
			return "", err
		}
		hash = h
	}

	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return "", fmt.Errorf("deluge accepted torrent but did not return a hash")
	}

	if label != "" {
		// Label plugin is optional; ignore errors if it is not loaded.
		_ = c.setLabel(ctx, hash, label)
	}

	return hash, nil
}

// addMagnet calls core.add_torrent_magnet which returns the hash directly.
func (c *Client) addMagnet(ctx context.Context, magnet string) (string, error) {
	var hash string
	if err := c.call(ctx, true, "core.add_torrent_magnet", []any{magnet, map[string]any{}}, &hash); err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	return hash, nil
}

// addTorrentURL downloads the .torrent file via web.download_torrent_from_url
// (which saves it to a temp path on the Deluge server), then adds it via
// web.add_torrents. The hash is resolved by polling the unfiltered torrent
// list until a new hash (not in beforeSet) appears.
func (c *Client) addTorrentURL(ctx context.Context, torrentURL, label string) (string, error) {
	// Snapshot all existing hashes so we can identify the newly-added torrent.
	beforeSet := map[string]struct{}{}
	if before, err := c.GetTorrents(ctx); err == nil {
		for h := range before {
			beforeSet[strings.ToLower(h)] = struct{}{}
		}
	}

	// Step 1: ask Deluge to download the .torrent file to a local tmp path.
	var tmpPath string
	if err := c.call(ctx, true, "web.download_torrent_from_url", []any{torrentURL, ""}, &tmpPath); err != nil {
		return "", fmt.Errorf("download torrent from url: %w", err)
	}

	// Step 2: add the downloaded .torrent file.
	addEntry := map[string]any{
		"path":    tmpPath,
		"options": map[string]any{},
	}
	var addResult any
	if err := c.call(ctx, true, "web.add_torrents", []any{[]any{addEntry}}, &addResult); err != nil {
		return "", fmt.Errorf("add torrents: %w", err)
	}

	// Poll the unfiltered torrent list until the new torrent appears — Deluge
	// processes the file asynchronously and the hash may not be visible immediately.
	deadline := time.Now().Add(hashPollTimeout)
	var lastStatuses map[string]TorrentStatus
	for {
		statuses, err := c.GetTorrents(ctx)
		if err != nil {
			return "", fmt.Errorf("add torrent accepted but hash lookup failed: %w", err)
		}
		lastStatuses = statuses
		for h := range statuses {
			lh := strings.ToLower(h)
			if _, seen := beforeSet[lh]; !seen {
				return lh, nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	beforeKeys := make([]string, 0, len(beforeSet))
	for h := range beforeSet {
		beforeKeys = append(beforeKeys, h)
	}
	afterKeys := make([]string, 0, len(lastStatuses))
	for h := range lastStatuses {
		afterKeys = append(afterKeys, h)
	}
	slog.Error("add torrent hash lookup timed out",
		"label", label,
		"before_hashes", beforeKeys,
		"after_hashes", afterKeys,
	)
	return "", fmt.Errorf("add torrent accepted but hash could not be determined")
}

// setLabel applies a label to a torrent via the Deluge label plugin.
// Errors are intentionally swallowed by the caller — the plugin is optional.
func (c *Client) setLabel(ctx context.Context, hash, label string) error {
	var result any
	return c.call(ctx, true, "label.set_torrent", []any{hash, label}, &result)
}

// GetTorrents returns status for all torrents, keyed by lower-cased hash.
func (c *Client) GetTorrents(ctx context.Context) (map[string]TorrentStatus, error) {
	fields := []string{"name", "hash", "progress", "state", "eta", "download_payload_rate", "total_size", "total_done"}
	var raw map[string]TorrentStatus
	if err := c.call(ctx, true, "core.get_torrents_status", []any{map[string]any{}, fields}, &raw); err != nil {
		return nil, fmt.Errorf("get torrents status: %w", err)
	}
	out := make(map[string]TorrentStatus, len(raw))
	for h, s := range raw {
		out[strings.ToLower(h)] = s
	}
	return out, nil
}

// RemoveTorrent removes a torrent by hash, optionally deleting its data files.
func (c *Client) RemoveTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}
	var result bool
	if err := c.call(ctx, true, "core.remove_torrent", []any{hash, deleteFiles}, &result); err != nil {
		return fmt.Errorf("remove torrent: %w", err)
	}
	return nil
}

func (c *Client) ensureLoggedIn(ctx context.Context) error {
	c.mu.Lock()
	loggedIn := c.loggedIn
	c.mu.Unlock()
	if loggedIn {
		return nil
	}
	return c.Login(ctx)
}

// call sends a JSON-RPC request to /json and unmarshals the result into out.
// If authenticated is true and the session has expired (401), it re-logs in
// and retries once.
func (c *Client) call(ctx context.Context, authenticated bool, method string, params []any, out any) error {
	if authenticated {
		if err := c.ensureLoggedIn(ctx); err != nil {
			return err
		}
	}

	body, err := c.doCall(ctx, method, params)
	if err != nil {
		// 401 means session expired — re-auth and retry once.
		if !strings.Contains(err.Error(), "HTTP 401") || !authenticated {
			return err
		}
		c.mu.Lock()
		c.loggedIn = false
		c.mu.Unlock()
		if loginErr := c.Login(ctx); loginErr != nil {
			return loginErr
		}
		body, err = c.doCall(ctx, method, params)
		if err != nil {
			return err
		}
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode result for %s: %w", method, err)
		}
	}
	return nil
}

func (c *Client) doCall(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	id := c.reqID.Add(1)
	payload, err := json.Marshal(rpcRequest{Method: method, Params: params, ID: id})
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /json: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("HTTP 401: session expired")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
