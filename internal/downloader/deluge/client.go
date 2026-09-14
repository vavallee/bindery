// Package deluge provides a client for the Deluge Web UI JSON-RPC API,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package deluge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/vavallee/bindery/internal/downloader/clienthost"
	"github.com/vavallee/bindery/internal/downloader/infohash"
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
		baseURL:        fmt.Sprintf("%s://%s%s", scheme, clienthost.Authority(host, port), urlbase.Normalize(urlBase)),
		password:       password,
		http:           &http.Client{Timeout: 15 * time.Second, Jar: jar},
		fetchTransport: httpsec.GuardedTransport(httpsec.DownloadFetchPolicy()),
	}
}

// Login authenticates with the Deluge Web UI and attaches the resulting
// session to a deluged daemon, which every core.* method needs — see
// connectDaemon.
func (c *Client) Login(ctx context.Context) error {
	var result bool
	if err := c.call(ctx, false, "auth.login", []any{c.password}, &result); err != nil {
		return fmt.Errorf("deluge login: %w", err)
	}
	if !result {
		return fmt.Errorf("deluge login failed: wrong password")
	}
	// Once per login rather than once per call: a session keeps its daemon for
	// its lifetime, so an extra round trip on every RPC would buy nothing. The
	// 401 retry path in call() re-enters Login and re-checks.
	if err := c.connectDaemon(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// Test verifies connectivity by checking that the Web UI is reachable, the
// password is correct, and the session has a deluged daemon behind it.
func (c *Client) Test(ctx context.Context) error {
	if err := c.ensureLoggedIn(ctx); err != nil {
		// A missing daemon isn't a reachability problem — the Web UI answered,
		// so "could not reach Deluge" would send the operator hunting a network
		// fault that isn't there.
		if errors.Is(err, errNoDaemon) {
			return err
		}
		return fmt.Errorf("could not reach Deluge at %s — %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	// ensureLoggedIn only reaches connectDaemon when it actually logs in, so a
	// long-lived client can be holding a session whose daemon has since gone
	// away. Re-check here: a Test that passes while every grab fails is the
	// defect underneath #2204.
	return c.connectDaemon(ctx)
}

// errNoDaemon marks the state at the heart of #2204: auth.login succeeded, so
// the Web UI is reachable and the password is right, but the session has no
// deluged behind it and every core.* method will fail.
var errNoDaemon = errors.New("the Deluge Web UI is not connected to a deluged daemon")

// daemonHost is one entry from web.get_hosts.
type daemonHost struct {
	id   string
	addr string
	port int64
}

// label renders a host for an error message, preferring the address over the
// opaque host id.
func (h daemonHost) label() string {
	switch {
	case h.addr == "":
		return h.id
	case h.port > 0:
		return fmt.Sprintf("%s:%d", h.addr, h.port)
	default:
		return h.addr
	}
}

// connectDaemon attaches the Web UI session to a deluged daemon unless it is
// attached already.
//
// deluge-web and deluged are separate processes. auth.login authenticates
// against deluge-web alone; until the session is bound to a daemon, deluge-web
// has nothing to proxy core.* methods to and fails all of them while auth
// still succeeds. Where deluge-web auto-connects to a single local daemon the
// step is invisible, which is how Bindery got away without it (#2204).
//
// Idempotent: web.connected short-circuits when a daemon is already attached,
// so the common path costs one extra call per login and nothing per RPC.
func (c *Client) connectDaemon(ctx context.Context) error {
	// authenticated=false throughout: auth.login already put the session cookie
	// in the jar, and asking call() to authenticate would re-enter Login from
	// inside Login.
	var connected bool
	if err := c.call(ctx, false, "web.connected", []any{}, &connected); err != nil {
		return fmt.Errorf("deluge: could not check whether the Web UI has a daemon: %w", err)
	}
	if connected {
		return nil
	}

	hosts, err := c.daemonHosts(ctx)
	if err != nil {
		return err
	}
	switch len(hosts) {
	case 0:
		return fmt.Errorf("%w, and no daemon host is configured for it. Open the Deluge Web UI, add your deluged under Connection Manager, and connect to it", errNoDaemon)
	case 1:
		// The unambiguous case, and the only one Bindery acts on.
	default:
		labels := make([]string, 0, len(hosts))
		for _, h := range hosts {
			labels = append(labels, h.label())
		}
		// Guessing would be worse than refusing: picking the wrong daemon sends
		// every grab somewhere the importer never looks, and it would look like
		// a silent success.
		return fmt.Errorf("%w, and %d hosts are configured (%s), so Bindery will not guess which one you meant. Open the Deluge Web UI, connect to the daemon you want Bindery to use, and tick its auto connect option", errNoDaemon, len(hosts), strings.Join(labels, ", "))
	}

	var methods any
	if err := c.call(ctx, false, "web.connect", []any{hosts[0].id}, &methods); err != nil {
		return fmt.Errorf("%w: connecting it to %s failed: %w", errNoDaemon, hosts[0].label(), err)
	}
	return nil
}

// daemonHosts lists the daemons configured in the Web UI's Connection Manager.
//
// Rows are positional arrays whose width varies by Deluge version: 2.x sends
// [id, host, port, username], 1.3.x sent [id, host, port, status, version].
// Only the leading three carry meaning here, so anything past them is skipped
// rather than decoded.
func (c *Client) daemonHosts(ctx context.Context) ([]daemonHost, error) {
	var rows [][]json.RawMessage
	if err := c.call(ctx, false, "web.get_hosts", []any{}, &rows); err != nil {
		return nil, fmt.Errorf("deluge: could not list the configured daemon hosts: %w", err)
	}
	hosts := make([]daemonHost, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		var h daemonHost
		if err := json.Unmarshal(row[0], &h.id); err != nil || h.id == "" {
			continue
		}
		if len(row) > 1 {
			_ = json.Unmarshal(row[1], &h.addr)
		}
		if len(row) > 2 {
			_ = json.Unmarshal(row[2], &h.port)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// AddTorrent submits a magnet link or torrent URL and returns the torrent hash.
//
// seedRatio carries the per-indexer override (#883): a non-negative value is
// applied via core.set_torrent_stop_ratio after the hash resolves. Deluge's
// RPC accepts only non-negative floats here, so the -1 unlimited sentinel (and
// a nil pointer) skips the call entirely, leaving Deluge's global default in
// place. Ratio-limit errors are non-fatal: the torrent is already added, so a
// failure to tighten the ratio must not fail the grab.
//
// A torrent Deluge already holds resolves to that torrent's hash instead of
// failing the grab (#2289), and the label and ratio still apply to it; see
// existingTorrent.
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
		return "", errNoHashReturned
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

// addMagnet calls core.add_torrent_magnet, which returns the hash directly.
// When Deluge already holds the torrent, the hash is the magnet's btih
// instead; see existingTorrent.
func (c *Client) addMagnet(ctx context.Context, magnet string) (string, error) {
	var hash string
	err := c.call(ctx, true, "core.add_torrent_magnet", []any{magnet, map[string]any{}}, &hash)
	if err == nil && strings.TrimSpace(hash) != "" {
		return hash, nil
	}
	if err != nil && !alreadyInSession(err) {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	existing, err := c.existingTorrent(ctx, infohash.Normalize(infohash.FromMagnet(magnet)), err)
	if err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	return existing, nil
}

// errNoHashReturned is an add that came back with neither a hash nor an
// error. Deluge 1.3 answers that way when libtorrent refuses a torrent the
// session already holds.
var errNoHashReturned = errors.New("deluge accepted torrent but did not return a hash")

// alreadyInSession reports whether err is Deluge refusing an add because the
// session already holds the torrent. Deluge 2.x raises AddTorrentError
// ("Torrent already in session (<hash>).") from TorrentManager, and deluge-web
// relays it as the RPC error message.
func alreadyInSession(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already in session")
}

// existingTorrent finishes an add Deluge did not complete because it already
// holds the torrent, which is what re-grabbing a release that is still seeding
// looks like (#2289). The content is there, so the grab succeeds with the
// existing torrent's hash, the way qBittorrent's 409 does (#769).
//
// hash is derived by the caller from the magnet's btih or the .torrent's info
// dictionary. It is believed only once Deluge lists it; otherwise the grab
// fails with Deluge's own reason. addErr is the refusal, or nil when Deluge
// answered null.
func (c *Client) existingTorrent(ctx context.Context, hash string, addErr error) (string, error) {
	cause := addErr
	if cause == nil {
		cause = errNoHashReturned
	}
	if hash == "" {
		return "", fmt.Errorf("%w, and the torrent's hash could not be derived to look for it", cause)
	}
	present, err := c.hasTorrent(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("%w, and checking whether Deluge holds %s failed: %w", cause, hash, err)
	}
	if !present {
		return "", fmt.Errorf("%w, and %s is not in Deluge's torrent list", cause, hash)
	}
	slog.Info("deluge: torrent already in the session, reusing it", "hash", hash)
	return hash, nil
}

// hasTorrent reports whether the session holds hash. core.get_torrents_status
// with an id filter drops ids the session does not know, so an unknown hash
// comes back as an empty map rather than an error. The filter must be a list:
// Deluge iterates it, and a bare string would be read one character at a time.
func (c *Client) hasTorrent(ctx context.Context, hash string) (bool, error) {
	var status map[string]json.RawMessage
	filter := map[string]any{"id": []string{hash}}
	if err := c.call(ctx, true, "core.get_torrents_status", []any{filter, []string{"hash"}}, &status); err != nil {
		return false, err
	}
	for h := range status {
		if strings.EqualFold(h, hash) {
			return true, nil
		}
	}
	return false, nil
}

// addTorrentFile fetches the .torrent bytes inside Bindery (so the request
// runs in Bindery's DNS namespace, not Deluge's VPN container) and submits
// the content to Deluge via core.add_torrent_file.  If the URL resolves to a
// magnet redirect the caller should use addMagnet instead — this function
// returns a non-nil magnetURL in that case and the caller switches paths.
//
// core.add_torrent_file returns the infohash directly, so no before/after
// snapshot polling is needed. When Deluge already holds the torrent, the hash
// is computed from the file's info dictionary instead; see existingTorrent.
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
	var added string
	err = c.call(ctx, true, "core.add_torrent_file", []any{filename, filedump, map[string]any{}}, &added)
	if err == nil && strings.TrimSpace(added) != "" {
		return strings.ToLower(strings.TrimSpace(added)), "", nil
	}
	if err != nil && !alreadyInSession(err) {
		return "", "", fmt.Errorf("core.add_torrent_file: %w", err)
	}
	existing, err := c.existingTorrent(ctx, infohash.FromTorrentFile(fetched.data), err)
	if err != nil {
		return "", "", fmt.Errorf("core.add_torrent_file: %w", err)
	}
	return existing, "", nil
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
	// core.remove_torrent answers False when the daemon declines the removal
	// without raising, which doCall's error-member check cannot see: the reply
	// is a plain 200 with no error object, so discarding this flag reported
	// every such refusal as a success (#2192). A pointer distinguishes an
	// explicit false from a daemon that answered null; only the former is a
	// refusal.
	var result *bool
	if err := c.call(ctx, true, "core.remove_torrent", []any{hash, deleteFiles}, &result); err != nil {
		return fmt.Errorf("remove torrent: %w", err)
	}
	if result != nil && !*result {
		return fmt.Errorf("deluge rejected the removal of torrent %s (core.remove_torrent returned false); check the Deluge daemon's log for the reason", hash)
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
