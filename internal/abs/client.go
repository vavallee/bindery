// Package abs provides Audiobookshelf client, normalization, and import logic.
package abs

import (
	"bytes"
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

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	defaultTimeout = 10 * time.Second
	maxAttempts    = 3
)

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("abs api error (%d)", e.StatusCode)
	}
	return e.Message
}

// NormalizeBaseURL validates and canonicalises the Audiobookshelf base URL;
// see httpsec.NormalizeServiceBaseURL.
func NormalizeBaseURL(raw string) (string, error) {
	return httpsec.NormalizeServiceBaseURL(raw, "Audiobookshelf")
}

// ValidateBaseURLSecure is NormalizeBaseURL plus the outbound address policy,
// for the settings-save boundary; see httpsec.ValidateServiceBaseURL.
func ValidateBaseURLSecure(raw string) (string, error) {
	return httpsec.ValidateServiceBaseURL(raw, "Audiobookshelf")
}

// UserAgent delegates to the shared canonical helper so abs traffic carries
// the same identity as the rest of Bindery's outbound HTTP.
func UserAgent(version string) string {
	return useragent.Build(version)
}

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	userAgent  string
}

func NewClient(baseURL, apiKey string) (*Client, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	normalizedKey, err := httpsec.NormalizeAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	if normalizedKey == "" {
		return nil, errors.New("api_key is required")
	}
	return &Client{
		baseURL: normalized,
		apiKey:  normalizedKey,
		// Dialled directly (never via BINDERY_OUTBOUND_PROXY), with the dial
		// and every redirect hop checked so a redirect or a rebound hostname
		// cannot reach a link-local or metadata address.
		httpClient: &http.Client{
			Timeout:       defaultTimeout,
			Transport:     httpsec.DirectGuardedTransport(httpsec.PolicyService),
			CheckRedirect: httpsec.RedirectGuard(httpsec.PolicyService),
		},
		userAgent: useragent.Get(),
	}, nil
}

func (c *Client) WithVersion(version string) *Client {
	c.userAgent = UserAgent(version)
	return c
}

func (c *Client) WithUserAgent(userAgent string) *Client {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = useragent.Get()
	}
	c.userAgent = userAgent
	return c
}

func (c *Client) Authorize(ctx context.Context) (*AuthorizeResponse, error) {
	var out AuthorizeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/authorize", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListLibraries(ctx context.Context) ([]Library, error) {
	var out librariesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/libraries", nil, &out); err != nil {
		return nil, err
	}
	return out.Libraries, nil
}

func (c *Client) GetLibrary(ctx context.Context, id string) (*Library, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("library_id is required")
	}
	var out Library
	if err := c.doJSON(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListLibraryItems(ctx context.Context, libraryID string, page, limit int) (*LibraryItemsPage, error) {
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return nil, errors.New("library_id is required")
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	if page >= 0 {
		values.Set("page", fmt.Sprintf("%d", page))
	}
	values.Set("minified", "1")
	var out LibraryItemsPage
	if err := c.doJSON(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/items?"+values.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScanLibrary triggers an ABS library folder scan. ABS will walk all folders
// configured for the library and surface any newly-added items. This is called
// after a successful audiobook import so the item appears in ABS promptly
// rather than waiting for the next scheduled scan (Bug #10).
func (c *Client) ScanLibrary(ctx context.Context, libraryID string) error {
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return errors.New("library_id is required")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/libraries/"+url.PathEscape(libraryID)+"/scan", nil, nil)
}

func (c *Client) GetLibraryItem(ctx context.Context, id string) (*LibraryItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("item_id is required")
	}
	values := url.Values{}
	values.Set("expanded", "1")
	values.Set("include", "authors")
	var out LibraryItem
	if err := c.doJSON(ctx, http.MethodGet, "/api/items/"+url.PathEscape(id)+"?"+values.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, out any) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// A reader is consumed by the first attempt, so each attempt gets its own.
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts-1 && shouldRetry(err) {
				sleepBackoff(ctx, attempt)
				continue
			}
			return err
		}

		if resp.StatusCode >= http.StatusInternalServerError && attempt < maxAttempts-1 {
			drainAndClose(resp.Body)
			sleepBackoff(ctx, attempt)
			continue
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			err := decodeAPIError(resp)
			_ = resp.Body.Close()
			return err
		}
		if out == nil {
			_ = resp.Body.Close()
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
		_ = resp.Body.Close()
		return nil
	}
	return lastErr
}

func decodeAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	message := strings.TrimSpace(string(body))
	if message != "" {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if s, ok := payload["error"].(string); ok && s != "" {
				message = s
			} else if s, ok := payload["message"].(string); ok && s != "" {
				message = s
			}
		}
	}
	if message == "" {
		message = resp.Status
	}
	return &APIError{StatusCode: resp.StatusCode, Message: message}
}

func sleepBackoff(ctx context.Context, attempt int) {
	delay := 150 * time.Millisecond
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func shouldRetry(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func drainAndClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}
