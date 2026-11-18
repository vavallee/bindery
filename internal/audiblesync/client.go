// Package audiblesync is a client for Audible Sync, a self-hosted service that
// syncs an Audible library and serves the account's wishlist and library over
// a bearer-token HTTP API.
//
// The integration is opt in. An operator who runs Audible Sync points Bindery
// at it, and the wishlist import list (internal/audiblesynclistsyncer) reads
// the account's wishlist through it. See docs/third-party-data.md §Audible
// Sync.
//
// API reference: the service's /docs (OpenAPI), and docs/api.md in the
// Audible Sync repository.
package audiblesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	defaultTimeout = 15 * time.Second
	maxAttempts    = 3
	// minRequestGap paces every request this client makes. Audible Sync does
	// not throttle its callers, and a wishlist change is a request to Audible
	// on the operator's account, so Bindery keeps itself to a steady rate.
	minRequestGap = 250 * time.Millisecond
	// maxErrorBody bounds how much of an error response is read into a
	// message: a reverse proxy can answer with a whole HTML page.
	maxErrorBody = 4 << 10
)

// ErrNeedsLogin is returned when Audible Sync answers 409 because the
// account has no working Audible credentials. The fix is on the Audible Sync
// side: sign the account in again there.
var ErrNeedsLogin = errors.New("the Audible account needs to be signed in again in Audible Sync")

// ErrNotFound is returned for a 404: an unknown ASIN, account or wishlist
// entry.
var ErrNotFound = errors.New("not found in Audible Sync")

// ErrUnauthorized is returned for a 401: the API token was refused.
var ErrUnauthorized = errors.New("the API token was rejected by Audible Sync")

// APIError is any other non-success answer from Audible Sync.
type APIError struct {
	StatusCode int
	Detail     string
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("Audible Sync api error (%d)", e.StatusCode)
	}
	return fmt.Sprintf("Audible Sync api error (%d): %s", e.StatusCode, e.Detail)
}

// NormalizeBaseURL validates and canonicalises the URL the operator typed. A
// path is kept, since Audible Sync can be served under a base path
// (AUDIBLE_SYNC_BASE_PATH).
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base_url is required")
	}
	// Checked before parsing: a bare "host:port" either parses with the host
	// as its scheme or fails with a message about path segments, and neither
	// tells the operator they left off http://. The scheme is not guessed,
	// because http over https is a security relevant choice.
	if !strings.Contains(raw, "://") {
		return "", fmt.Errorf("base_url %q is missing a scheme: write it as %q, or https:// if Audible Sync is behind TLS", raw, "http://"+raw)
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
	u.Path = strings.TrimRight(u.Path, "/")
	// A pasted URL that ends in /api would double the prefix on every call.
	u.Path = strings.TrimSuffix(u.Path, "/api")
	return u.String(), nil
}

// ValidateBaseURLSecure layers the outbound SSRF policy on NormalizeBaseURL.
// Use it where an admin saves the URL; LAN and loopback addresses stay
// allowed for homelab installs, link-local and cloud metadata do not.
func ValidateBaseURLSecure(raw string) (string, error) {
	u, err := NormalizeBaseURL(raw)
	if err != nil {
		return "", err
	}
	if err := httpsec.ValidateOutboundURL(u, httpsec.PolicyService); err != nil {
		return "", fmt.Errorf("base_url %q: %w", raw, err)
	}
	return u, nil
}

// NormalizeAPIToken trims the token and refuses control characters, which
// would otherwise reach the Authorization header.
func NormalizeAPIToken(raw string) (string, error) {
	tok := strings.TrimSpace(raw)
	tok = strings.TrimPrefix(tok, "Bearer ")
	for _, r := range tok {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("api_token contains invalid control characters")
		}
	}
	return tok, nil
}

// Client talks to one Audible Sync instance. It is safe for concurrent use.
type Client struct {
	baseURL   string
	token     string
	accountID int
	http      *http.Client

	paceMu   sync.Mutex
	lastSent time.Time
	gap      time.Duration
}

// NewClient builds a client for baseURL with the given bearer token.
// accountID selects the Audible Sync account wishlist calls go through when
// they do not name one; zero lets Audible Sync choose, which is its oldest
// signed in account.
func NewClient(baseURL, token string, accountID int) (*Client, error) {
	u, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	tok, err := NormalizeAPIToken(token)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, errors.New("api_token is required")
	}
	// Same policy as the Audiobookshelf and Grimmory clients: dialled
	// directly, with every dial and redirect hop checked, so a redirect or a
	// rebound name cannot reach a link-local or metadata address.
	return &Client{
		baseURL:   u,
		token:     tok,
		accountID: accountID,
		gap:       minRequestGap,
		http: &http.Client{
			Timeout:       defaultTimeout,
			Transport:     httpsec.DirectGuardedTransport(httpsec.PolicyService),
			CheckRedirect: httpsec.RedirectGuard(httpsec.PolicyService),
		},
	}, nil
}

// BaseURL is the normalised base URL the client calls.
func (c *Client) BaseURL() string { return c.baseURL }

// AccountID is the Audible Sync account the client asks through; zero means
// Audible Sync's default.
func (c *Client) AccountID() int { return c.accountID }

// withHTTPClient swaps the transport; tests use it with httptest servers,
// which listen on loopback.
func (c *Client) withHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// withPacing changes the gap between requests; tests set it to zero.
func (c *Client) withPacing(gap time.Duration) *Client {
	c.gap = gap
	return c
}

// pace blocks until minRequestGap has passed since the previous request.
func (c *Client) pace(ctx context.Context) error {
	c.paceMu.Lock()
	wait := time.Until(c.lastSent.Add(c.gap))
	if wait < 0 {
		wait = 0
	}
	c.lastSent = time.Now().Add(wait)
	c.paceMu.Unlock()
	if wait == 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// query adds the client's account to q when one is configured and q does not
// already name one.
func (c *Client) query(q url.Values) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if c.accountID > 0 && q.Get("account_id") == "" {
		q.Set("account_id", strconv.Itoa(c.accountID))
	}
	return q
}

// do sends one request and decodes a JSON answer into out (nil to discard).
// It retries transport errors and 502/503/504, the answers Audible Sync or a
// proxy in front of it gives while restarting or while Audible is slow.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	_, err := c.doStatus(ctx, method, path, q, body, out)
	return err
}

func (c *Client) doStatus(ctx context.Context, method, path string, q url.Values, body, out any) (int, error) {
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return 0, fmt.Errorf("encode Audible Sync request: %w", err)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.pace(ctx); err != nil {
			return 0, err
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", useragent.Get())
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			lastErr = fmt.Errorf("request to Audible Sync failed: %s %s: %w", method, path, err)
			if attempt < maxAttempts && sleepCtx(ctx, backoff(attempt)) == nil {
				continue
			}
			return 0, lastErr
		}

		status := resp.StatusCode
		err = handleResponse(resp, out)
		var apiErr *APIError
		if errors.As(err, &apiErr) && retryable(apiErr.StatusCode) && attempt < maxAttempts {
			lastErr = err
			if sleepCtx(ctx, backoff(attempt)) != nil {
				return 0, lastErr
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		return status, nil
	}
	return 0, lastErr
}

func handleResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode Audible Sync response: %w", err)
		}
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	detail := errorDetail(raw)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		if detail != "" {
			return fmt.Errorf("%w: %s", ErrNotFound, detail)
		}
		return ErrNotFound
	case http.StatusConflict:
		if looksLikeNeedsLogin(detail) {
			return fmt.Errorf("%w (%s)", ErrNeedsLogin, detail)
		}
	}
	return &APIError{StatusCode: resp.StatusCode, Detail: detail}
}

// errorDetail pulls FastAPI's {"detail": ...} out of an error body. A
// validation error carries a list there; its first message is enough. A body
// that is not JSON (a proxy's page) is returned trimmed and shortened.
func errorDetail(raw []byte) string {
	var env struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Detail) > 0 {
		var s string
		if json.Unmarshal(env.Detail, &s) == nil {
			return s
		}
		var list []struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(env.Detail, &list) == nil && len(list) > 0 {
			return list[0].Msg
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + " […]"
	}
	return httpsec.RedactSecrets(s)
}

func looksLikeNeedsLogin(detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "sign in") || strings.Contains(d, "signed in") ||
		strings.Contains(d, "signing in") ||
		strings.Contains(d, "login") || strings.Contains(d, "log in") ||
		strings.Contains(d, "credentials")
}

func retryable(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 500 * time.Millisecond
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
