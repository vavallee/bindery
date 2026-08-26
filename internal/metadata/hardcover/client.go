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

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
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

	// hardcoverErrorResponseBodyLimit caps how much of a non-200 body we read.
	// Only parsed JSON fields ever reach the error message, so this needs to
	// cover Hardcover's error envelope and nothing more.
	hardcoverErrorResponseBodyLimit = 4 << 10

	// hardcoverErrorDetailLimit caps how much upstream text we quote back.
	hardcoverErrorDetailLimit = 200
)

// Client implements metadata.Provider for Hardcover.app using its GraphQL API.
// As of 2026 the endpoint rejects unauthenticated requests with
// {"error":"Unable to verify token"} for every query — including plain
// search — so a token must be set via WithToken, WithTokenSource, or
// NewAuthenticated for any call to succeed.
type Client struct {
	http        *http.Client
	token       string // API token; required for all queries (search included)
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
		http: &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
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
		// Honor BINDERY_OUTBOUND_PROXY like New(); without the proxy transport,
		// the Hardcover list syncer and import-list browse (the callers of
		// NewAuthenticated) dial hardcover.app directly while every other
		// Hardcover call is proxied (#proxy-bypass).
		http:  &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
		token: token,
	}
}

func (c *Client) Name() string { return "hardcover" }

func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
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
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
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
// authorWorksProjection is the field set every author-works query returns.
//
// NB: do not select `language` directly here. It is an *edition* field; the
// `books` type has no `language`, so requesting it makes Hardcover reject the
// whole query ("field 'language' not found in type: 'books'",
// validation-failed). Derive it through the default-edition relations instead.
const authorWorksProjection = `
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
		compilation
		audio_seconds
		default_audio_edition_id
		default_ebook_edition_id
		default_ebook_edition { language { language } }
		default_audio_edition { language { language } }
		book_series(order_by: { position: asc }) {
			position
			series { id name }
		}
		contributions {
			contribution
			author { id name slug }
		}`

// authorWorksQuery builds a paged author-works query around an author
// predicate. The contributions predicate always pins the *role* as well:
// matching on the author alone returned every book the person touched in any
// capacity, so a narrator's or translator's credits were imported as their own
// works and re-broke manual corrections on every metadata refresh (#1733).
func authorWorksQuery(name, authorPredicate, authorVarDecl string) string {
	return `query ` + name + `(` + authorVarDecl + `, $limit: Int!, $offset: Int!) {
		books(
			where: {
				canonical_id: {_is_null: true},
				contributions: {author: {` + authorPredicate + `}, ` + authorContributionFilter + `}
			},
			limit: $limit,
			offset: $offset,
			order_by: {users_count: desc}
		) {` + authorWorksProjection + `
		}
	}`
}

// fetchAuthorWorks runs a paged author-works query with the supplied predicate
// variables.
func (c *Client) fetchAuthorWorks(ctx context.Context, gql string, vars map[string]any) ([]models.Book, error) {
	books := make([]models.Book, 0, authorWorksPageSize)
	for offset := 0; offset < authorWorksMaxBooks; offset += authorWorksPageSize {
		var resp struct {
			Data struct {
				Books []hcBook `json:"books"`
			} `json:"data"`
		}
		page := make(map[string]any, len(vars)+2)
		for k, v := range vars {
			page[k] = v
		}
		page["limit"] = authorWorksPageSize
		page["offset"] = offset
		if err := c.query(ctx, gql, page, &resp); err != nil {
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

// GetAuthorWorksByName selects works by author *name*.
//
// Two different people publishing under the same name are indistinguishable to
// this query, so it merges them (#1734: two authors both publishing as "J.A.
// Andrews", one of whom picked up ~44 books that were not theirs). It is
// correct only where no better identity exists yet, which is the initial
// resolve step, where a human or a confidence score is about to pick which
// Hardcover author was meant. Once an author row carries a Hardcover identity,
// callers should use GetAuthorWorksByIdentity instead.
func (c *Client) GetAuthorWorksByName(ctx context.Context, authorName string) ([]models.Book, error) {
	authorName = strings.TrimSpace(authorName)
	if authorName == "" {
		return nil, nil
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
	gql := authorWorksQuery("GetAuthorWorksByName", "name: {_eq: $author}", "$author: String!")
	return c.fetchAuthorWorks(ctx, gql, map[string]any{"author": authorName})
}

// GetAuthorWorksByIdentity selects works by the Hardcover author the row is
// actually linked to, rather than by name (#1734).
//
// foreignID is an "hc:"-prefixed identity or the bare slug. The slug lookup is
// preferred and a primary-key lookup is only tried when the slug matches
// nothing, because a purely numeric value is ambiguous: it may be a numeric
// slug, or the DB-id fallback toAuthor emits when the slug is empty. That is
// the same ordering GetAuthor uses, and for the same reason (#1256).
//
// A slug that resolves to no author returns no works rather than falling back
// to a name search. Silently widening to the query this exists to avoid would
// re-merge the identities at exactly the moment the caller asked not to.
func (c *Client) GetAuthorWorksByIdentity(ctx context.Context, foreignID string) ([]models.Book, error) {
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(foreignID), idPrefix))
	if id == "" {
		return nil, nil
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}

	slugGQL := authorWorksQuery("GetAuthorWorksBySlug", "slug: {_eq: $slug}", "$slug: String!")
	books, err := c.fetchAuthorWorks(ctx, slugGQL, map[string]any{"slug": id})
	if err != nil || len(books) > 0 {
		return books, err
	}

	// An author with a real slug and genuinely no works is indistinguishable
	// here from a numeric id that is not a slug, so try the id form when the
	// value could be one. Costs one extra query in the empty case.
	numericID, ok := hardcoverNumericID(id)
	if !ok {
		return books, nil
	}
	idGQL := authorWorksQuery("GetAuthorWorksByID", "id: {_eq: $authorId}", "$authorId: Int!")
	return c.fetchAuthorWorks(ctx, idGQL, map[string]any{"authorId": numericID})
}

// GetAuthorWorks satisfies the aggregator's worksProvider capability, so an
// author whose primary identity is already Hardcover gets a real author-works
// query instead of falling through to SearchBooks, which is another name-shaped
// lookup with the same same-name merging problem (#1734).
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	return c.GetAuthorWorksByIdentity(ctx, authorForeignID)
}

func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
	id := strings.TrimPrefix(foreignID, idPrefix)
	const slugGQL = `query GetAuthor($slug: String!) {
		authors(where: {slug: {_eq: $slug}}, limit: 1) {
			id
			name
			slug
			bio
			image { url }
		}
	}`
	const idGQL = `query GetAuthor($id: Int!) {
		authors(where: {id: {_eq: $id}}, limit: 1) {
			id
			name
			slug
			bio
			image { url }
		}
	}`
	fetch := func(gql string, vars map[string]any) (*models.Author, error) {
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

	// Prefer the slug lookup. A purely numeric value is ambiguous — it may be a
	// numeric slug or the DB-id fallback toAuthor emits when the slug is empty —
	// so only fall back to a primary-key lookup when the slug matches nothing
	// (#1256). The old code always took the id branch for numeric values,
	// returning the wrong author for any numeric slug.
	author, err := fetch(slugGQL, map[string]any{"slug": id})
	if err != nil || author != nil {
		return author, err
	}
	if numericID, ok := hardcoverNumericID(id); ok {
		return fetch(idGQL, map[string]any{"id": numericID})
	}
	return nil, nil
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
	id := strings.TrimPrefix(foreignID, idPrefix)
	const slugGQL = `query GetBook($slug: String!) {
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
			book_series(order_by: { position: asc }) {
				position
				series { id name }
			}
			contributions {
				contribution
				author { id name slug }
			}
		}
	}`
	const idGQL = `query GetBook($id: Int!) {
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
			book_series(order_by: { position: asc }) {
				position
				series { id name }
			}
			contributions {
				contribution
				author { id name slug }
			}
		}
	}`
	fetch := func(gql string, vars map[string]any) (*models.Book, error) {
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

	// Prefer the slug lookup. A purely numeric value is ambiguous — it may be a
	// numeric slug (e.g. "1984", "2001") or the DB-id fallback toBook emits when
	// the slug is empty — so only fall back to a primary-key lookup when the slug
	// matches nothing (#1256). The old code always took the id branch for numeric
	// values, so GetBook("hc:1984") fetched whatever book had database id 1984.
	book, err := fetch(slugGQL, map[string]any{"slug": id})
	if err != nil || book != nil {
		return book, err
	}
	if numericID, ok := hardcoverNumericID(id); ok {
		return fetch(idGQL, map[string]any{"id": numericID})
	}
	return nil, nil
}

func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	id := strings.TrimSpace(strings.TrimPrefix(bookForeignID, idPrefix))
	if id == "" {
		return nil, nil
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}

	const slugGQL = `query GetEditions($slug: String!, $limit: Int!, $offset: Int!) {
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
	const idGQL = `query GetEditions($bookID: Int!, $limit: Int!, $offset: Int!) {
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

	fetchAll := func(gql string, vars map[string]any) ([]models.Edition, error) {
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

	// Prefer the slug lookup; only fall back to a book_id lookup when the slug
	// matches no editions, since a numeric value is ambiguous between a numeric
	// slug and the DB-id fallback (#1256). The old code queried book_id directly
	// for any numeric value, returning the editions of an unrelated book.
	editions, err := fetchAll(slugGQL, map[string]any{"slug": id})
	if err != nil || len(editions) > 0 {
		return editions, err
	}
	if numericID, ok := hardcoverNumericID(id); ok {
		return fetchAll(idGQL, map[string]any{"bookID": numericID})
	}
	return editions, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
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
				book_series(order_by: { position: asc }) {
					position
					series { id name }
				}
				contributions {
					contribution
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, hardcoverErrorResponseBodyLimit))
		return classifyHTTPError(resp.StatusCode, b)
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, hardcoverSuccessResponseBodyLimit))
	if err != nil {
		return err
	}
	var envelope struct {
		Errors []gqlError `json:"errors"`
	}
	if err := json.Unmarshal(b, &envelope); err == nil && len(envelope.Errors) > 0 {
		return fmt.Errorf("GraphQL: %s", httpsec.RedactSecrets(formatGraphQLErrors(envelope.Errors)))
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

// hcErrorEnvelope is the JSON body Hardcover returns when its edge rejects a
// request before the GraphQL layer sees it, for example
// {"error":"invalid_token","error_description":"Invalid or expired token"} for
// a bad token, or a bare {"error":"ilike and related operations are not
// permitted on this server."} for a rejected query operator. Errors carries the
// GraphQL envelope for the rare non-200 that still answers in GraphQL's shape.
type hcErrorEnvelope struct {
	Error            string     `json:"error"`
	ErrorDescription string     `json:"error_description"`
	Message          string     `json:"message"`
	Errors           []gqlError `json:"errors"`
}

// detail picks the most specific human-readable text the envelope carries, in
// descending order of usefulness, sanitised for inclusion in an error message.
func (e hcErrorEnvelope) detail() string {
	for _, candidate := range []string{e.ErrorDescription, e.Message, e.Error} {
		if d := sanitizeErrorDetail(candidate); d != "" {
			return d
		}
	}
	if len(e.Errors) > 0 {
		return sanitizeErrorDetail(formatGraphQLErrors(e.Errors))
	}
	return ""
}

// classifyHTTPError turns a non-200 Hardcover response into something an
// operator can act on. Until #2128 the body was pasted into the message
// verbatim, so a Hardcover outage rendered its HTML error page into the
// Settings UI and read like a token problem. Three cases matter: the token was
// rejected, the body is a structured JSON error worth quoting, and everything
// else (HTML error page, plain text, empty), which is reported as an upstream
// failure without echoing a single byte of it.
func classifyHTTPError(status int, raw []byte) error {
	envelope, parsed := parseHardcoverError(raw)
	detail := envelope.detail()
	switch {
	case isTokenRejection(status, envelope, parsed):
		if detail != "" {
			return fmt.Errorf("token rejected (HTTP %d: %s)", status, detail)
		}
		return fmt.Errorf("token rejected (HTTP %d), check the Hardcover API token in Settings", status)
	case detail != "":
		return fmt.Errorf("HTTP %d: %s", status, detail)
	case parsed:
		return fmt.Errorf("HTTP %d (upstream returned a JSON error with no description, so this is a Hardcover-side failure rather than a token problem)", status)
	case len(bytes.TrimSpace(raw)) == 0:
		return fmt.Errorf("HTTP %d (upstream returned an empty response body, so this is a Hardcover-side failure rather than a token problem)", status)
	default:
		return fmt.Errorf("HTTP %d (upstream returned a non-JSON response, likely an error page, so this is a Hardcover-side failure rather than a token problem)", status)
	}
}

// parseHardcoverError decodes the error envelope, reporting whether the body
// was a JSON object at all. A truncated or non-JSON body (an HTML error page
// being the case that prompted #2128) reports false, and the caller then says
// what happened instead of quoting it.
func parseHardcoverError(raw []byte) (hcErrorEnvelope, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return hcErrorEnvelope{}, false
	}
	var envelope hcErrorEnvelope
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return hcErrorEnvelope{}, false
	}
	return envelope, true
}

// isTokenRejection reports whether the response blames the credential rather
// than the query or the server. 401 always does. 403 only counts when the body
// carries an auth error: Hardcover also answers 403 for query operators it
// refuses (see authorContributionFilter), which no token change fixes.
func isTokenRejection(status int, envelope hcErrorEnvelope, parsed bool) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusForbidden || !parsed {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Error)) {
	case "invalid_token", "invalid_request", "invalid_grant", "unauthorized", "access_denied", "insufficient_scope":
		return true
	}
	// Unknown 403 codes still count when the text names the credential *and*
	// rejects it, which is how Hardcover phrases an unauthenticated request
	// ({"error":"Unable to verify token"}). Naming the token is not enough on
	// its own: "Token bucket exhausted, retry after 30s" is a rate limit, and
	// telling the operator to reissue a working token would send them the
	// wrong way.
	detail := strings.ToLower(envelope.detail())
	if !strings.Contains(detail, "token") {
		return false
	}
	for _, rejection := range []string{"invalid", "expired", "unable to verify", "not associated", "unauthori", "forbidden", "revoked", "missing"} {
		if strings.Contains(detail, rejection) {
			return true
		}
	}
	return false
}

// sanitizeErrorDetail makes upstream text safe to quote back: secrets redacted,
// collapsed onto one line, angle brackets dropped so markup smuggled into a
// JSON field cannot reach the UI, and hard-capped.
func sanitizeErrorDetail(s string) string {
	s = strings.Join(strings.Fields(httpsec.RedactSecrets(s)), " ")
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > hardcoverErrorDetailLimit {
		s = strings.TrimSpace(strings.ToValidUTF8(s[:hardcoverErrorDetailLimit], "")) + "…"
	}
	return s
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
	// Contribution is the role the person played on the book — Hardcover's
	// `contributions.contribution` column, a free-text nullable String, not an
	// enum. Documented values include "Author", "Narrator", "Translator",
	// "Illustrator", "Editor", "Foreword" and "Cover Artist", but real rows are
	// unstructured (mixed case, compounds like "Author/Narrator") and the
	// primary author's row usually leaves it null. Queries that omit the field
	// decode it empty, which authorContribution treats as the author role — so
	// a missing selection degrades to the pre-#1733 behaviour rather than
	// stripping every author.
	Contribution string `json:"contribution"`
}

// authorContributionFilter is the Hardcover `contributions` predicate that
// keeps only author-role rows. It must accept a null/empty role: on Hardcover
// the primary author's contribution row almost never sets `contribution`, so a
// bare `{contribution: {_eq: "Author"}}` matches nearly nothing and would
// silently return an empty result set (#1733). Verified live: on "Blackflame"
// the narrator's row reads `contribution: "Narrator"` while Will Wight's own
// row is null.
//
// The explicit spellings are NOT decoration and NOT reachable by a pattern
// match. Hardcover's server rejects pattern operators outright —
//
//	{"error":"ilike and related operations are not permitted on this server."}  (HTTP 403)
//
// — so an `_ilike: "author%"` arm does not merely fail to match, it fails the
// whole request, which would take down every per-author "Refresh Metadata" and
// "Refresh All" run. `_in` is the widest operator the server actually allows
// here. A literal "Author" is rare but real (verified live), so the arm earns
// its place: without it those authors' catalogues come back empty.
const authorContributionFilter = `_or: [{contribution: {_is_null: true}}, {contribution: {_eq: ""}}, {contribution: {_in: ["Author", "author", "AUTHOR"]}}]`

// isAuthorContributionRole mirrors authorContributionFilter client-side. An
// empty role means "author" both because Hardcover leaves the primary author's
// row null and because a query that did not select the field decodes as empty.
func isAuthorContributionRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "" || strings.HasPrefix(role, "author")
}

// authorContribution returns the first author-role contribution. Hardcover
// returns contributions in credit order, so for a co-authored book that is the
// primary author — models.Book carries a single author, so the rest are
// dropped, same as before #1733.
func authorContribution(contributions []hcContribution) (hcAuthor, bool) {
	for _, contribution := range contributions {
		if isAuthorContributionRole(contribution.Contribution) {
			return contribution.Author, true
		}
	}
	return hcAuthor{}, false
}

type hcBook struct {
	ID                    int                `json:"id"`
	Title                 string             `json:"title"`
	Subtitle              string             `json:"subtitle"`
	Slug                  string             `json:"slug"`
	Description           string             `json:"description"`
	Image                 *hcImage           `json:"image"`
	ReleaseYear           *int               `json:"release_year"`
	RatingsCount          int                `json:"ratings_count"`
	Rating                float64            `json:"rating"`
	UsersCount            int                `json:"users_count"`
	Genres                []string           `json:"genres"`
	ISBNs                 []string           `json:"isbns"`
	HasAudiobook          bool               `json:"has_audiobook"`
	HasEbook              bool               `json:"has_ebook"`
	Compilation           bool               `json:"compilation"`
	AudioSeconds          *int               `json:"audio_seconds"`
	DefaultAudioEditionID *int               `json:"default_audio_edition_id"`
	DefaultEbookEditionID *int               `json:"default_ebook_edition_id"`
	Language              *hcLanguage        `json:"language"`
	Contributions         []hcContribution   `json:"contributions"`
	AuthorNames           []string           `json:"author_names"`
	BookSeries            []hcBookSeries     `json:"book_series"`
	SeriesRefs            []models.SeriesRef `json:"-"`
	// AudioEditions carries a small, server-filtered slice of audio editions
	// that have an ASIN. List/shelf queries request it inline so the ASIN is
	// available without a per-book GetEditions round-trip (#1694). Empty for
	// every other query.
	AudioEditions []hcASINEdition `json:"editions"`
	// DefaultEbookEdition / DefaultAudioEdition carry the language of the
	// book's default editions for the same queries — the only inline source
	// of language, since `books` has no language field of its own.
	DefaultEbookEdition *hcEditionLanguage `json:"default_ebook_edition"`
	DefaultAudioEdition *hcEditionLanguage `json:"default_audio_edition"`
}

// hcASINEdition is the trimmed edition shape the list/shelf queries inline to
// carry an audiobook ASIN and a language fallback. Kept separate from
// hcEdition so the full edition projection stays exclusive to GetEditions.
// The queries order by edition id so the promoted ASIN is stable across
// syncs rather than dependent on server-side row order.
type hcASINEdition struct {
	ASIN     string      `json:"asin"`
	Language *hcLanguage `json:"language"`
}

// hcEditionLanguage carries just the language of a default-edition relation.
// The `books` GraphQL type has no direct `language` field (confirmed against
// the live API: selecting it fails validation), so list/shelf queries read it
// off default_ebook_edition / default_audio_edition instead.
type hcEditionLanguage struct {
	Language *hcLanguage `json:"language"`
}

// hcBookSeries captures the Hardcover GraphQL `book_series` relation on a
// book — the series it belongs to and its position. Used to hydrate
// SeriesRefs for list/shelf books (the `books` type has no `featured_series`
// field; that exists only on Typesense search documents).
type hcBookSeries struct {
	Position any `json:"position"`
	Series   struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"series"`
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
	// Contribution is the credit role, the same free-text field the GraphQL
	// book queries carry. #1733 filtered on it there but not here, because the
	// Typesense document shape is not published and guessing a field name that
	// decodes to empty would have silently kept the old behaviour while looking
	// like coverage.
	//
	// Confirmed against api.hardcover.app: the field is `contribution`, and on
	// the primary author it is either absent or explicit null — never the string
	// "Author" — which is exactly what isAuthorContributionRole already treats as
	// an author. Both shapes decode to "" here. The role IS spelled out for
	// non-authors ("Narrator"). A parallel top-level `contribution_types` array
	// does name the primary author explicitly, but it is a separate list that has
	// to be index-matched to this one, so the per-entry field is the safer source.
	Contribution string `json:"contribution"`
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
			// Carry the role through. Dropping it left every search-sourced
			// contribution with an empty role, which authorContribution reads as
			// "is an author", so the first credit won — and Hardcover lists the
			// narrator first on plenty of audiobook-bearing works. That is the
			// pre-#1733 behaviour surviving on the search path (#1892).
			book.Contributions = append(book.Contributions, hcContribution{
				Author:       author,
				Contribution: contribution.Contribution,
			})
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

// bookSeriesRefs builds SeriesRefs from the GraphQL book_series relation on a
// book. Mirrors searchSeriesRefs (Typesense) so list/shelf imports get the
// same series linking the search path provides. book_series is ordered by
// position asc, so the first valid entry is the primary series.
func bookSeriesRefs(bs []hcBookSeries) []models.SeriesRef {
	for _, s := range bs {
		title := strings.TrimSpace(s.Series.Name)
		if title == "" || s.Series.ID <= 0 {
			continue
		}
		return []models.SeriesRef{{
			ForeignID: seriesIDPrefix + strconv.Itoa(s.Series.ID),
			Title:     title,
			Position:  formatSeriesPosition(s.Position),
			Primary:   true,
		}}
	}
	return nil
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
		seriesRefs = bookSeriesRefs(b.BookSeries)
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
		ProviderISBNs:    b.ISBNs,
		SeriesRefs:       seriesRefs,
		Language:         hardcoverLanguageName(b.Language),
		IsCompilation:    b.Compilation,
	}
	if len(b.Genres) > 0 {
		bk.Genres = b.Genres
	}
	// An inlined audio edition is itself proof the work has an audiobook, and
	// covers the case where Hardcover reports no default_audio_edition_id but
	// audio editions exist — the promotion the removed per-book edition
	// fan-out used to perform (#1694).
	hasAudiobook := b.HasAudiobook || hasPositiveInt(b.DefaultAudioEditionID) || len(b.AudioEditions) > 0
	hasEbook := b.HasEbook || hasPositiveInt(b.DefaultEbookEditionID)
	switch {
	case hasAudiobook && hasEbook:
		bk.MediaType = models.MediaTypeBoth
	case hasAudiobook:
		bk.MediaType = models.MediaTypeAudiobook
	case hasEbook:
		bk.MediaType = models.MediaTypeEbook
	}
	// Carry an audiobook ASIN straight off the list/shelf response when the
	// query inlined one. Only list and shelf queries populate AudioEditions;
	// every other caller leaves it empty and this is a no-op (#1694).
	for _, e := range b.AudioEditions {
		if asin := strings.ToUpper(strings.TrimSpace(e.ASIN)); asin != "" {
			bk.ASIN = asin
			break
		}
	}
	// Fill Language from the inline edition relations when the book itself
	// carried none (list/shelf queries — `books` has no language field).
	// Preference: default ebook edition → default audio edition → any inline
	// audio edition. An empty Language is "unknown" to IsLanguageAllowed and
	// can silently drop the book under UnknownLanguageBehavior == fail, which
	// is why losing this on list sync mattered (#1694 review).
	if bk.Language == "" {
		candidates := []*hcLanguage{}
		if b.DefaultEbookEdition != nil {
			candidates = append(candidates, b.DefaultEbookEdition.Language)
		}
		if b.DefaultAudioEdition != nil {
			candidates = append(candidates, b.DefaultAudioEdition.Language)
		}
		for _, e := range b.AudioEditions {
			candidates = append(candidates, e.Language)
		}
		for _, c := range candidates {
			if lang := hardcoverLanguageName(c); lang != "" {
				bk.Language = lang
				break
			}
		}
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
	// Pick the author-role contribution rather than whichever row Hardcover
	// happened to return first: taking index 0 unconditionally filed books
	// under their narrator or translator (#1733 — Will Wight's Cradle books
	// landing under their audiobook narrator).
	if author, ok := authorContribution(b.Contributions); ok {
		a := c.toAuthor(author)
		bk.Author = &a
	} else if len(b.Contributions) > 0 {
		// Contributions exist but none is an author — e.g. an anthology
		// credited only to its editor. Fall back to the first credit rather
		// than leaving Author nil: a book with no author is dropped by the
		// import paths, which is worse than an imperfect credit.
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
	return textutil.SortName(name)
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
