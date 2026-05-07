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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout   = 10 * time.Second
	defaultUserAgent = "bindery/dev"
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
	return e.Message
}

// StatusResponse is the shape returned by GET /api/status.
type StatusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
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
func UserAgent(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return defaultUserAgent
	}
	return "bindery/" + version
}

// Client is an HTTP client for the Grimmory REST API.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
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
	return &Client{
		baseURL:   u,
		apiKey:    k,
		userAgent: defaultUserAgent,
		http: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
		},
	}, nil
}

// WithUserAgent overrides the User-Agent header sent with every request.
func (c *Client) WithUserAgent(ua string) *Client {
	c.userAgent = ua
	return c
}

// Ping calls GET /api/status to verify connectivity and authentication.
func (c *Client) Ping(ctx context.Context) (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.do(ctx, http.MethodGet, "/api/status", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
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
		return json.Unmarshal(raw, out)
	}
	return nil
}
