// Package qbittorrent provides a client for the qBittorrent WebUI API v2,
// used to submit magnet/torrent URLs and poll status for torrent downloads.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
)

// AuthError signals that qBittorrent responded but rejected the login.
// Test() inspects this type via errors.As so it can avoid wrapping auth
// failures with the misleading "could not reach + use container name"
// hint that only applies to actual transport failures.
type AuthError struct {
	Status int
	Body   string
}

func (e *AuthError) Error() string {
	switch {
	case e.Status == http.StatusForbidden && e.Body == "":
		return "qBittorrent auth failed (HTTP 403, empty body): your IP is most likely banned after repeated failed logins. " +
			"Clear it in qBit (Tools → Options → Web UI → IP filtering, or the banlist in qBittorrent.conf — restart of qBit may not clear it because the banlist is persisted)."
	case e.Status == http.StatusOK && e.Body == "Fails.":
		return "qBittorrent auth failed: credentials rejected (check the WebUI username/password matches what's saved in bindery)."
	case e.Status == http.StatusForbidden:
		return fmt.Sprintf("qBittorrent auth failed (HTTP 403): %s — host-header validation may be rejecting bindery; disable it in Tools → Options → Web UI, or whitelist the bindery container's hostname.", e.Body)
	case e.Status != http.StatusOK:
		return fmt.Sprintf("qBittorrent auth failed (HTTP %d): %s", e.Status, e.Body)
	default:
		return fmt.Sprintf("qBittorrent auth failed: %s", e.Body)
	}
}

// hashPollTimeout is the maximum time to wait for a newly-added torrent's hash
// to appear in the unfiltered torrent list.
var hashPollTimeout = 30 * time.Second

// maxTorrentFileBytes caps the torrent payload Bindery will fetch before
// uploading it to qBittorrent.
var maxTorrentFileBytes int64 = 50 << 20

// Client interacts with the qBittorrent WebUI API v2.
// Authentication is cookie-based: Login() obtains a SID cookie which is
// stored in the embedded http.Client's cookie jar and sent automatically on
// subsequent requests.
//
// Field mapping for DownloadClient storage:
//   - APIKey  → password  (qBittorrent uses username/password, not an API key)
//   - URLBase → reverse-proxy subpath, appended to baseURL (#369)
type Client struct {
	baseURL            string
	username           string
	password           string
	http               *http.Client
	validateTorrentURL func(string) error
	mu                 sync.Mutex // guards loggedIn
	addMu              sync.Mutex // serialises AddTorrent: keeps before/after hash diff atomic
	loggedIn           bool
}

// New creates a qBittorrent client. urlBase is the optional reverse-proxy
// subpath (e.g. "/qbit") that will be appended between the host:port and
// the standard /api/v2 endpoints; leave it empty for a direct connection.
func New(host string, port int, username, password, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}

	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  fmt.Sprintf("%s://%s:%d%s", scheme, host, port, urlbase.Normalize(urlBase)),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second, Jar: jar},
		validateTorrentURL: func(raw string) error {
			return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLAN)
		},
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
	// qBittorrent v5.x enforces CSRF protection on /auth/login and rejects
	// requests without matching Origin and Referer headers (often silently —
	// the empty-body 403 that motivated AuthError above). v4.x ignores these
	// headers, so setting them is safe across versions.
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	text := strings.TrimSpace(string(body))

	// qBittorrent v4.x returns `200 OK` + body "Ok." on a successful login;
	// v5.x returns `204 No Content` with an empty body. Accept both.
	if resp.StatusCode == http.StatusNoContent {
		c.mu.Lock()
		c.loggedIn = true
		c.mu.Unlock()
		return nil
	}

	if resp.StatusCode != http.StatusOK || text == "Fails." {
		return &AuthError{Status: resp.StatusCode, Body: text}
	}

	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// Test verifies connectivity by fetching the application version. The error
// wording adapts to the failure mode: auth/config issues (the server
// responded but rejected us) get a targeted hint; transport failures (the
// server didn't respond at all) get a hint based on the error class.
func (c *Client) Test(ctx context.Context) error {
	if _, err := c.get(ctx, "/api/v2/app/version"); err != nil {
		var authErr *AuthError
		if errors.As(err, &authErr) {
			// Server responded — this is an auth/config issue, not unreachable.
			return fmt.Errorf("connected to qBittorrent at %s but %w", c.baseURL, err)
		}
		return fmt.Errorf("could not reach qBittorrent at %s — %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	return nil
}

// AddTorrent submits a magnet link or torrent URL to qBittorrent for download
// and returns the torrent hash when it can be determined.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, category, savePath string) (string, error) {
	// Serialise concurrent AddTorrent calls so that each goroutine's
	// before-snapshot → submit → poll sequence is atomic. Without this, two
	// concurrent calls both snapshot an identical beforeSet, both submit their
	// torrents, and then both resolve to the same "newest" torrent hash — the
	// root cause of Bug 2.
	c.addMu.Lock()
	defer c.addMu.Unlock()

	// Snapshot all existing hashes (unfiltered) so we can detect newly-added
	// items regardless of which category qBittorrent assigns them initially.
	// For indirect URLs (e.g. Prowlarr Torznab redirects), qBittorrent must
	// follow the redirect and fetch the remote .torrent file before it assigns
	// metadata and category. Polling with a category filter during this window
	// returns nothing; the detection deadline expires and the hash is lost
	// (#418). The before/after hash-set diff already uniquely identifies the
	// new torrent, so the category filter is omitted from both polling calls.
	beforeSet := map[string]struct{}{}
	if before, err := c.GetTorrents(ctx, ""); err == nil {
		for _, t := range before {
			beforeSet[strings.ToLower(t.Hash)] = struct{}{}
		}
	}

	if err := c.ensureLoggedIn(ctx); err != nil {
		return "", err
	}

	var req *http.Request
	submitted := magnetOrURL
	// torrentFile holds the raw .torrent bytes when the add is a file upload,
	// so a 409 duplicate response can still recover the infohash.
	var torrentFile []byte
	if strings.HasPrefix(magnetOrURL, "http://") || strings.HasPrefix(magnetOrURL, "https://") {
		// Fetch the .torrent content in Bindery so qBittorrent never needs to
		// reach the indexer URL directly (which may be a Docker-only hostname
		// like prowlarr:9696 that qBittorrent can't resolve from its network).
		fetched, err := c.fetchTorrentContent(ctx, magnetOrURL)
		if err != nil {
			return "", fmt.Errorf("fetch torrent content: %w", err)
		}

		if fetched.magnetURL != "" {
			submitted = fetched.magnetURL
			form := url.Values{"urls": {fetched.magnetURL}}
			if category != "" {
				form.Set("category", category)
			}
			if savePath != "" {
				form.Set("savepath", savePath)
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost,
				c.baseURL+"/api/v2/torrents/add",
				strings.NewReader(form.Encode()))
			if err != nil {
				return "", fmt.Errorf("build add request: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			torrentFile = fetched.data
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			fw, err := mw.CreateFormFile("torrents", "upload.torrent")
			if err != nil {
				return "", fmt.Errorf("build multipart: %w", err)
			}
			if _, err := fw.Write(fetched.data); err != nil {
				return "", fmt.Errorf("write torrent content: %w", err)
			}
			if category != "" {
				if err := mw.WriteField("category", category); err != nil {
					return "", fmt.Errorf("write torrent category: %w", err)
				}
			}
			if savePath != "" {
				if err := mw.WriteField("savepath", savePath); err != nil {
					return "", fmt.Errorf("write torrent savepath: %w", err)
				}
			}
			if err := mw.Close(); err != nil {
				return "", fmt.Errorf("close multipart: %w", err)
			}

			req, err = http.NewRequestWithContext(ctx, http.MethodPost,
				c.baseURL+"/api/v2/torrents/add", &buf)
			if err != nil {
				return "", fmt.Errorf("build add request: %w", err)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
		}
	} else {
		form := url.Values{"urls": {magnetOrURL}}
		if category != "" {
			form.Set("category", category)
		}
		if savePath != "" {
			form.Set("savepath", savePath)
		}
		var err error
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/api/v2/torrents/add",
			strings.NewReader(form.Encode()))
		if err != nil {
			return "", fmt.Errorf("build add request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("add torrent: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))

	// qBittorrent answers POST /torrents/add with 409 Conflict when it already
	// holds the torrent. The content is effectively available, so recover the
	// existing torrent's hash and proceed instead of failing the grab.
	if resp.StatusCode == http.StatusConflict {
		hash := infoHashFromMagnet(submitted)
		if hash == "" {
			hash = infoHashFromTorrentFile(torrentFile)
		}
		if hash == "" {
			return "", fmt.Errorf("add torrent: qBittorrent reports the torrent is already present but its hash could not be determined")
		}
		if category != "" {
			_ = c.setCategory(ctx, hash, category)
		}
		slog.Info("add torrent: already present in qBittorrent, reusing existing torrent", "hash", hash)
		return hash, nil
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("add torrent HTTP %d: %s", resp.StatusCode, text)
	}

	// qBittorrent v4.x: plaintext body "Ok." on success, anything else is failure.
	// qBittorrent v5.x: JSON body {"added_torrent_ids":[...],"success_count":N,...}.
	// Accept either shape; on v5 prefer added_torrent_ids[0] as the infohash so we
	// can skip the hash-poll fallback when the API already returned the hash.
	v5Hash := ""
	if text != "Ok." {
		var v5 struct {
			AddedTorrentIDs []string `json:"added_torrent_ids"`
			SuccessCount    int      `json:"success_count"`
			FailureCount    int      `json:"failure_count"`
		}
		if err := json.Unmarshal(body, &v5); err != nil || (v5.SuccessCount == 0 && len(v5.AddedTorrentIDs) == 0) {
			return "", fmt.Errorf("add torrent failed: %s", text)
		}
		if len(v5.AddedTorrentIDs) > 0 {
			v5Hash = strings.ToLower(strings.TrimSpace(v5.AddedTorrentIDs[0]))
		}
	}

	if v5Hash != "" {
		return v5Hash, nil
	}
	if infoHash := infoHashFromMagnet(submitted); infoHash != "" {
		return infoHash, nil
	}

	// Poll the unfiltered torrent list until the new torrent appears — qBittorrent
	// must fetch and parse the .torrent file before the hash is visible, which can
	// take a few seconds for remote URLs (e.g. Prowlarr Torznab redirects).
	// We poll unfiltered so we find the torrent regardless of which category
	// qBittorrent has assigned at the moment it first appears.
	deadline := time.Now().Add(hashPollTimeout)
	var lastAfter []Torrent
	for {
		after, err := c.GetTorrents(ctx, "")
		if err != nil {
			return "", fmt.Errorf("add torrent accepted but hash lookup failed: %w", err)
		}
		lastAfter = after
		var newest *Torrent
		for i := range after {
			t := &after[i]
			h := strings.ToLower(t.Hash)
			if _, seen := beforeSet[h]; seen {
				continue
			}
			if newest == nil || t.AddedOn > newest.AddedOn {
				newest = t
			}
		}
		if newest != nil {
			hash := strings.ToLower(newest.Hash)
			if category != "" {
				_ = c.setCategory(ctx, hash, category)
			}
			return hash, nil
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
	afterKeys := make([]string, 0, len(lastAfter))
	for i := range lastAfter {
		afterKeys = append(afterKeys, strings.ToLower(lastAfter[i].Hash))
	}
	slog.Error("add torrent hash lookup timed out",
		"category", category,
		"before_hashes", beforeKeys,
		"after_hashes", afterKeys,
	)
	return "", fmt.Errorf("add torrent accepted but hash could not be determined")
}

func infoHashFromMagnet(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "magnet" {
		return ""
	}
	xt := u.Query().Get("xt")
	if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
		return ""
	}
	h := strings.TrimSpace(xt[len("urn:btih:"):])
	if h == "" {
		return ""
	}
	return strings.ToLower(h)
}

type fetchedTorrentContent struct {
	data      []byte
	magnetURL string
}

// fetchTorrentContent downloads a .torrent file URL and returns its raw bytes.
// It limits the response size and validates each redirect target before fetch.
func (c *Client) fetchTorrentContent(ctx context.Context, rawURL string) (*fetchedTorrentContent, error) {
	current := rawURL
	fetchClient := &http.Client{
		Transport: c.http.Transport,
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

		resp, err := fetchClient.Do(req)
		if err != nil {
			return nil, err
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
	if c.validateTorrentURL == nil {
		return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLAN)
	}
	return c.validateTorrentURL(raw)
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
	// Windows-qBit reports paths with backslashes; downstream Linux path code
	// (filepath.Walk, PathRemap.Apply, pathIsAtOrUnder) can't process them.
	// Normalize at the API boundary so every consumer sees forward-slash form.
	for i := range torrents {
		torrents[i].SavePath = normalizePath(torrents[i].SavePath)
		torrents[i].ContentPath = normalizePath(torrents[i].ContentPath)
		torrents[i].Name = normalizePath(torrents[i].Name)
	}
	return torrents, nil
}

// GetCategories returns all configured qBittorrent categories keyed by name.
func (c *Client) GetCategories(ctx context.Context) (map[string]Category, error) {
	data, err := c.get(ctx, "/api/v2/torrents/categories")
	if err != nil {
		return nil, err
	}
	var categories map[string]Category
	if err := json.Unmarshal(data, &categories); err != nil {
		return nil, fmt.Errorf("decode categories: %w", err)
	}
	for name, category := range categories {
		if category.Name == "" {
			category.Name = name
			categories[name] = category
		}
	}
	return categories, nil
}

// GetDefaultSavePath returns qBittorrent's default save path.
func (c *Client) GetDefaultSavePath(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v2/app/defaultSavePath")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Files returns the per-torrent file list for hash. Names are paths
// relative to the torrent's save path (forward-slash normalised even when
// qBittorrent runs on Windows); for a single-file torrent dropped directly
// at the save root the entry's Name is just the file's basename.
//
// This is the authoritative list of files that belong to this torrent, used
// by the importer (issue #903) to avoid walking the shared download root
// and picking up unrelated siblings.
//
// An empty list with a nil error means qBittorrent reported no files for
// the torrent (typical for a torrent still resolving metadata). An unknown
// hash returns HTTP 404 from the API, which is surfaced as an error.
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	endpoint := "/api/v2/torrents/files?hash=" + url.QueryEscape(hash)
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var files []rpcFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("decode torrent files: %w", err)
	}
	out := make([]File, 0, len(files))
	for _, f := range files {
		name := normalizePath(f.Name)
		if name == "" {
			continue
		}
		out = append(out, File{Name: name, Size: f.Size})
	}
	return out, nil
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

// setCategory assigns a category to a torrent by hash.
func (c *Client) setCategory(ctx context.Context, hash, category string) error {
	form := url.Values{
		"hashes":   {hash},
		"category": {category},
	}
	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/torrents/setCategory",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build setCategory request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("setCategory: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
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
		if resp2.StatusCode != http.StatusOK {
			body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 512))
			return nil, fmt.Errorf("HTTP %d: %s", resp2.StatusCode, string(body2))
		}
		return io.ReadAll(resp2.Body)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
