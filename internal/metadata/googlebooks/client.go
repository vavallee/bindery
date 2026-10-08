// Package googlebooks provides a read-only client for the Google Books API,
// used as a metadata enricher for author and book details.
package googlebooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
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
}

// New creates a Google Books client. apiKey can be empty for basic access.
func New(apiKey string) *Client {
	return &Client{
		http:   &http.Client{Timeout: 10 * time.Second, Transport: httpsec.DefaultProxyTransport()},
		apiKey: apiKey,
		gate:   providerhttp.NewGate(),
	}
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
	if c.apiKey != "" {
		u += "&key=" + url.QueryEscape(c.apiKey)
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
	if c.apiKey != "" {
		u += "?key=" + url.QueryEscape(c.apiKey)
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
		Language:         vi.Language,
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
// instead of failing the lookup outright (#2369). Google Books answers 429
// when the key's quota runs out, and a refusal holds every request this
// client makes rather than letting the others keep spending it.
//
// Redact is set because the API key rides in the query string: a transport
// error wraps a *url.Error whose message embeds the full URL, so it is
// flattened to its redacted text before anything can log it (#1144).
func (c *Client) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		URL:    rawURL,
		Redact: true,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(target)
}

// sortName delegates to textutil.SortName, the same way the openlibrary and
// hardcover clients do, so all three providers stamp the same sort form on an
// author (#2363).
func sortName(name string) string {
	return textutil.SortName(name)
}
