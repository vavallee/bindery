// Package bookorbit reads a BookOrbit server so Bindery can learn which books
// a library already holds and where their files live.
//
// Bindery is MIT and BookOrbit is AGPL-3.0, so nothing here is taken from
// BookOrbit's code. The client was written against the HTTP responses of a
// running server and BookOrbit's public documentation (docs/API_KEYS.md and
// the OpenAPI reference it publishes). Every type below is typed by hand and
// carries only the fields this importer reads.
//
// The client only ever reads. It sends GET requests, plus the one POST that
// BookOrbit itself marks as safe for a read only API key (the library book
// query, which is a search with a JSON body).
package bookorbit

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
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	requestTimeout = 30 * time.Second
	maxAttempts    = 3

	// maxResponseBytes caps one response body. A page of 100 book cards is a
	// few hundred kilobytes and one book record a few kilobytes, so anything
	// near this is not the answer the importer asked for.
	defaultMaxResponseBytes = 16 << 20

	// PageSize is how many books one listing request asks for. BookOrbit
	// accepts up to 200.
	PageSize = 100

	apiKeyHeader = "X-Api-Key" //nolint:gosec // #nosec G101 -- header name, not a credential value
)

// maxResponseBytes is a variable so a test can lower it.
var maxResponseBytes = defaultMaxResponseBytes

// APIError is a non 2xx answer from BookOrbit.
type APIError struct {
	StatusCode int
	// Code is BookOrbit's errorCode, such as API_KEY_READ_ONLY, when the
	// body carried one.
	Code    string
	Message string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Code != "" {
		return fmt.Sprintf("bookorbit api error (%d %s): %s", e.StatusCode, e.Code, msg)
	}
	return fmt.Sprintf("bookorbit api error (%d): %s", e.StatusCode, msg)
}

// ShapeError means BookOrbit answered 200 with a body this importer cannot
// read: a field it depends on is missing or has another type. BookOrbit does
// not promise a stable API, so an upgrade on their side can cause this. The
// importer treats it as fatal for the whole run rather than guessing.
type ShapeError struct {
	Endpoint string
	Detail   string
}

func (e *ShapeError) Error() string {
	return fmt.Sprintf("BookOrbit answered %s in a shape this importer does not understand (%s); its API may have changed, so nothing was imported", e.Endpoint, e.Detail)
}

// NormalizeBaseURL trims and validates a BookOrbit base URL. It deliberately
// does not guess a scheme: choosing http over https for the user is a security
// relevant guess.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base URL %q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("base URL %q is missing a host", raw)
	}
	if u.User != nil {
		return "", errors.New("base URL must not carry a username or password")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	// People paste the address of the API or of a page. The importer adds
	// /api/v1 itself.
	u.Path = strings.TrimSuffix(u.Path, "/api/v1")
	u.Path = strings.TrimSuffix(u.Path, "/api")
	return u.String(), nil
}

// ValidateBaseURL is NormalizeBaseURL plus, for an address written as an IP,
// the outbound address policy Bindery applies to every service URL an admin
// types into a settings form: loopback and private LAN addresses are allowed,
// cloud metadata addresses are not.
//
// A hostname is not resolved here. Saving settings should not depend on DNS
// answering at that moment, and the answer can change afterwards anyway. The
// client's transport applies the same policy to the address it actually dials,
// on every request and every redirect hop, which is the check that counts.
func ValidateBaseURL(raw string) (string, error) {
	normalized, err := NormalizeBaseURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if net.ParseIP(u.Hostname()) == nil {
		return normalized, nil
	}
	if err := httpsec.ValidateOutboundURL(normalized, httpsec.PolicyService); err != nil {
		return "", fmt.Errorf("base URL %q: %w", raw, err)
	}
	return normalized, nil
}

// NormalizeAPIKey trims an API key and refuses one that could not be sent as
// a header value.
func NormalizeAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", errors.New("API key is required")
	}
	for _, r := range key {
		if r <= 0x20 || r == 0x7f {
			return "", errors.New("API key contains whitespace or control characters")
		}
	}
	return key, nil
}

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	userAgent  string
}

// NewClient builds a client for the server at baseURL. The transport is the
// one Bindery uses for Audiobookshelf and its other admin configured services:
// dialled directly, with the dial and every redirect hop checked against the
// service address policy.
func NewClient(baseURL, apiKey string) (*Client, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	key, err := NormalizeAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	origin, err := url.Parse(normalized)
	if err != nil {
		return nil, err
	}
	guard := httpsec.RedirectGuard(httpsec.PolicyService)
	return &Client{
		baseURL: normalized,
		apiKey:  key,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: httpsec.DirectGuardedTransport(httpsec.PolicyService),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// net/http drops Authorization when a redirect leaves the
				// host, but it forwards every other header, and the key
				// travels in one of those.
				if !sameOrigin(req.URL, origin) {
					req.Header.Del(apiKeyHeader)
				}
				return guard(req, via)
			},
		},
		userAgent: useragent.Get(),
	}, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (c *Client) WithUserAgent(userAgent string) *Client {
	if userAgent = strings.TrimSpace(userAgent); userAgent != "" {
		c.userAgent = userAgent
	}
	return c
}

// Identity is who the API key acts as.
type Identity struct {
	Username             string `json:"username"`
	AuthenticationMethod string `json:"authenticationMethod"`
}

// Me asks BookOrbit who the key belongs to. It is the cheapest authenticated
// request, so the connection test uses it.
func (c *Client) Me(ctx context.Context) (*Identity, error) {
	const endpoint = "GET /api/v1/auth/me"
	body, err := c.do(ctx, http.MethodGet, "/api/v1/auth/me", nil)
	if err != nil {
		return nil, err
	}
	var out Identity
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &ShapeError{Endpoint: endpoint, Detail: "not a JSON object"}
	}
	return &out, nil
}

// Library is one BookOrbit library.
type Library struct {
	ID   int64
	Name string
	// Folders are the library's folders as BookOrbit sees them, which are
	// paths inside its container. Shown to the admin so they can write the
	// path remap. Empty when the server did not report any.
	Folders []string
}

// ListLibraries returns the libraries the key can see. A key limited to some
// libraries only lists those.
func (c *Client) ListLibraries(ctx context.Context) ([]Library, error) {
	const endpoint = "GET /api/v1/libraries"
	body, err := c.do(ctx, http.MethodGet, "/api/v1/libraries", nil)
	if err != nil {
		return nil, err
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &ShapeError{Endpoint: endpoint, Detail: "not a JSON array"}
	}
	out := make([]Library, 0, len(raw))
	for _, entry := range raw {
		var lib Library
		if err := requireInt(entry, "id", &lib.ID); err != nil {
			return nil, &ShapeError{Endpoint: endpoint, Detail: err.Error()}
		}
		if err := requireString(entry, "name", &lib.Name); err != nil {
			return nil, &ShapeError{Endpoint: endpoint, Detail: err.Error()}
		}
		lib.Folders = decodeFolders(entry["folders"])
		out = append(out, lib)
	}
	return out, nil
}

// decodeFolders reads a library's folder list. It is display only, so an
// unfamiliar shape yields nothing rather than an error.
func decodeFolders(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var plain []string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var objects []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil
	}
	out := make([]string, 0, len(objects))
	for _, o := range objects {
		if p := strings.TrimSpace(o.Path); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// BookPage is one page of a library's book listing. The listing carries no
// file paths, so only the ids are kept and each book is then read in full.
type BookPage struct {
	IDs   []int64
	Total int
}

// ListBookIDs returns page (zero based) of the books in a library.
func (c *Client) ListBookIDs(ctx context.Context, libraryID int64, page int) (*BookPage, error) {
	path := "/api/v1/libraries/" + strconv.FormatInt(libraryID, 10) + "/books"
	endpoint := "POST " + path
	// Both collapse flags are off so one card is always one book. Oldest
	// first, so a book added while the walk is under way lands on the last
	// page instead of shifting every page after it.
	query := map[string]any{
		"collapseSeries": false,
		"collapseLinked": false,
		"sort":           []map[string]string{{"field": "addedAt", "dir": "asc"}},
		"pagination":     map[string]int{"page": page, "size": PageSize},
	}
	payload, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &ShapeError{Endpoint: endpoint, Detail: "not a JSON object"}
	}
	var total int64
	if err := requireInt(raw, "total", &total); err != nil {
		return nil, &ShapeError{Endpoint: endpoint, Detail: err.Error()}
	}
	itemsRaw, ok := raw["items"]
	if !ok {
		return nil, &ShapeError{Endpoint: endpoint, Detail: `field "items" is missing`}
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, &ShapeError{Endpoint: endpoint, Detail: `field "items" is not an array of objects`}
	}
	out := &BookPage{Total: int(total), IDs: make([]int64, 0, len(items))}
	for _, item := range items {
		var id int64
		if err := requireInt(item, "id", &id); err != nil {
			return nil, &ShapeError{Endpoint: endpoint, Detail: "items[]: " + err.Error()}
		}
		out.IDs = append(out.IDs, id)
	}
	return out, nil
}

// Book is the part of a BookOrbit book record the importer reads.
type Book struct {
	ID            int64
	LibraryID     int64
	Title         string
	Subtitle      string
	Description   string
	Publisher     string
	Language      string
	PublishedDate string
	PublishedYear int
	ISBN10        string
	ISBN13        string
	// ASIN is the Audible id. An Amazon only id is left out: it may be a Kindle one.
	ASIN            string
	Authors         []Person
	Series          []SeriesRef
	Genres          []string
	Narrators       []string
	DurationSeconds float64
	Files           []File
}

type Person struct {
	ID   int64
	Name string
}

type SeriesRef struct {
	ID   int64
	Name string
	// Index is the position as BookOrbit wrote it, so "4.5" stays "4.5".
	Index string
}

type File struct {
	ID int64
	// Path is the absolute path inside BookOrbit's container.
	Path      string
	Format    string
	Role      string
	SizeBytes int64
}

// GetBook reads one book in full, with its files.
func (c *Client) GetBook(ctx context.Context, id int64) (*Book, error) {
	path := "/api/v1/books/" + strconv.FormatInt(id, 10)
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	book, err := decodeBook(body)
	if err != nil {
		return nil, &ShapeError{Endpoint: "GET " + path, Detail: err.Error()}
	}
	return book, nil
}

func (c *Client) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set(apiKeyHeader, c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts-1 && isTimeout(err) {
				sleepBackoff(ctx, attempt)
				continue
			}
			return nil, err
		}
		if resp.StatusCode >= http.StatusInternalServerError && attempt < maxAttempts-1 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			lastErr = &APIError{StatusCode: resp.StatusCode}
			sleepBackoff(ctx, attempt)
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			apiErr := decodeAPIError(resp)
			_ = resp.Body.Close()
			return nil, apiErr
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseBytes)+1))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s %s: %w", method, path, err)
		}
		if len(body) > maxResponseBytes {
			return nil, fmt.Errorf("%s %s answered with more than %d bytes, refusing to read it", method, path, maxResponseBytes)
		}
		return body, nil
	}
	return nil, lastErr
}

func decodeAPIError(resp *http.Response) *APIError {
	out := &APIError{StatusCode: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var payload struct {
		ErrorCode string          `json:"errorCode"`
		Message   json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return out
	}
	out.Code = payload.ErrorCode
	// message is a string on most errors and a list of strings on a
	// validation error.
	var single string
	var many []string
	switch {
	case json.Unmarshal(payload.Message, &single) == nil:
		out.Message = single
	case json.Unmarshal(payload.Message, &many) == nil:
		out.Message = strings.Join(many, "; ")
	}
	if len(out.Message) > 300 {
		out.Message = out.Message[:300]
	}
	return out
}

func isTimeout(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func sleepBackoff(ctx context.Context, attempt int) {
	timer := time.NewTimer(150 * time.Millisecond << attempt)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
