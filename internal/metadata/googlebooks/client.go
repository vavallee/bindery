// Package googlebooks provides a read-only client for the Google Books API,
// used as a metadata enricher for author and book details.
package googlebooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

const baseURL = "https://www.googleapis.com/books/v1"

// Client implements metadata.Provider for Google Books API.
// Used primarily for description enrichment — OL descriptions are often sparse.
type Client struct {
	http   *http.Client
	apiKey string // optional, increases quota from shared pool to 1000/day
}

// New creates a Google Books client. apiKey can be empty for basic access.
func New(apiKey string) *Client {
	return &Client{
		http:   &http.Client{Timeout: 10 * time.Second},
		apiKey: apiKey,
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
	foreignID = strings.TrimPrefix(foreignID, "gb:")
	u := fmt.Sprintf("%s/volumes/%s", baseURL, foreignID)
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
	isbn = isbnutil.Normalize(isbn)
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

func (c *Client) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func sortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}
