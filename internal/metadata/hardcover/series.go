package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
)

const seriesIDPrefix = "hc-series:"

// seriesCatalogQuery fetches a series and the books filed under it.
//
// The compilation filter is written as an _or with _is_null rather than a bare
// _eq: false, because in Hasura an _eq never matches a null. A book whose
// compilation column is unset would otherwise drop out of every series catalog
// silently, which is a worse failure than the one this filter is here to fix.
// Same reasoning as canonical_id above it and authorContributionFilter in
// client.go.
//
// Omnibus editions are excluded here rather than filtered after the fact:
// Hardcover files a box set under the position of the first book it contains,
// so "Harry Potter Collection #1-6", "Harry Potter Boxed Set" and "The Harry
// Potter Trilogy" all arrive at position 1 beside the novel itself, and once
// they have arrived nothing distinguishes them from a volume. They are books
// someone may own; they are not volume 1 of the series.
//
// Reader count breaks the ties that remain. A translation is its own book
// sharing the original's position, so a position holds one entry per language
// and the one most readers hold is the one meant. collapseSeriesPositions
// applies that; ordering here only makes the result stable.
const seriesCatalogQuery = `query GetBooksBySeries($seriesId: Int!) {
		series_by_pk(id: $seriesId) {
			id
			name
			slug
			author { name }
			books_count
			book_series(
				order_by: [{position: asc}, {book: {users_count: desc_nulls_last}}]
				where: {
					book: {
						canonical_id: {_is_null: true}
						_or: [
							{compilation: {_eq: false}}
							{compilation: {_is_null: true}}
						]
					}
				}
			) {
				position
				book {
					id
					title
					subtitle
					slug
					description
					users_count
					image { url }
					release_year
					ratings_count
					rating
					default_audio_edition_id
					default_ebook_edition_id
					contributions {
						contribution
						author { id name slug }
					}
				}
			}
		}
	}`

// SearchSeries searches Hardcover's catalog using the same search endpoint
// Shelfarr uses for series discovery.
func (c *Client) SearchSeries(ctx context.Context, query string, limit int) ([]metadata.SeriesSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
	gql := `query SearchSeries($query: String!, $queryType: String!, $perPage: Int!) {
		search(query: $query, query_type: $queryType, per_page: $perPage) {
			ids
			results
		}
	}`
	var resp struct {
		Data struct {
			Search struct {
				IDs     []any           `json:"ids"`
				Results json.RawMessage `json:"results"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := c.query(ctx, gql, map[string]any{
		"query":     query,
		"queryType": "Series",
		"perPage":   limit,
	}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover search series: %w", err)
	}
	results := parseSeriesSearchResults(resp.Data.Search.Results)
	if len(results) == 0 {
		found := seriesSearchFoundCount(resp.Data.Search.Results)
		if found == 0 {
			found = len(resp.Data.Search.IDs)
		}
		if found > 0 {
			return nil, fmt.Errorf("hardcover search series: response contained %d matches but no mappable series documents", found)
		}
	}
	return results, nil
}

// GetSeriesCatalog fetches the ordered books in a Hardcover series.
func (c *Client) GetSeriesCatalog(ctx context.Context, foreignID string) (*metadata.SeriesCatalog, error) {
	if c.authorizationToken(ctx) == "" {
		return nil, metadata.ErrProviderNotConfigured
	}
	seriesID, err := hardcoverSeriesNumericID(foreignID)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Series *hcSeriesCatalog `json:"series_by_pk"`
		} `json:"data"`
	}
	if err := c.query(ctx, seriesCatalogQuery, map[string]any{"seriesId": seriesID}, &resp); err != nil {
		return nil, fmt.Errorf("hardcover get series catalog: %w", err)
	}
	if resp.Data.Series == nil {
		return nil, nil
	}
	return c.toSeriesCatalog(*resp.Data.Series), nil
}

type hcSeriesCatalog struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	BooksCount int    `json:"books_count"`
	Author     *struct {
		Name string `json:"name"`
	} `json:"author"`
	BookSeries []struct {
		Position any    `json:"position"`
		Book     hcBook `json:"book"`
	} `json:"book_series"`
}

type hcSeriesSearchEnvelope struct {
	Found int                    `json:"found"`
	Hits  []hcSeriesSearchResult `json:"hits"`
}

type hcSeriesSearchResult struct {
	Document hcSeriesSearchDocument `json:"document"`
}

type hcSeriesSearchDocument struct {
	ID any `json:"id"`
	// Slug is documented as "The URL slug of the series" in the Hardcover
	// search field list and is what hardcover.app/series/<slug> routes on.
	Slug              string   `json:"slug"`
	Name              string   `json:"name"`
	AuthorName        string   `json:"author_name"`
	PrimaryBooksCount int      `json:"primary_books_count"`
	BooksCount        int      `json:"books_count"`
	BookCount         int      `json:"book_count"`
	ReadersCount      int      `json:"readers_count"`
	Books             []string `json:"books"`
}

func parseSeriesSearchResults(raw json.RawMessage) []metadata.SeriesSearchResult {
	raw = normalizeRawSearchResults(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var envelope hcSeriesSearchEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Hits) > 0 {
		out := make([]metadata.SeriesSearchResult, 0, len(envelope.Hits))
		for _, hit := range envelope.Hits {
			if result := seriesSearchDocumentToResult(hit.Document); result.ForeignID != "" {
				out = append(out, result)
			}
		}
		return out
	}
	var items []hcSeriesSearchResult
	if err := json.Unmarshal(raw, &items); err == nil {
		out := make([]metadata.SeriesSearchResult, 0, len(items))
		for _, item := range items {
			if result := seriesSearchDocumentToResult(item.Document); result.ForeignID != "" {
				out = append(out, result)
			}
		}
		return out
	}
	var docs []hcSeriesSearchDocument
	if err := json.Unmarshal(raw, &docs); err == nil {
		out := make([]metadata.SeriesSearchResult, 0, len(docs))
		for _, doc := range docs {
			if result := seriesSearchDocumentToResult(doc); result.ForeignID != "" {
				out = append(out, result)
			}
		}
		return out
	}
	return nil
}

func normalizeRawSearchResults(raw json.RawMessage) json.RawMessage {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		return json.RawMessage(encoded)
	}
	return raw
}

func seriesSearchFoundCount(raw json.RawMessage) int {
	raw = normalizeRawSearchResults(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var envelope hcSeriesSearchEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil {
		return envelope.Found
	}
	return 0
}

func seriesSearchDocumentToResult(doc hcSeriesSearchDocument) metadata.SeriesSearchResult {
	id := seriesIDString(doc.ID)
	if id == "" {
		return metadata.SeriesSearchResult{}
	}
	count := doc.PrimaryBooksCount
	if count == 0 {
		count = doc.BooksCount
	}
	if count == 0 {
		count = doc.BookCount
	}
	return metadata.SeriesSearchResult{
		ForeignID:    seriesIDPrefix + id,
		ProviderID:   id,
		Slug:         strings.TrimSpace(doc.Slug),
		Title:        strings.TrimSpace(doc.Name),
		AuthorName:   strings.TrimSpace(doc.AuthorName),
		BookCount:    count,
		ReadersCount: doc.ReadersCount,
		Books:        doc.Books,
	}
}

func (c *Client) toSeriesCatalog(series hcSeriesCatalog) *metadata.SeriesCatalog {
	catalog := &metadata.SeriesCatalog{
		ForeignID:  seriesIDPrefix + strconv.Itoa(series.ID),
		ProviderID: strconv.Itoa(series.ID),
		Slug:       strings.TrimSpace(series.Slug),
		Title:      strings.TrimSpace(series.Name),
		BookCount:  series.BooksCount,
	}
	if series.Author != nil {
		catalog.AuthorName = strings.TrimSpace(series.Author.Name)
	}
	books := make([]metadata.SeriesCatalogBook, 0, len(series.BookSeries))
	for _, entry := range series.BookSeries {
		book := c.toBook(entry.Book)
		position := formatSeriesPosition(entry.Position)
		books = append(books, metadata.SeriesCatalogBook{
			ForeignID:  book.ForeignID,
			ProviderID: strconv.Itoa(entry.Book.ID),
			Title:      strings.TrimSpace(entry.Book.Title),
			Subtitle:   strings.TrimSpace(entry.Book.Subtitle),
			Position:   position,
			UsersCount: entry.Book.UsersCount,
			Book:       book,
		})
	}
	catalog.Books = collapseSeriesPositions(dedupeSeriesCatalogBooks(books))
	return catalog
}

// collapseSeriesPositions keeps one book per numbered slot.
//
// It runs after title dedup, not before. Title dedup handles the same book
// listed at two positions and keeps the lower one; collapsing first would
// instead pick that book as the winner of the higher slot and evict whatever
// genuinely belongs there.
//
// Hardcover records a translation as its own book sharing the original's
// position, so a slot holds one entry per language: position 1 of The
// Stormlight Archive is the English novel plus its Russian, Bulgarian, Polish
// and Portuguese counterparts. Their titles all differ, so title dedup alone
// leaves every one of them in the catalog.
//
// The entry most readers hold is the one meant, and the margin is not close —
// Harry Potter's Philosopher's Stone has some seventeen thousand readers where
// the others at that position have none. Books with no position are left
// alone; they are the extras a series accumulates rather than duplicates of a
// volume.
func collapseSeriesPositions(books []metadata.SeriesCatalogBook) []metadata.SeriesCatalogBook {
	if len(books) < 2 {
		return books
	}
	byPosition := make(map[string]int, len(books))
	out := make([]metadata.SeriesCatalogBook, 0, len(books))
	for _, book := range books {
		position := strings.TrimSpace(book.Position)
		if position == "" {
			out = append(out, book)
			continue
		}
		at, seen := byPosition[position]
		if !seen {
			byPosition[position] = len(out)
			out = append(out, book)
			continue
		}
		if book.UsersCount > out[at].UsersCount {
			out[at] = book
		}
	}
	return out
}

func dedupeSeriesCatalogBooks(books []metadata.SeriesCatalogBook) []metadata.SeriesCatalogBook {
	if len(books) < 2 {
		return books
	}
	byTitle := make(map[string]metadata.SeriesCatalogBook, len(books))
	order := make([]string, 0, len(books))
	for _, book := range books {
		key := indexer.NormalizeTitleForDedup(book.Title)
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(book.Title))
		}
		existing, ok := byTitle[key]
		if !ok {
			byTitle[key] = book
			order = append(order, key)
			continue
		}
		if compareSeriesPosition(book.Position, existing.Position) < 0 {
			byTitle[key] = book
		}
	}
	out := make([]metadata.SeriesCatalogBook, 0, len(order))
	for _, key := range order {
		out = append(out, byTitle[key])
	}
	sort.SliceStable(out, func(a, b int) bool {
		return compareSeriesPosition(out[a].Position, out[b].Position) < 0
	})
	return out
}

func compareSeriesPosition(a, b string) int {
	af, aerr := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, berr := strconv.ParseFloat(strings.TrimSpace(b), 64)
	switch {
	case aerr == nil && berr == nil && af < bf:
		return -1
	case aerr == nil && berr == nil && af > bf:
		return 1
	case aerr == nil && berr != nil:
		return -1
	case aerr != nil && berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func formatSeriesPosition(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimRight(strings.TrimRight(v.String(), "0"), ".")
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

func hardcoverSeriesNumericID(foreignID string) (int, error) {
	id := strings.TrimPrefix(strings.TrimSpace(foreignID), seriesIDPrefix)
	if id == "" {
		return 0, fmt.Errorf("hardcover series id is empty")
	}
	parsed, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("hardcover series id %q is not numeric: %w", foreignID, err)
	}
	return parsed, nil
}

func seriesIDString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
