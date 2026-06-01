// Package hardcover provides a read-only GraphQL client for hardcover.app,
// used as a metadata enricher for community ratings and series data.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	graphqlURL = "https://api.hardcover.app/v1/graphql"
	idPrefix   = "hc:"

	authorWorksPageSize = 100
	authorWorksMaxBooks = 500
	editionsPageSize    = 100
	editionsMaxCount    = 1000

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
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	gql := `query SearchAuthors($query: String!, $queryType: String!, $perPage: Int!) {
		search(query: $query, query_type: $queryType, per_page: $perPage) {
			results
		}
	}`
	var resp struct {
		Data struct {
			Search struct {
				Results json.RawMessage `json:"results"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{
		"query":     query,
		"queryType": "Author",
		"perPage":   20,
	}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover search authors: %w", err)
	}
	docs := parseAuthorSearchResults(resp.Data.Search.Results)
	authors := make([]models.Author, 0, len(docs))
	for _, a := range docs {
		authors = append(authors, c.toAuthor(a))
	}
	return authors, nil
}

func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	gql := `query SearchBooks($query: String!, $queryType: String!, $perPage: Int!) {
		search(query: $query, query_type: $queryType, per_page: $perPage) {
			results
		}
	}`
	var resp struct {
		Data struct {
			Search struct {
				Results json.RawMessage `json:"results"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{
		"query":     query,
		"queryType": "Book",
		"perPage":   20,
	}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover search books: %w", err)
	}
	docs := parseBookSearchResults(resp.Data.Search.Results)
	books := make([]models.Book, 0, len(docs))
	for _, b := range docs {
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
			audio_seconds
			default_audio_edition_id
			default_ebook_edition_id
			language { language }
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
	id := strings.TrimPrefix(foreignID, idPrefix)
	gql := `query GetAuthor($slug: String!) {
		authors(where: {slug: {_eq: $slug}}, limit: 1) {
			id
			name
			slug
			bio
			image { url }
		}
	}`
	vars := map[string]any{"slug": id}
	if numericID, ok := hardcoverNumericID(id); ok {
		gql = `query GetAuthor($id: Int!) {
			authors(where: {id: {_eq: $id}}, limit: 1) {
				id
				name
				slug
				bio
				image { url }
			}
		}`
		vars = map[string]any{"id": numericID}
	}
	var resp struct {
		Data struct {
			Authors []hcAuthor `json:"authors"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, vars, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get author: %w", err)
	}
	if len(resp.Data.Authors) == 0 {
		return nil, nil
	}
	a := c.toAuthor(resp.Data.Authors[0])
	return &a, nil
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	id := strings.TrimPrefix(foreignID, idPrefix)
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
			default_audio_edition_id
			default_ebook_edition_id
			contributions {
				author { id name slug }
			}
		}
	}`
	vars := map[string]any{"slug": id}
	if numericID, ok := hardcoverNumericID(id); ok {
		gql = `query GetBook($id: Int!) {
			books(where: {id: {_eq: $id}}, limit: 1) {
				id
				title
				slug
				description
				image { url }
				release_year
				ratings_count
				rating
				default_audio_edition_id
				default_ebook_edition_id
				contributions {
					author { id name slug }
				}
			}
		}`
		vars = map[string]any{"id": numericID}
	}
	var resp struct {
		Data struct {
			Books []hcBook `json:"books"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, vars, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get book: %w", err)
	}
	if len(resp.Data.Books) == 0 {
		return nil, nil
	}
	b := c.toBook(resp.Data.Books[0])
	return &b, nil
}

func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	id := strings.TrimSpace(strings.TrimPrefix(bookForeignID, idPrefix))
	if id == "" {
		return nil, nil
	}

	gql := `query GetEditions($slug: String!, $limit: Int!, $offset: Int!) {
		editions(
			where: {book: {slug: {_eq: $slug}}},
			limit: $limit,
			offset: $offset,
			order_by: {id: asc}
		) {
			id
			title
			isbn_10
			isbn_13
			asin
			publisher { name }
			release_date
			release_year
			physical_format
			edition_format
			edition_information
			pages
			image { url }
			language { language }
			reading_format { format }
			audio_seconds
			book { title }
		}
	}`
	vars := map[string]any{"slug": id}
	if numericID, ok := hardcoverNumericID(id); ok {
		gql = `query GetEditions($bookID: Int!, $limit: Int!, $offset: Int!) {
		editions(
			where: {book_id: {_eq: $bookID}},
			limit: $limit,
			offset: $offset,
			order_by: {id: asc}
		) {
			id
			title
			isbn_10
			isbn_13
			asin
			publisher { name }
			release_date
			release_year
			physical_format
			edition_format
			edition_information
			pages
			image { url }
			language { language }
			reading_format { format }
			audio_seconds
			book { title }
		}
	}`
		vars = map[string]any{"bookID": numericID}
	}

	editions := make([]models.Edition, 0, editionsPageSize)
	for offset := 0; offset < editionsMaxCount; offset += editionsPageSize {
		vars["limit"] = editionsPageSize
		vars["offset"] = offset
		var resp struct {
			Data struct {
				Editions []hcEdition `json:"editions"`
			} `json:"data"`
		}
		if err := c.query(ctx, gql, vars, &resp); err != nil {
			return nil, fmt.Errorf("hardcover get editions: %w", err)
		}
		for _, e := range resp.Data.Editions {
			editions = append(editions, hardcoverEditionToModel(e))
		}
		if len(resp.Data.Editions) < editionsPageSize {
			break
		}
	}
	return editions, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	gql := `query GetBookByISBN($isbn: String!) {
		editions(where: {_or: [{isbn_10: {_eq: $isbn}}, {isbn_13: {_eq: $isbn}}]}, limit: 1) {
			language { language }
			book {
				id
				title
				slug
				description
				image { url }
				release_year
				ratings_count
				rating
				default_audio_edition_id
				default_ebook_edition_id
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
	if language := hardcoverLanguageName(ed.Language); language != "" {
		b.Language = language
	}
	return &b, nil
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
	req.Header.Set("User-Agent", useragent.Get())
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
	Language string `json:"language"`
}

type hcPublisher struct {
	Name string `json:"name"`
}

type hcReadingFormat struct {
	Format string `json:"format"`
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
	ID                     int                `json:"id"`
	Title                  string             `json:"title"`
	Subtitle               string             `json:"subtitle"`
	Slug                   string             `json:"slug"`
	Description            string             `json:"description"`
	Image                  *hcImage           `json:"image"`
	ReleaseYear            *int               `json:"release_year"`
	RatingsCount           int                `json:"ratings_count"`
	Rating                 float64            `json:"rating"`
	UsersCount             int                `json:"users_count"`
	Genres                 []string           `json:"genres"`
	ISBNs                  []string           `json:"isbns"`
	HasAudiobook           bool               `json:"has_audiobook"`
	HasEbook               bool               `json:"has_ebook"`
	AudioSeconds           *int               `json:"audio_seconds"`
	DefaultAudioEditionID  *int               `json:"default_audio_edition_id"`
	DefaultEbookEditionID  *int               `json:"default_ebook_edition_id"`
	Language               *hcLanguage        `json:"language"`
	Contributions          []hcContribution   `json:"contributions"`
	AuthorNames            []string           `json:"author_names"`
	FeaturedSeries         *hcFeaturedSeries  `json:"featured_series"`
	FeaturedSeriesID       *int               `json:"featured_series_id"`
	FeaturedSeriesPosition any                `json:"featured_series_position"`
	SeriesRefs             []models.SeriesRef `json:"-"`
}

// hcFeaturedSeries captures the Hardcover GraphQL `featured_series` relation
// on a book — the primary series the book belongs to. Used to hydrate
// SeriesRefs for list/shelf books, which would otherwise lose their series
// association at import time.
type hcFeaturedSeries struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type hcEdition struct {
	ID                 int              `json:"id"`
	Title              string           `json:"title"`
	ISBN10             string           `json:"isbn_10"`
	ISBN13             string           `json:"isbn_13"`
	ASIN               string           `json:"asin"`
	Publisher          *hcPublisher     `json:"publisher"`
	ReleaseDate        string           `json:"release_date"`
	ReleaseYear        *int             `json:"release_year"`
	PhysicalFormat     string           `json:"physical_format"`
	EditionFormat      string           `json:"edition_format"`
	EditionInformation string           `json:"edition_information"`
	Pages              *int             `json:"pages"`
	Image              *hcImage         `json:"image"`
	Language           *hcLanguage      `json:"language"`
	ReadingFormat      *hcReadingFormat `json:"reading_format"`
	AudioSeconds       *int             `json:"audio_seconds"`
	Book               *struct {
		Title string `json:"title"`
	} `json:"book"`
}

type hcAuthorSearchEnvelope struct {
	Hits []hcAuthorSearchHit `json:"hits"`
}

type hcAuthorSearchHit struct {
	Document hcAuthorSearchDocument `json:"document"`
}

type hcAuthorSearchDocument struct {
	ID          any    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Bio         string `json:"bio"`
	Description string `json:"description"`
	Image       any    `json:"image"`
	ImageURL    string `json:"image_url"`
	CachedImage any    `json:"cached_image"`
}

type hcBookSearchEnvelope struct {
	Hits []hcBookSearchHit `json:"hits"`
}

type hcBookSearchHit struct {
	Document hcBookSearchDocument `json:"document"`
}

type hcBookSearchDocument struct {
	ID                     any                    `json:"id"`
	Title                  string                 `json:"title"`
	Slug                   string                 `json:"slug"`
	Description            string                 `json:"description"`
	Image                  any                    `json:"image"`
	ImageURL               string                 `json:"image_url"`
	CachedImage            any                    `json:"cached_image"`
	ReleaseYear            any                    `json:"release_year"`
	RatingsCount           any                    `json:"ratings_count"`
	Rating                 any                    `json:"rating"`
	ISBNs                  any                    `json:"isbns"`
	Genres                 any                    `json:"genres"`
	HasAudiobook           any                    `json:"has_audiobook"`
	HasEbook               any                    `json:"has_ebook"`
	FeaturedSeries         any                    `json:"featured_series"`
	FeaturedSeriesID       any                    `json:"featured_series_id"`
	FeaturedSeriesPosition any                    `json:"featured_series_position"`
	Contributions          []hcSearchContribution `json:"contributions"`
	AuthorNames            []string               `json:"author_names"`
}

type hcSearchContribution struct {
	Author hcAuthorSearchDocument `json:"author"`
}

func parseAuthorSearchResults(raw json.RawMessage) []hcAuthor {
	raw = normalizeRawSearchResults(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var envelope hcAuthorSearchEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Hits) > 0 {
		return authorSearchHitsToAuthors(envelope.Hits)
	}
	var hits []hcAuthorSearchHit
	if err := json.Unmarshal(raw, &hits); err == nil {
		return authorSearchHitsToAuthors(hits)
	}
	var docs []hcAuthorSearchDocument
	if err := json.Unmarshal(raw, &docs); err == nil {
		authors := make([]hcAuthor, 0, len(docs))
		for _, doc := range docs {
			if author, ok := authorSearchDocumentToAuthor(doc); ok {
				authors = append(authors, author)
			}
		}
		return authors
	}
	return nil
}

func parseBookSearchResults(raw json.RawMessage) []hcBook {
	raw = normalizeRawSearchResults(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var envelope hcBookSearchEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Hits) > 0 {
		return bookSearchHitsToBooks(envelope.Hits)
	}
	var hits []hcBookSearchHit
	if err := json.Unmarshal(raw, &hits); err == nil {
		return bookSearchHitsToBooks(hits)
	}
	var docs []hcBookSearchDocument
	if err := json.Unmarshal(raw, &docs); err == nil {
		books := make([]hcBook, 0, len(docs))
		for _, doc := range docs {
			if book, ok := bookSearchDocumentToBook(doc); ok {
				books = append(books, book)
			}
		}
		return books
	}
	return nil
}

func authorSearchHitsToAuthors(hits []hcAuthorSearchHit) []hcAuthor {
	authors := make([]hcAuthor, 0, len(hits))
	for _, hit := range hits {
		if author, ok := authorSearchDocumentToAuthor(hit.Document); ok {
			authors = append(authors, author)
		}
	}
	return authors
}

func bookSearchHitsToBooks(hits []hcBookSearchHit) []hcBook {
	books := make([]hcBook, 0, len(hits))
	for _, hit := range hits {
		if book, ok := bookSearchDocumentToBook(hit.Document); ok {
			books = append(books, book)
		}
	}
	return books
}

func authorSearchDocumentToAuthor(doc hcAuthorSearchDocument) (hcAuthor, bool) {
	name := strings.TrimSpace(doc.Name)
	id, _ := searchInt(doc.ID)
	slug := strings.TrimSpace(doc.Slug)
	if name == "" || (slug == "" && id <= 0) {
		return hcAuthor{}, false
	}
	bio := strings.TrimSpace(doc.Bio)
	if bio == "" {
		bio = strings.TrimSpace(doc.Description)
	}
	return hcAuthor{
		ID:    id,
		Name:  name,
		Slug:  slug,
		Bio:   bio,
		Image: searchImage(doc.Image, doc.ImageURL, doc.CachedImage),
	}, true
}

func bookSearchDocumentToBook(doc hcBookSearchDocument) (hcBook, bool) {
	title := strings.TrimSpace(doc.Title)
	id, _ := searchInt(doc.ID)
	slug := strings.TrimSpace(doc.Slug)
	if title == "" || (slug == "" && id <= 0) {
		return hcBook{}, false
	}
	book := hcBook{
		ID:            id,
		Title:         title,
		Slug:          slug,
		Description:   strings.TrimSpace(doc.Description),
		Image:         searchImage(doc.Image, doc.ImageURL, doc.CachedImage),
		ReleaseYear:   searchIntPtr(doc.ReleaseYear),
		RatingsCount:  searchIntValue(doc.RatingsCount),
		Rating:        searchFloatValue(doc.Rating),
		ISBNs:         searchISBNList(doc.ISBNs),
		Genres:        searchStringList(doc.Genres, nil),
		HasAudiobook:  searchBool(doc.HasAudiobook),
		HasEbook:      searchBool(doc.HasEbook),
		SeriesRefs:    searchSeriesRefs(doc.FeaturedSeries, doc.FeaturedSeriesID, doc.FeaturedSeriesPosition),
		Contributions: make([]hcContribution, 0, len(doc.Contributions)),
		AuthorNames:   doc.AuthorNames,
	}
	for _, contribution := range doc.Contributions {
		author, ok := authorSearchDocumentToAuthor(contribution.Author)
		if ok {
			book.Contributions = append(book.Contributions, hcContribution{Author: author})
		}
	}
	return book, true
}

func searchImage(values ...any) *hcImage {
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			continue
		case string:
			if url := strings.TrimSpace(v); url != "" {
				return &hcImage{URL: url}
			}
		case map[string]any:
			if url, ok := v["url"].(string); ok && strings.TrimSpace(url) != "" {
				return &hcImage{URL: strings.TrimSpace(url)}
			}
			if url, ok := v["image_url"].(string); ok && strings.TrimSpace(url) != "" {
				return &hcImage{URL: strings.TrimSpace(url)}
			}
		}
	}
	return nil
}

func searchIntPtr(value any) *int {
	i, ok := searchInt(value)
	if !ok {
		return nil
	}
	return &i
}

func searchIntValue(value any) int {
	i, _ := searchInt(value)
	return i
}

func searchInt(value any) (int, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case int:
		return v, true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		i, err := strconv.Atoi(strconv.FormatFloat(v, 'f', 0, 64))
		return i, err == nil
	case json.Number:
		i, err := strconv.Atoi(v.String())
		return i, err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		return i, err == nil
	default:
		i, err := strconv.Atoi(seriesIDString(v))
		return i, err == nil
	}
}

func searchFloatValue(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return v
	case json.Number:
		f, _ := strconv.ParseFloat(v.String(), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		f, _ := strconv.ParseFloat(seriesIDString(v), 64)
		return f
	}
}

func searchISBNList(value any) []string {
	return searchStringList(value, isbnutil.Normalize)
}

func searchStringList(value any, normalize func(string) string) []string {
	var out []string
	seen := make(map[string]struct{})
	var add func(any)
	add = func(item any) {
		switch v := item.(type) {
		case nil:
			return
		case []any:
			for _, elem := range v {
				add(elem)
			}
		case []string:
			for _, elem := range v {
				add(elem)
			}
		case string:
			value := strings.TrimSpace(v)
			if strings.HasPrefix(value, "[") {
				var nested []any
				if err := json.Unmarshal([]byte(value), &nested); err == nil {
					add(nested)
					return
				}
			}
			if normalize != nil {
				value = normalize(value)
			}
			value = strings.TrimSpace(value)
			if value == "" {
				return
			}
			key := strings.ToLower(value)
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			out = append(out, value)
		case json.Number:
			add(v.String())
		case float64:
			add(strconv.FormatFloat(v, 'f', -1, 64))
		case float32:
			add(strconv.FormatFloat(float64(v), 'f', -1, 32))
		case int:
			add(strconv.Itoa(v))
		case int8:
			add(strconv.FormatInt(int64(v), 10))
		case int16:
			add(strconv.FormatInt(int64(v), 10))
		case int32:
			add(strconv.FormatInt(int64(v), 10))
		case int64:
			add(strconv.FormatInt(v, 10))
		case uint:
			add(strconv.FormatUint(uint64(v), 10))
		case uint8:
			add(strconv.FormatUint(uint64(v), 10))
		case uint16:
			add(strconv.FormatUint(uint64(v), 10))
		case uint32:
			add(strconv.FormatUint(uint64(v), 10))
		case uint64:
			add(strconv.FormatUint(v, 10))
		default:
			return
		}
	}
	add(value)
	return out
}

func searchBool(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "t", "true", "y", "yes":
			return true
		default:
			return false
		}
	case json.Number:
		i, err := strconv.Atoi(v.String())
		return err == nil && i != 0
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return searchBool(searchScalarString(v))
	}
}

// featuredSeriesRefs builds SeriesRefs from the GraphQL featured_series fields
// on a book. Mirrors searchSeriesRefs (Typesense) so list/shelf imports get
// the same primary-series linking the search path already provides.
func featuredSeriesRefs(series *hcFeaturedSeries, idValue *int, positionValue any) []models.SeriesRef {
	var (
		title string
		id    string
	)
	if series != nil {
		title = strings.TrimSpace(series.Name)
		if series.ID > 0 {
			id = strconv.Itoa(series.ID)
		}
	}
	if id == "" && idValue != nil && *idValue > 0 {
		id = strconv.Itoa(*idValue)
	}
	if title == "" || id == "" {
		return nil
	}
	return []models.SeriesRef{{
		ForeignID: seriesIDPrefix + id,
		Title:     title,
		Position:  formatSeriesPosition(positionValue),
		Primary:   true,
	}}
}

func searchSeriesRefs(seriesValue, idValue, positionValue any) []models.SeriesRef {
	title, id := searchFeaturedSeries(seriesValue)
	if id == "" {
		id = searchNumericSeriesID(idValue)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	if id == "" {
		slog.Debug("dropping Hardcover search series ref without numeric id", "title", title, "id", idValue)
		return nil
	}
	return []models.SeriesRef{{
		ForeignID: seriesIDPrefix + id,
		Title:     title,
		Position:  formatSeriesPosition(positionValue),
		Primary:   true,
	}}
}

func searchFeaturedSeries(value any) (string, string) {
	switch v := value.(type) {
	case nil:
		return "", ""
	case string:
		return strings.TrimSpace(v), ""
	case map[string]any:
		title := firstNonEmpty(
			searchScalarString(v["name"]),
			searchScalarString(v["title"]),
			searchScalarString(v["series"]),
		)
		id := firstNonEmpty(
			searchNumericSeriesID(v["id"]),
			searchNumericSeriesID(v["series_id"]),
		)
		return title, id
	case []any:
		for _, item := range v {
			title, id := searchFeaturedSeries(item)
			if strings.TrimSpace(title) != "" {
				return title, id
			}
		}
	}
	return "", ""
}

func searchNumericSeriesID(value any) string {
	id, ok := searchInt(value)
	if !ok || id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func searchScalarString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
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
	seriesRefs := b.SeriesRefs
	if len(seriesRefs) == 0 {
		seriesRefs = featuredSeriesRefs(b.FeaturedSeries, b.FeaturedSeriesID, b.FeaturedSeriesPosition)
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
		ISBNs:            b.ISBNs,
		SeriesRefs:       seriesRefs,
		Language:         hardcoverLanguageName(b.Language),
	}
	if len(b.Genres) > 0 {
		bk.Genres = b.Genres
	}
	hasAudiobook := b.HasAudiobook || hasPositiveInt(b.DefaultAudioEditionID)
	hasEbook := b.HasEbook || hasPositiveInt(b.DefaultEbookEditionID)
	switch {
	case hasAudiobook && hasEbook:
		bk.MediaType = models.MediaTypeBoth
	case hasAudiobook:
		bk.MediaType = models.MediaTypeAudiobook
	case hasEbook:
		bk.MediaType = models.MediaTypeEbook
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
	} else if len(b.AuthorNames) > 0 {
		for _, authorName := range b.AuthorNames {
			name := strings.TrimSpace(authorName)
			if name == "" {
				continue
			}
			bk.Author = &models.Author{
				Name:             name,
				SortName:         sortName(name),
				MetadataProvider: "hardcover",
			}
			break
		}
	}
	return bk
}

func hasPositiveInt(value *int) bool {
	return value != nil && *value > 0
}

func hardcoverEditionToModel(e hcEdition) models.Edition {
	title := strings.TrimSpace(e.Title)
	if title == "" && e.Book != nil {
		title = strings.TrimSpace(e.Book.Title)
	}
	format := firstNonEmpty(e.PhysicalFormat, e.EditionFormat, hardcoverReadingFormat(e))
	ed := models.Edition{
		ForeignID:   idPrefix + strconv.Itoa(e.ID),
		Title:       title,
		Publisher:   hardcoverPublisherName(e.Publisher),
		PublishDate: parseHardcoverEditionDate(e.ReleaseDate, e.ReleaseYear),
		Format:      format,
		NumPages:    positiveIntPtr(e.Pages),
		Language:    hardcoverLanguageName(e.Language),
		ImageURL:    hardcoverImageURL(e.Image),
		IsEbook:     hardcoverEditionIsEbook(format, hardcoverReadingFormat(e)),
		EditionInfo: strings.TrimSpace(e.EditionInformation),
		Monitored:   true,
	}
	ed.ISBN10 = nonEmptyStringPtr(e.ISBN10)
	ed.ISBN13 = nonEmptyStringPtr(e.ISBN13)
	ed.ASIN = nonEmptyStringPtr(e.ASIN)
	return ed
}

func parseHardcoverEditionDate(releaseDate string, releaseYear *int) *time.Time {
	releaseDate = strings.TrimSpace(releaseDate)
	if releaseDate != "" {
		for _, layout := range []string{"2006-01-02", time.RFC3339} {
			t, err := time.Parse(layout, releaseDate)
			if err == nil {
				return &t
			}
		}
	}
	if releaseYear != nil && *releaseYear > 0 {
		t := time.Date(*releaseYear, 1, 1, 0, 0, 0, 0, time.UTC)
		return &t
	}
	return nil
}

func hardcoverEditionIsEbook(values ...string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.Contains(normalized, "ebook") ||
			strings.Contains(normalized, "e-book") ||
			strings.Contains(normalized, "kindle") {
			return true
		}
	}
	return false
}

func hardcoverReadingFormat(e hcEdition) string {
	if e.ReadingFormat == nil {
		return ""
	}
	return strings.TrimSpace(e.ReadingFormat.Format)
}

func hardcoverPublisherName(publisher *hcPublisher) string {
	if publisher == nil {
		return ""
	}
	return strings.TrimSpace(publisher.Name)
}

func hardcoverLanguageName(language *hcLanguage) string {
	if language == nil {
		return ""
	}
	code := strings.ToLower(strings.TrimSpace(language.Language))
	if code == "" {
		return ""
	}
	if mapped, ok := hardcoverLanguageAliases[code]; ok {
		return mapped
	}
	return code
}

var hardcoverLanguageAliases = map[string]string{
	"english":    "eng",
	"en":         "eng",
	"german":     "ger",
	"de":         "ger",
	"deu":        "ger",
	"french":     "fre",
	"fr":         "fre",
	"fra":        "fre",
	"spanish":    "spa",
	"es":         "spa",
	"italian":    "ita",
	"it":         "ita",
	"dutch":      "dut",
	"nl":         "dut",
	"nld":        "dut",
	"portuguese": "por",
	"pt":         "por",
	"japanese":   "jpn",
	"ja":         "jpn",
	"russian":    "rus",
	"ru":         "rus",
	"chinese":    "chi",
	"zh":         "chi",
	"danish":     "dan",
	"da":         "dan",
	"swedish":    "swe",
	"sv":         "swe",
	"norwegian":  "nor",
	"no":         "nor",
	"polish":     "pol",
	"pl":         "pol",
	"finnish":    "fin",
	"fi":         "fin",
	"hindi":      "hin",
	"hi":         "hin",
	"turkish":    "tur",
	"tr":         "tur",
	"arabic":     "ara",
	"ar":         "ara",
	"korean":     "kor",
	"ko":         "kor",
	"czech":      "cze",
	"cs":         "cze",
	"greek":      "gre",
	"el":         "gre",
	"hungarian":  "hun",
	"hu":         "hun",
	"romanian":   "rum",
	"ro":         "rum",
	"catalan":    "cat",
	"ca":         "cat",
	"latin":      "lat",
	"la":         "lat",
}

func hardcoverImageURL(image *hcImage) string {
	if image == nil {
		return ""
	}
	return strings.TrimSpace(image.URL)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func nonEmptyStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func positiveIntPtr(value *int) *int {
	if value == nil || *value <= 0 {
		return nil
	}
	n := *value
	return &n
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

func hardcoverNumericID(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
