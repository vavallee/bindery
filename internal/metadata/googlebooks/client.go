// Package googlebooks provides a read-only client for the Google Books API,
// used as a metadata enricher for author and book details.
package googlebooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const baseURL = "https://www.googleapis.com/books/v1"

// Client implements metadata.Provider for Google Books API.
// Used primarily for description enrichment — OL descriptions are often sparse.
type Client struct {
	http   *http.Client
	apiKey string // optional, increases quota from shared pool to 1000/day
	// gate holds every request this client makes while Google Books is
	// refusing. Nil (a zero value Client in tests) holds each call on its own.
	gate *providerhttp.Gate
	// liveKey, once SetAPIKey has run, replaces apiKey. It lets one client
	// (and so one refusal gate) live for the whole process while the key
	// changes under it from Settings (#1771).
	liveKey atomic.Pointer[string]
}

// New creates a Google Books client. apiKey can be empty for basic access.
func New(apiKey string) *Client {
	return &Client{
		http:   &http.Client{Timeout: 10 * time.Second, Transport: httpsec.MetadataTransport()},
		apiKey: apiKey,
		gate:   providerhttp.NewGate(),
	}
}

// SetAPIKey changes the key every later request sends. An empty key means
// basic, unkeyed access. Requests already built keep the key they read.
func (c *Client) SetAPIKey(apiKey string) {
	c.liveKey.Store(&apiKey)
}

// APIKeyConfigured reports whether requests currently carry a key.
func (c *Client) APIKeyConfigured() bool {
	return c.key() != ""
}

// key is the API key for the request being built.
func (c *Client) key() string {
	if k := c.liveKey.Load(); k != nil {
		return *k
	}
	return c.apiKey
}

func (c *Client) Name() string { return "googlebooks" }

func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	// Google Books doesn't have a dedicated author search; search by inauthor
	books, err := c.SearchBooks(ctx, "inauthor:"+query)
	if err != nil {
		return nil, err
	}

	// Deduplicate authors from results
	seen := make(map[string]bool)
	var authors []models.Author
	for _, b := range books {
		if b.Author != nil && !seen[b.Author.Name] {
			seen[b.Author.Name] = true
			authors = append(authors, *b.Author)
		}
	}
	return authors, nil
}

func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	u := fmt.Sprintf("%s/volumes?q=%s&maxResults=20", baseURL, url.QueryEscape(query))
	if key := c.key(); key != "" {
		u += "&key=" + url.QueryEscape(key)
	}

	var resp volumeSearchResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search books: %w", err)
	}

	books := make([]models.Book, 0, len(resp.Items))
	for _, item := range resp.Items {
		b := c.volumeToBook(item)
		books = append(books, b)
	}
	return books, nil
}

func (c *Client) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	// Google Books doesn't support author lookup by ID
	return nil, fmt.Errorf("google books does not support author lookup by ID")
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	// SearchBooks/volumeToBook stamp ForeignID as "gb:<volumeID>"; strip the
	// prefix before building the volumes URL or the request 404s.
	id := strings.TrimPrefix(foreignID, "gb:")
	u := fmt.Sprintf("%s/volumes/%s", baseURL, id)
	if key := c.key(); key != "" {
		u += "?key=" + url.QueryEscape(key)
	}

	var item volumeItem
	if err := c.getJSON(ctx, u, &item); err != nil {
		return nil, fmt.Errorf("get book %s: %w", foreignID, err)
	}

	b := c.volumeToBook(item)
	return &b, nil
}

func (c *Client) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	// Google Books doesn't expose edition lists per work
	return nil, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	books, err := c.SearchBooks(ctx, "isbn:"+isbn)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (c *Client) volumeToBook(item volumeItem) models.Book {
	vi := item.VolumeInfo
	b := models.Book{
		ForeignID:        "gb:" + item.ID,
		Title:            vi.Title,
		SortTitle:        vi.Title,
		Description:      vi.Description,
		Genres:           vi.Categories,
		AverageRating:    vi.AverageRating,
		RatingsCount:     vi.RatingsCount,
		Language:         models.NormalizeLanguageCode(vi.Language),
		MetadataProvider: "googlebooks",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}
	if b.Genres == nil {
		b.Genres = []string{}
	}
	for _, identifier := range vi.IndustryIdentifiers {
		if identifier.Type == "ISBN_10" || identifier.Type == "ISBN_13" {
			b.ProviderISBNs = append(b.ProviderISBNs, identifier.Identifier)
		}
	}
	if vi.ImageLinks != nil && vi.ImageLinks.Thumbnail != "" {
		b.ImageURL = strings.Replace(vi.ImageLinks.Thumbnail, "http://", "https://", 1)
	}
	if len(vi.Authors) > 0 {
		b.Author = &models.Author{
			Name:             vi.Authors[0],
			SortName:         sortName(vi.Authors[0]),
			MetadataProvider: "googlebooks",
		}
	}
	return b
}

// getJSON goes through the request loop every HTTP metadata provider shares
// (package providerhttp), so a 429 is retried with Retry-After and backoff
// instead of failing the lookup outright (#2369), and a refusal holds every
// request this client makes rather than letting the others keep spending it.
//
// A quota that has run out for the day is the exception. Google Books reports
// it as a 403 or 429 whose reason is dailyLimitExceeded or quotaExceeded, or
// whose message names the "Queries per day" limit under any reason. Retrying
// it only spends more requests on an answer that will not change until
// tomorrow, so it comes back at once, marked as a rate limit.
//
// Redact is set because the API key rides in the query string: a transport
// error wraps a *url.Error whose message embeds the full URL, so it is
// flattened to its redacted text before anything can log it (#1144).
func (c *Client) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		URL:    rawURL,
		Redact: true,
		Final:  isQuotaExhausted,
	})
	if err != nil {
		var status *providerhttp.StatusError
		if errors.As(err, &status) && status.Final {
			return &quotaExhaustedError{err: status}
		}
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(target)
}

// quotaReasonRe matches the reason Google's API error envelope gives for a
// quota that has run out for the day, as opposed to a per minute throttle
// ("rateLimitExceeded", "userRateLimitExceeded"), which is worth retrying.
var quotaReasonRe = regexp.MustCompile(`"reason"\s*:\s*"(dailyLimitExceeded|quotaExceeded)"`)

// quotaPerDayRe matches the human message Google gives when the daily limit is
// the one that ran out: "... limit 'Queries per day' of service ...". Some
// refusals of the same condition carry reason "rateLimitExceeded", the reason
// of a per minute throttle (#3451), so the reason alone cannot tell them
// apart. The per minute message names "Queries per minute per user" instead.
// The apostrophes around the limit are left out of the pattern because JSON
// may spell them \u0027.
var quotaPerDayRe = regexp.MustCompile(`(?i)queries per day`)

// isQuotaExhausted reports whether a 403 or 429 is the daily quota refusal:
// by its reason, or by a message that names the per day limit.
func isQuotaExhausted(code int, body []byte) bool {
	if code != http.StatusForbidden && code != http.StatusTooManyRequests {
		return false
	}
	return quotaReasonRe.Match(body) || quotaPerDayRe.Match(body)
}

// quotaExhaustedError marks a daily quota refusal as a rate limit whatever
// its status, so scheduled work stops asking instead of walking its queue.
type quotaExhaustedError struct{ err error }

func (e *quotaExhaustedError) Error() string {
	return "google books daily quota exhausted: " + e.err.Error()
}
func (e *quotaExhaustedError) Unwrap() error        { return e.err }
func (e *quotaExhaustedError) Is(target error) bool { return target == providererr.ErrRateLimited }

// sortName delegates to textutil.SortName, the same way the openlibrary and
// hardcover clients do, so all three providers stamp the same sort form on an
// author (#2363).
func sortName(name string) string {
	return textutil.SortName(name)
}
