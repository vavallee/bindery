// Package deluge provides a client for the Deluge Web UI JSON-RPC API,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package deluge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

// maxTorrentFileBytes caps the torrent payload Bindery will fetch before
// uploading it to Deluge.
var maxTorrentFileBytes int64 = 50 << 20

// Client interacts with the Deluge Web UI JSON-RPC API.
// Authentication is cookie-based: Login() posts auth.login which sets a
// session cookie stored in the embedded http.Client's cookie jar.
//
// Field mapping for DownloadClient storage:
//   - Password → password  (Deluge Web UI uses a single password, no username)
//   - Category  → label    (applied via the label plugin after adding)
type Client struct {
	baseURL            string
	password           string
	http               *http.Client
	fetchTransport     http.RoundTripper // SSRF-guarded transport for indexer torrent fetches
	validateTorrentURL func(string) error
	mu                 sync.Mutex // guards loggedIn
	loggedIn           bool
	reqID              atomic.Int64
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
		baseURL:        fmt.Sprintf("%s://%s:%d%s", scheme, host, port, urlbase.Normalize(urlBase)),
		password:       password,
		http:           &http.Client{Timeout: 15 * time.Second, Jar: jar},
		fetchTransport: httpsec.GuardedTransport(httpsec.DownloadFetchPolicy()),
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
		return fmt.Errorf("could not reach Deluge at %s — %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	return nil
}

// AddTorrent submits a magnet link or torrent URL and returns the torrent hash.
//
// seedRatio carries the per-indexer override (#883): a non-negative value is
// applied via core.set_torrent_stop_ratio after the hash resolves. Deluge's
// RPC accepts only non-negative floats here, so the -1 unlimited sentinel (and
// a nil pointer) skips the call entirely, leaving Deluge's global default in
// place. Ratio-limit errors are non-fatal: the torrent is already added, so a
// failure to tighten the ratio must not fail the grab.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, label string, seedRatio *float64) (string, error) {
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
		h, mag, err := c.addTorrentFile(ctx, magnetOrURL)
		if err != nil {
			return "", err
		}
		if mag != "" {
			// The indexer redirected to a magnet link; hand off to the magnet path.
			mh, merr := c.addMagnet(ctx, mag)
			if merr != nil {
				return "", merr
			}
			h = mh
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

	// Apply the per-indexer seed-ratio override. Skipped for nil (no override)
	// and the -1 unlimited sentinel, which Deluge cannot express via
	// set_torrent_stop_ratio (it rejects negatives) — leave the global default.
	if seedRatio != nil && *seedRatio >= 0 {
		if err := c.setStopRatio(ctx, hash, *seedRatio); err != nil {
			slog.Warn("deluge: failed to set seed-ratio limit", "hash", hash, "ratio", *seedRatio, "error", err)
		}
	}

	return hash, nil
}

// setStopRatio sets the per-torrent stop seed ratio via
// core.set_torrent_stop_ratio and enables ratio-based stopping via
// core.set_torrent_stop_at_ratio so the limit is actually honored rather than
// silently stored. Both accept (hash, value) and only non-negative ratios.
func (c *Client) setStopRatio(ctx context.Context, hash string, ratio float64) error {
	var result any
	if err := c.call(ctx, true, "core.set_torrent_stop_ratio", []any{hash, ratio}, &result); err != nil {
		return fmt.Errorf("set torrent stop ratio: %w", err)
	}
	if err := c.call(ctx, true, "core.set_torrent_stop_at_ratio", []any{hash, true}, &result); err != nil {
		return fmt.Errorf("set torrent stop at ratio: %w", err)
	}
	return nil
}

// addMagnet calls core.add_torrent_magnet which returns the hash directly.
func (c *Client) addMagnet(ctx context.Context, magnet string) (string, error) {
	var hash string
	if err := c.call(ctx, true, "core.add_torrent_magnet", []any{magnet, map[string]any{}}, &hash); err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	return hash, nil
}

// addTorrentFile fetches the .torrent bytes inside Bindery (so the request
// runs in Bindery's DNS namespace, not Deluge's VPN container) and submits
// the content to Deluge via core.add_torrent_file.  If the URL resolves to a
// magnet redirect the caller should use addMagnet instead — this function
// returns a non-nil magnetURL in that case and the caller switches paths.
//
// core.add_torrent_file returns the infohash directly, so no before/after
// snapshot polling is needed.
func (c *Client) addTorrentFile(ctx context.Context, torrentURL string) (hash string, magnetURL string, err error) {
	fetched, err := c.fetchTorrentContent(ctx, torrentURL)
	if err != nil {
		return "", "", fmt.Errorf("fetch torrent: %w", err)
	}
	if fetched.magnetURL != "" {
		return "", fetched.magnetURL, nil
	}

	// Derive a filename from the URL path; fall back to a safe default.
	filename := path.Base(torrentURL)
	if filename == "" || filename == "." || filename == "/" {
		filename = "download.torrent"
	}

	filedump := base64.StdEncoding.EncodeToString(fetched.data)
	var infohash string
	if err := c.call(ctx, true, "core.add_torrent_file", []any{filename, filedump, map[string]any{}}, &infohash); err != nil {
		return "", "", fmt.Errorf("core.add_torrent_file: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(infohash)), "", nil
}

type fetchedTorrentContent struct {
	data      []byte
	magnetURL string
}

// fetchTorrentContent downloads a .torrent file URL and returns its raw bytes.
// It follows up to 5 redirects, validates each target URL before fetching,
// and caps the response at maxTorrentFileBytes.  A redirect to a magnet: URI
// is returned as magnetURL rather than bytes.
func (c *Client) fetchTorrentContent(ctx context.Context, rawURL string) (*fetchedTorrentContent, error) {
	current := rawURL
	fetchClient := &http.Client{
		// Guard the dial against the indexer-controlled torrent URL: the loop
		// re-validates each redirect hop, and this adds a per-dial recheck so a
		// DNS rebind between validate and connect can't reach a forbidden host.
		// Uses the download-fetch-policy transport, not the RPC client's (which
		// targets the admin-configured download client over loopback).
		Transport: c.fetchTransport,
		Timeout:   c.http.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for redirects := 0; redirects <= 5; redirects++ {
		if err := c.validateTorrentFetchURL(current); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, fmt.Errorf("build torrent fetch request: %w", err)
		}
		req.Header.Set("Accept", "application/x-bittorrent")
		req.Header.Set("User-Agent", useragent.Get())

		resp, err := fetchClient.Do(req)
		if err != nil {
			// Scrub the indexer apikey the *url.Error would otherwise leak into
			// the download row / history / webhook payloads.
			return nil, httpsec.RedactURLError(err)
		}

		if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
			location := resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, resp.Body)
			if err := resp.Body.Close(); err != nil {
				return nil, fmt.Errorf("close redirect body: %w", err)
			}
			if location == "" {
				return nil, fmt.Errorf("redirect without location")
			}
			if strings.HasPrefix(strings.ToLower(location), "magnet:") {
				return &fetchedTorrentContent{magnetURL: location}, nil
			}
			next, err := req.URL.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("invalid redirect location: %w", err)
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return nil, fmt.Errorf("unsupported redirect scheme %q", next.Scheme)
			}
			current = next.String()
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("indexer returned HTTP %d", resp.StatusCode)
		}
		data, err := readLimited(resp.Body, maxTorrentFileBytes)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("empty torrent response")
		}
		return &fetchedTorrentContent{data: data}, nil
	}

	return nil, fmt.Errorf("too many redirects")
}

func (c *Client) validateTorrentFetchURL(raw string) error {
	if c.validateTorrentURL != nil {
		return c.validateTorrentURL(raw)
	}
	return httpsec.ValidateOutboundURL(raw, httpsec.DownloadFetchPolicy())
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("torrent response exceeds %d bytes", maxBytes)
	}
	return data, nil
}

// setLabel applies a label to a torrent via the Deluge label plugin.
// Errors are intentionally swallowed by the caller — the plugin is optional.
func (c *Client) setLabel(ctx context.Context, hash, label string) error {
	var result any
	return c.call(ctx, true, "label.set_torrent", []any{hash, label}, &result)
}

// GetTorrents returns status for all torrents, keyed by lower-cased hash.
func (c *Client) GetTorrents(ctx context.Context) (map[string]TorrentStatus, error) {
	fields := []string{"name", "hash", "progress", "state", "eta", "download_payload_rate", "total_size", "total_done", "save_path", "download_location"}
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

// Files returns the per-torrent file list for hash. Names are paths
// relative to the torrent's save_path / download_location; for a
// single-file torrent dropped directly at the save root the entry's Name
// is just the file's basename.
//
// This is the authoritative list of files that belong to this torrent, used
// by the importer (issue #903) to avoid walking the shared download root
// and picking up unrelated siblings.
//
// A torrent that the Deluge daemon does not know about surfaces as an RPC
// error from core.get_torrent_status (KeyError on the hash).
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	var status torrentFilesStatus
	if err := c.call(ctx, true, "core.get_torrent_status", []any{hash, []string{"files"}}, &status); err != nil {
		return nil, fmt.Errorf("get torrent files: %w", err)
	}
	out := make([]File, 0, len(status.Files))
	for _, f := range status.Files {
		if f.Path == "" {
			continue
		}
		out = append(out, File{Name: f.Path, Size: f.Size})
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
