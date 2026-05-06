// Package hardcover provides a read-only GraphQL client for hardcover.app,
// used as a metadata enricher for community ratings and series data.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const (
	graphqlURL = "https://api.hardcover.app/v1/graphql"
	idPrefix   = "hc:"

	authorWorksPageSize = 100
	authorWorksMaxBooks = 500

	hardcoverSuccessResponseBodyLimit = 8 << 20
)

// Client implements metadata.Provider for Hardcover.app using its public GraphQL API.
// Set an API token via WithToken or NewAuthenticated to enable authenticated queries.
type Client struct {
	http        *http.Client
	token       string // optional API token; required for user-specific queries
	tokenSource func(context.Context) string
}

// NormalizeAPIToken accepts either the raw token copied from Hardcover or an
// Authorization-style value such as "Bearer <token>" and returns the raw token.
func NormalizeAPIToken(value string) string {
	token := strings.TrimSpace(value)
	for {
		token = strings.Trim(strings.TrimSpace(token), `"'`+"`")
		lower := strings.ToLower(token)
		switch {
		case strings.HasPrefix(lower, "authorization:"):
			token = strings.TrimSpace(token[len("authorization:"):])
			continue
		case strings.HasPrefix(lower, "authorization="):
			token = strings.TrimSpace(token[len("authorization="):])
			continue
		}
		if strings.EqualFold(token, "Bearer") {
			return ""
		}
		fields := strings.Fields(token)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Bearer") {
			break
		}
		token = strings.TrimSpace(token[len(fields[0]):])
	}
	return token
}

// New creates a new Hardcover client.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// WithToken returns a copy of the client configured to use the given API token.
// Required for authenticated queries such as GetUserWishlist.
func (c *Client) WithToken(token string) *Client {
	return &Client{http: c.http, token: token}
}

// WithTokenSource returns a copy of the client that resolves an API token
// for each request. It is used for UI-managed credentials that can change
// while the process is running.
func (c *Client) WithTokenSource(source func(context.Context) string) *Client {
	return &Client{http: c.http, token: c.token, tokenSource: source}
}

// NewAuthenticated creates a new client that sends Authorization: Bearer <token>
// for authenticated queries (e.g. reading user lists).
func NewAuthenticated(token string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 15 * time.Second},
		token: token,
	}
}

func (c *Client) Name() string { return "hardcover" }

func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	gql := `query SearchAuthors($query: String!) {
		authors(where: {name: {_ilike: $query}}, limit: 20) {
			id
			name
			slug
			bio
			image { url }
		}
	}`
	var resp struct {
		Data struct {
			Authors []hcAuthor `json:"authors"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"query": "%" + query + "%"}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover search authors: %w", err)
	}
	authors := make([]models.Author, 0, len(resp.Data.Authors))
	for _, a := range resp.Data.Authors {
		authors = append(authors, c.toAuthor(a))
	}
	return authors, nil
}

func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	gql := `query SearchBooks($query: String!) {
		books(where: {title: {_ilike: $query}}, limit: 20) {
			id
			title
			slug
			description
			image { url }
			release_year
			ratings_count
			rating
			contributions {
				author { id name slug }
			}
		}
	}`
	var resp struct {
		Data struct {
			Books []hcBook `json:"books"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"query": "%" + query + "%"}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover search books: %w", err)
	}
	books := make([]models.Book, 0, len(resp.Data.Books))
	for _, b := range resp.Data.Books {
		books = append(books, c.toBook(b))
	}
	return books, nil
}

// GetAuthorWorksByName fetches canonical Hardcover books for an author in
// page-sized batches. It requires a configured API token because Hardcover's
// schema endpoints are token-backed in production; an unconfigured client
// returns no supplemental results.
func (c *Client) GetAuthorWorksByName(ctx context.Context, authorName string) ([]models.Book, error) {
	authorName = strings.TrimSpace(authorName)
	if authorName == "" {
		return nil, nil
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}

	gql := `query GetAuthorWorksByName($author: String!, $limit: Int!, $offset: Int!) {
		books(
			where: {
				canonical_id: {_is_null: true},
				contributions: {author: {name: {_eq: $author}}}
			},
			limit: $limit,
			offset: $offset,
			order_by: {users_count: desc}
		) {
			id
			title
			subtitle
			slug
			description
			image { url }
			release_year
			ratings_count
			rating
			users_count
			genres
			has_audiobook
			has_ebook
			audio_seconds
			contributions {
				author { id name slug }
			}
		}
	}`

	books := make([]models.Book, 0, authorWorksPageSize)
	for offset := 0; offset < authorWorksMaxBooks; offset += authorWorksPageSize {
		var resp struct {
			Data struct {
				Books []hcBook `json:"books"`
			} `json:"data"`
		}
		if err := c.query(ctx, gql, map[string]any{
			"author": authorName,
			"limit":  authorWorksPageSize,
			"offset": offset,
		}, &resp); err != nil {
			return nil, fmt.Errorf("hardcover get author works: %w", err)
		}
		for _, b := range resp.Data.Books {
			books = append(books, c.toBook(b))
		}
		if len(resp.Data.Books) < authorWorksPageSize {
			break
		}
	}
	return books, nil
}

func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	slug := strings.TrimPrefix(foreignID, idPrefix)
	gql := `query GetAuthor($slug: String!) {
		authors(where: {slug: {_eq: $slug}}, limit: 1) {
			id
			name
			slug
			bio
			image { url }
		}
	}`
	var resp struct {
		Data struct {
			Authors []hcAuthor `json:"authors"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"slug": slug}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get author: %w", err)
	}
	if len(resp.Data.Authors) == 0 {
		return nil, nil
	}
	a := c.toAuthor(resp.Data.Authors[0])
	return &a, nil
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	slug := strings.TrimPrefix(foreignID, idPrefix)
	gql := `query GetBook($slug: String!) {
		books(where: {slug: {_eq: $slug}}, limit: 1) {
			id
			title
			slug
			description
			image { url }
			release_year
			ratings_count
			rating
			contributions {
				author { id name slug }
			}
		}
	}`
	var resp struct {
		Data struct {
			Books []hcBook `json:"books"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"slug": slug}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get book: %w", err)
	}
	if len(resp.Data.Books) == 0 {
		return nil, nil
	}
	b := c.toBook(resp.Data.Books[0])
	return &b, nil
}

// GetEditions is not supported by Hardcover.
func (c *Client) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return nil, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	gql := `query GetBookByISBN($isbn: String!) {
		editions(where: {_or: [{isbn_10: {_eq: $isbn}}, {isbn_13: {_eq: $isbn}}]}, limit: 1) {
			language { iso_639_1 }
			book {
				id
				title
				slug
				description
				image { url }
				release_year
				ratings_count
				rating
				contributions {
					author { id name slug }
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			Editions []struct {
				Language *hcLanguage `json:"language"`
				Book     hcBook      `json:"book"`
			} `json:"editions"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"isbn": isbn}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get book by isbn: %w", err)
	}
	if len(resp.Data.Editions) == 0 {
		return nil, nil
	}
	ed := resp.Data.Editions[0]
	b := c.toBook(ed.Book)
	if ed.Language != nil && ed.Language.ISO6391 != "" {
		b.Language = ed.Language.ISO6391
	}
	return &b, nil
}

// GetUserWishlist fetches the authenticated user's "Want to Read" books.
// Returns candidates suitable for list-cross recommendations.
// Requires the client to have an API token set via WithToken; returns nil if not configured.
func (c *Client) GetUserWishlist(ctx context.Context, limit int) ([]models.RecommendationCandidate, error) {
	if c.token == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	// status_id 1 = "Want to Read" in Hardcover's reading status enum.
	gql := `query GetWishlist($limit: Int!) {
		me {
			user_books(where: {status_id: {_eq: 1}}, limit: $limit) {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			Me struct {
				UserBooks []struct {
					Book hcBook `json:"book"`
				} `json:"user_books"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"limit": limit}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get wishlist: %w", err)
	}

	candidates := make([]models.RecommendationCandidate, 0, len(resp.Data.Me.UserBooks))
	for _, ub := range resp.Data.Me.UserBooks {
		b := c.toBook(ub.Book)
		cand := models.RecommendationCandidate{
			ForeignID:    b.ForeignID,
			Title:        b.Title,
			ImageURL:     b.ImageURL,
			Description:  b.Description,
			Rating:       b.AverageRating,
			RatingsCount: b.RatingsCount,
			ReleaseDate:  b.ReleaseDate,
			MediaType:    models.MediaTypeEbook,
			Genres:       []string{},
		}
		if b.Author != nil {
			cand.AuthorName = b.Author.Name
		}
		candidates = append(candidates, cand)
	}
	return candidates, nil
}

// --- Authenticated list queries ---

// HCList represents a Hardcover reading list or built-in shelf.
// Built-in shelves use negative IDs: -1 Want to Read, -2 Currently Reading,
// -3 Read, -4 Did Not Finish.
type HCList struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	BooksCount int    `json:"booksCount"`
}

// hcBuiltinShelves are the four standard Hardcover reading-status shelves.
// They live in user_books (filtered by status_id), not in me.lists, so they
// are injected as synthetic entries using negative IDs to avoid collision with
// real list IDs.
var hcBuiltinShelves = []HCList{
	{ID: -1, Name: "Want to Read", Slug: "want-to-read"},
	{ID: -2, Name: "Currently Reading", Slug: "currently-reading"},
	{ID: -3, Name: "Read", Slug: "read"},
	{ID: -4, Name: "Did Not Finish", Slug: "did-not-finish"},
}

// hcShelfStatusID maps a synthetic shelf list ID to its Hardcover status_id.
func hcShelfStatusID(listID int) (int, bool) {
	switch listID {
	case -1:
		return 1, true
	case -2:
		return 2, true
	case -3:
		return 3, true
	case -4:
		return 4, true
	}
	return 0, false
}

// GetUserLists returns the authenticated user's reading lists, prepended by
// the four built-in Hardcover shelves (Want to Read, Currently Reading, Read,
// Did Not Finish). Built-in shelves always appear even when the user has no
// custom lists, which was the root cause of the "No lists found" report.
func (c *Client) GetUserLists(ctx context.Context) ([]HCList, error) {
	gql := `query GetUserLists {
		me {
			lists {
				id
				name
				slug
				books_count
			}
		}
	}`
	var resp struct {
		Data struct {
			Me struct {
				Lists []struct {
					ID         int    `json:"id"`
					Name       string `json:"name"`
					Slug       string `json:"slug"`
					BooksCount int    `json:"books_count"`
				} `json:"lists"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, nil, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get user lists: %w", err)
	}
	lists := make([]HCList, 0, len(hcBuiltinShelves)+len(resp.Data.Me.Lists))
	lists = append(lists, hcBuiltinShelves...)
	for _, l := range resp.Data.Me.Lists {
		lists = append(lists, HCList{
			ID:         l.ID,
			Name:       l.Name,
			Slug:       l.Slug,
			BooksCount: l.BooksCount,
		})
	}
	return lists, nil
}

// GetListBooks returns all books in the given list as Bindery models.
// Negative listIDs refer to built-in Hardcover shelves (see hcBuiltinShelves).
func (c *Client) GetListBooks(ctx context.Context, listID int) ([]models.Book, error) {
	if statusID, ok := hcShelfStatusID(listID); ok {
		return c.getShelfBooks(ctx, statusID)
	}
	gql := `query GetListBooks($id: Int!) {
		list(id: $id) {
			id
			name
			slug
			list_books {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			List struct {
				ListBooks []struct {
					Book hcBook `json:"book"`
				} `json:"list_books"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"id": listID}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get list books: %w", err)
	}
	books := make([]models.Book, 0, len(resp.Data.List.ListBooks))
	for _, lb := range resp.Data.List.ListBooks {
		books = append(books, c.toBook(lb.Book))
	}
	return books, nil
}

// getShelfBooks fetches all books on a built-in Hardcover shelf by status_id.
func (c *Client) getShelfBooks(ctx context.Context, statusID int) ([]models.Book, error) {
	gql := `query GetShelfBooks($statusID: Int!) {
		me {
			user_books(where: {status_id: {_eq: $statusID}}, limit: 500) {
				book {
					id
					title
					slug
					description
					image { url }
					release_year
					ratings_count
					rating
					contributions {
						author { id name slug }
					}
				}
			}
		}
	}`
	var resp struct {
		Data struct {
			Me struct {
				UserBooks []struct {
					Book hcBook `json:"book"`
				} `json:"user_books"`
			} `json:"me"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{"statusID": statusID}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get shelf books: %w", err)
	}
	books := make([]models.Book, 0, len(resp.Data.Me.UserBooks))
	for _, ub := range resp.Data.Me.UserBooks {
		books = append(books, c.toBook(ub.Book))
	}
	return books, nil
}

// --- GraphQL transport ---

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func (c *Client) query(ctx context.Context, q string, vars map[string]any, out interface{}) error {
	body, err := json.Marshal(gqlRequest{Query: q, Variables: vars})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bindery/0.1 (https://github.com/vavallee/bindery)")
	if token := c.authorizationToken(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, hardcoverSuccessResponseBodyLimit))
	if err != nil {
		return err
	}
	var envelope struct {
		Errors []gqlError `json:"errors"`
	}
	if err := json.Unmarshal(b, &envelope); err == nil && len(envelope.Errors) > 0 {
		return fmt.Errorf("GraphQL: %s", formatGraphQLErrors(envelope.Errors))
	}
	return json.Unmarshal(b, out)
}

func (c *Client) authorizationToken(ctx context.Context) string {
	if c.tokenSource != nil {
		if token := NormalizeAPIToken(c.tokenSource(ctx)); token != "" {
			return token
		}
	}
	return NormalizeAPIToken(c.token)
}

func formatGraphQLErrors(errors []gqlError) string {
	if len(errors) == 0 {
		return "unknown error"
	}
	parts := make([]string, 0, min(len(errors), 3))
	for _, gqlErr := range errors {
		msg := strings.TrimSpace(gqlErr.Message)
		if msg == "" {
			msg = "unknown error"
		}
		if code, ok := gqlErr.Extensions["code"].(string); ok && code != "" {
			msg += " (" + code + ")"
		}
		parts = append(parts, msg)
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

// --- Internal types for JSON mapping ---

type hcImage struct {
	URL string `json:"url"`
}

type hcLanguage struct {
	ISO6391 string `json:"iso_639_1"`
}

type hcAuthor struct {
	ID    int      `json:"id"`
	Name  string   `json:"name"`
	Slug  string   `json:"slug"`
	Bio   string   `json:"bio"`
	Image *hcImage `json:"image"`
}

type hcContribution struct {
	Author hcAuthor `json:"author"`
}

type hcBook struct {
	ID            int              `json:"id"`
	Title         string           `json:"title"`
	Subtitle      string           `json:"subtitle"`
	Slug          string           `json:"slug"`
	Description   string           `json:"description"`
	Image         *hcImage         `json:"image"`
	ReleaseYear   *int             `json:"release_year"`
	RatingsCount  int              `json:"ratings_count"`
	Rating        float64          `json:"rating"`
	UsersCount    int              `json:"users_count"`
	Genres        []string         `json:"genres"`
	HasAudiobook  bool             `json:"has_audiobook"`
	HasEbook      bool             `json:"has_ebook"`
	AudioSeconds  *int             `json:"audio_seconds"`
	Contributions []hcContribution `json:"contributions"`
}

// --- Converters ---

func (c *Client) toAuthor(a hcAuthor) models.Author {
	slug := a.Slug
	if slug == "" {
		slug = fmt.Sprintf("%d", a.ID)
	}
	au := models.Author{
		ForeignID:        idPrefix + slug,
		Name:             a.Name,
		SortName:         sortName(a.Name),
		Description:      a.Bio,
		MetadataProvider: "hardcover",
	}
	if a.Image != nil {
		au.ImageURL = a.Image.URL
	}
	return au
}

func (c *Client) toBook(b hcBook) models.Book {
	slug := b.Slug
	if slug == "" {
		slug = fmt.Sprintf("%d", b.ID)
	}
	bk := models.Book{
		ForeignID:        idPrefix + slug,
		Title:            b.Title,
		SortTitle:        b.Title,
		Description:      b.Description,
		AverageRating:    b.Rating,
		RatingsCount:     b.RatingsCount,
		MetadataProvider: "hardcover",
		Monitored:        true,
		Status:           models.BookStatusWanted,
		Genres:           []string{},
	}
	if len(b.Genres) > 0 {
		bk.Genres = b.Genres
	}
	if b.Image != nil {
		bk.ImageURL = b.Image.URL
	}
	if b.ReleaseYear != nil && *b.ReleaseYear > 0 {
		t := time.Date(*b.ReleaseYear, 1, 1, 0, 0, 0, 0, time.UTC)
		bk.ReleaseDate = &t
	}
	if b.AudioSeconds != nil && *b.AudioSeconds > 0 {
		bk.DurationSeconds = *b.AudioSeconds
	}
	if len(b.Contributions) > 0 {
		a := c.toAuthor(b.Contributions[0].Author)
		bk.Author = &a
	}
	return bk
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
