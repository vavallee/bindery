// Package grimmory provides a client for the Grimmory self-hosted digital library API.
// API reference: https://grimmory.org/docs/api (enable with API_DOCS_ENABLED=true).
// NOTE: Grimmory's REST API is still maturing. Endpoint paths here are based on
// the available OpenAPI docs and may change before a stable API freeze.
package grimmory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	defaultTimeout = 10 * time.Second
	uploadTimeout  = 5 * time.Minute
)

// APIError represents an HTTP error from the Grimmory API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("grimmory api error (%d)", e.StatusCode)
	}
	// Always carry the upstream status: a reverse proxy in front of Grimmory
	// answering 502 with "Bad Gateway" must read as "Grimmory's URL replied
	// 502", not as an opaque failure inside Bindery (#1431). Bodies can be
	// whole proxy error pages — keep the rendered message short.
	msg := e.Message
	if len(msg) > 300 {
		msg = strings.ToValidUTF8(msg[:300], "") + " […]"
	}
	return fmt.Sprintf("grimmory api error (%d): %s", e.StatusCode, msg)
}

// StatusResponse is the connectivity/version summary Ping distills from
// Grimmory's health endpoint (see healthcheckEnvelope).
type StatusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// healthcheckEnvelope is the shape returned by GET /api/v1/healthcheck. Grimmory
// wraps the interesting fields in a "data" object alongside a top-level
// message/status/timestamp; only the nested status and version matter here.
type healthcheckEnvelope struct {
	Data struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	} `json:"data"`
}

// NormalizeBaseURL validates and canonicalises the user-supplied server URL.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base_url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base_url %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("base_url %q is missing a host", raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String(), nil
}

// ValidateBaseURLSecure layers an SSRF policy check on top of NormalizeBaseURL.
// Use this at the admin-input boundary (settings save) to refuse base URLs
// that point at link-local or cloud-metadata endpoints. Loopback and RFC1918
// are still allowed via PolicyLAN for homelab deployments. NewClient callers
// should keep using NormalizeBaseURL directly so test fixtures with httptest
// (loopback) still work.
func ValidateBaseURLSecure(raw string) (string, error) {
	u, err := NormalizeBaseURL(raw)
	if err != nil {
		return "", err
	}
	if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyLANLoopback); err != nil {
		return "", fmt.Errorf("base_url %q: %w", raw, err)
	}
	return u, nil
}

// NormalizeAPIKey strips whitespace and rejects control characters.
func NormalizeAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("api_key contains invalid control characters")
		}
	}
	return key, nil
}

// UserAgent returns the Bindery User-Agent string to send to Grimmory.
// Delegates to the shared canonical helper so every external client emits
// the same identity (see internal/useragent).
func UserAgent(version string) string {
	return useragent.Build(version)
}

// Client is an HTTP client for the Grimmory REST API.
type Client struct {
	baseURL   string
	apiKey    string
	username  string
	password  string
	userAgent string
	http      *http.Client
	upload    *http.Client

	// JWT session state, populated by login/refresh when username/password
	// auth is in use (current Grimmory has no static API tokens — see #818).
	// Guarded by mu so the per-import pusher and a bulk sync can share one
	// client.
	mu           sync.Mutex
	accessToken  string
	refreshToken string
}

// NewClient constructs a Client after validating and normalising the provided credentials.
func NewClient(baseURL, apiKey string) (*Client, error) {
	u, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	k, err := NormalizeAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	return &Client{
		baseURL:   u,
		apiKey:    k,
		userAgent: useragent.Get(),
		http: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
		// Book files can be tens of MB over a slow LAN link; give uploads
		// their own generous deadline instead of the 10s API timeout.
		upload: &http.Client{
			Timeout:   uploadTimeout,
			Transport: transport,
		},
	}, nil
}

// WithUserAgent overrides the User-Agent header sent with every request.
func (c *Client) WithUserAgent(ua string) *Client {
	c.userAgent = ua
	return c
}

// WithCredentials sets the Grimmory username/password used for JWT login.
// Ignored when a static API key is configured (the key wins, matching the
// forward-compat intent of the api_key field: if upstream ships token auth,
// setting a token bypasses the login round-trips).
func (c *Client) WithCredentials(username, password string) *Client {
	c.username = strings.TrimSpace(username)
	c.password = password
	return c
}

// Ping calls GET /api/v1/healthcheck to confirm the base URL points at a real
// Grimmory API and to read its version. That endpoint is public and returns a
// JSON envelope with the status and version nested under "data"; it needs no
// authentication. Verifying that the configured credentials actually work is a
// separate concern owned by VerifyAuth, which the "Test connection" handler
// calls right after a successful Ping.
//
// Earlier versions probed GET /api/status and treated its 401 as "guarded by a
// session" (#1448). But current Grimmory (v3.x) has no /api/status route at
// all: its Spring security layer answers any unmapped /api/** path with a 401
// Whitelabel page, which read like an auth wall but was really a 404 in
// disguise — so the probe could never succeed (#1485).
func (c *Client) Ping(ctx context.Context) (*StatusResponse, error) {
	var env healthcheckEnvelope
	if err := c.do(ctx, http.MethodGet, "/api/v1/healthcheck", nil, &env); err != nil {
		return nil, err
	}
	return &StatusResponse{Status: env.Data.Status, Version: env.Data.Version}, nil
}

// do issues a request carrying only a statically-configured API key as auth
// (or none). Used by the login/refresh calls, which must not present a JWT —
// they are how the JWT is obtained.
//
// Grimmory v3.x does not have API keys; the maintainer confirmed this on
// grimmory-tools/grimmory#1487 (#818). Send the Authorization header only when
// the user actually configured one — sending "Bearer " with an empty token is
// a no-op against current Grimmory and just noise on the wire.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	return c.doWithToken(ctx, method, path, c.apiKey, body, out)
}

// doWithToken issues a request carrying the given bearer token (empty token =
// no Authorization header).
func (c *Client) doWithToken(ctx context.Context, method, path, token string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			// A 2xx with a non-JSON body is almost always a SPA fallback page
			// or a reverse proxy answering in Grimmory's place (#1485) — say
			// so instead of surfacing json's "invalid character '<'".
			return fmt.Errorf("grimmory: %s %s returned HTTP %d with a non-JSON body (Content-Type %q) — the base URL likely points at a web UI or proxy rather than the Grimmory API: %w",
				method, path, resp.StatusCode, resp.Header.Get("Content-Type"), err)
		}
	}
	return nil
}

// ── Authentication ───────────────────────────────────────────────────────────
//
// Grimmory (like its Booklore ancestor) authenticates API clients with a JWT
// pair from POST /api/v1/auth/login and rotates it via POST /api/v1/auth/refresh.
// There are no static API tokens yet (#818 / grimmory-tools#1487); the api_key
// field is honoured as a plain Bearer token for forward compatibility and, when
// set, short-circuits the whole login flow.

type tokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// ErrNoCredentials is returned when an authenticated call is attempted with
// neither an API key nor a username/password configured.
var ErrNoCredentials = errors.New("grimmory: username/password (or api key) required")

// login performs a fresh username/password login and stores the token pair.
func (c *Client) login(ctx context.Context) (string, error) {
	if c.username == "" {
		return "", ErrNoCredentials
	}
	body, _ := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	var pair tokenPair
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(body)), &pair); err != nil {
		return "", fmt.Errorf("grimmory login: %w", err)
	}
	if pair.AccessToken == "" {
		return "", errors.New("grimmory login: response carried no accessToken")
	}
	c.mu.Lock()
	c.accessToken, c.refreshToken = pair.AccessToken, pair.RefreshToken
	c.mu.Unlock()
	return pair.AccessToken, nil
}

// bearer returns the Authorization bearer value for an authenticated call.
// A configured static API key always wins. Otherwise the cached JWT is used;
// force discards the cache and re-acquires (refresh first, then full login).
func (c *Client) bearer(ctx context.Context, force bool) (string, error) {
	if c.apiKey != "" {
		return c.apiKey, nil
	}
	c.mu.Lock()
	cached, refresh := c.accessToken, c.refreshToken
	c.mu.Unlock()
	if cached != "" && !force {
		return cached, nil
	}
	if refresh != "" && force {
		body, _ := json.Marshal(map[string]string{"refreshToken": refresh})
		var pair tokenPair
		err := c.do(ctx, http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(string(body)), &pair)
		if err == nil && pair.AccessToken != "" {
			c.mu.Lock()
			c.accessToken, c.refreshToken = pair.AccessToken, pair.RefreshToken
			c.mu.Unlock()
			return pair.AccessToken, nil
		}
		// Refresh token expired or rejected — fall through to a full login.
	}
	return c.login(ctx)
}

// VerifyAuth proves the configured credentials can authenticate: a no-op for
// a static API key (nothing to verify without a write call), a real login
// round-trip for username/password. Used by the Settings "Test" button.
func (c *Client) VerifyAuth(ctx context.Context) error {
	if c.apiKey != "" || c.username == "" {
		return nil
	}
	_, err := c.login(ctx)
	return err
}

// HasCredentials reports whether the client is configured for authenticated
// calls at all (static key or username/password).
func (c *Client) HasCredentials() bool {
	return c.apiKey != "" || c.username != ""
}

// ── BookDrop upload ──────────────────────────────────────────────────────────

// bookDropResponse is the subset of Grimmory's Book DTO the upload returns
// that Bindery cares about.
type bookDropResponse struct {
	ID int64 `json:"id"`
}

// UploadBookDrop pushes the file at filePath into Grimmory's BookDrop inbox
// (POST /api/v1/files/upload/bookdrop, multipart). Grimmory's own ingest
// pipeline takes it from there — metadata match, dedup review, shelving —
// which is exactly the treatment a Bindery import should get on the other
// side. Returns Grimmory's book id when the response carries one (0 when the
// file landed in the review queue without an id yet).
//
// A 401 is retried once after forcing re-authentication, so an expired access
// token from a long-lived Pusher heals transparently.
func (c *Client) UploadBookDrop(ctx context.Context, filePath string) (int64, error) {
	token, err := c.bearer(ctx, false)
	if err != nil {
		return 0, err
	}
	id, err := c.uploadBookDropOnce(ctx, filePath, token)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && c.apiKey == "" {
		if token, err = c.bearer(ctx, true); err != nil {
			return 0, err
		}
		return c.uploadBookDropOnce(ctx, filePath, token)
	}
	return id, err
}

func (c *Client) uploadBookDropOnce(ctx context.Context, filePath, token string) (int64, error) {
	f, err := os.Open(filePath) // #nosec G304 -- path comes from Bindery's own book_files records
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Stream the multipart body through a pipe so a large book never sits
	// fully in memory.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(filePath))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/files/upload/bookdrop", pr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("User-Agent", c.userAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.upload.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return 0, &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	var book bookDropResponse
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &book); err != nil {
			// A non-JSON 2xx still means the file landed; id is best-effort.
			return 0, nil
		}
	}
	return book.ID, nil
}
