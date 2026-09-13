// Package audible provides a read-only client for Audible's public catalogue
// API (api.audible.com). Used to supplement OpenLibrary/Hardcover ingestion:
// neither has complete Audible ASIN cross-referencing, so prolific authors
// lose a significant fraction of their audiobook catalogue without a
// direct source.
//
// Role: supplemental source — attached to the aggregator for author-based
// audiobook lookups. Not a full Provider (the catalogue endpoint is scoped
// to products, not author/work entities), so it exposes the narrower
// SearchBooksByAuthor method consumed by the aggregator.
//
// Endpoint: https://api.audible.com/1.0/catalog/products
// Auth: none required for the response groups we ask for (product_desc,
// product_attrs, media, contributors).
package audible

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	defaultBaseURL = "https://api.audible.com"
	// IDPrefix is the ForeignID namespace for books sourced from Audible.
	// Using the ASIN keeps rows globally unique across regions and mirrors
	// the "dnb:" / "OL..." prefix conventions used by sibling providers.
	IDPrefix = "audible:"
	// maxResults is the per-request page size. Audible caps this at 50.
	maxResults = 50
	// maxCataloguePages bounds pagination so an author name that matches an
	// enormous (or maliciously inflated) result set can't trigger unbounded
	// requests. 40 pages × 50 = 2000 products, which mirrors the equivalent
	// OpenLibrary bound (authorWorksMaxFetch) and clears the most prolific
	// real authors by a wide margin.
	maxCataloguePages = 40
)

// Client talks to the Audible catalogue API. Zero value is NOT usable; use
// New().
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns an Audible catalogue client pointed at api.audible.com (US).
// Other regional catalogues (audible.co.uk, audible.de, etc.) tend to ship
// a strict subset of the US catalogue for English titles, so the .com
// endpoint is the best single source for ASIN coverage. Callers that need
// another region can swap baseURL via a future option; keeping it pinned
// here avoids the combinatorial explosion of region-specific ASINs for
// v1.1.0.
func New() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
	}
}

// catalogueProduct is the subset of product fields used to build a Book.
// Audible's full response group payload is much larger; add fields here as
// concrete needs arise.
type catalogueProduct struct {
	ASIN             string            `json:"asin"`
	Title            string            `json:"title"`
	Subtitle         string            `json:"subtitle"`
	Language         string            `json:"language"`
	PublisherSummary string            `json:"publisher_summary"`
	RuntimeLengthMin int               `json:"runtime_length_min"`
	ReleaseDate      string            `json:"release_date"`
	FormatType       string            `json:"format_type"`
	Authors          []contributorName `json:"authors"`
	Narrators        []contributorName `json:"narrators"`
	ProductImages    map[string]string `json:"product_images"`
}

type contributorName struct {
	ASIN string `json:"asin,omitempty"`
	Name string `json:"name"`
}

type catalogueResponse struct {
	Products []catalogueProduct `json:"products"`
	// TotalResults is the catalogue's own count of matches for the query,
	// independent of the page window. Used to stop paging as soon as every
	// advertised product has been seen, rather than always spending one
	// extra round-trip to observe an empty page.
	TotalResults int `json:"total_results"`
}

// SearchBooksByAuthor queries the catalogue by author name and returns
// audiobook entries keyed by ASIN. Filtering by abridged/unabridged and
// language is left to the caller — SearchBooksByAuthor returns every hit
// so the calling aggregator can apply the active metadata profile's
// allowed_languages set consistently with the OpenLibrary ingestion path.
//
// Empty queries return nil, nil rather than hitting the endpoint — the
// catalogue API treats a missing author parameter as an unfiltered browse
// and would return unrelated results.
//
// The catalogue endpoint is paginated and num_results is capped at 50, so a
// single request truncates any author with a larger catalogue. Crucially,
// OMITTING page is not equivalent to page=1: page is 0-indexed, so an
// unpaged request returns window 0 and page=1 returns a DISJOINT second
// window. Requesting only the unpaged response therefore dropped every book
// past the first 50 with no error and no log line (#1751). Pages are walked
// from 0 and accumulated, deduplicated by ASIN so an overlap or a shifting
// sort between requests can't produce duplicate rows.
func (c *Client) SearchBooksByAuthor(ctx context.Context, author string) ([]models.Book, error) {
	author = strings.TrimSpace(author)
	if author == "" {
		return nil, nil
	}

	books := make([]models.Book, 0, maxResults)
	seen := make(map[string]struct{}, maxResults)

	for page := 0; page < maxCataloguePages; page++ {
		parsed, err := c.fetchCataloguePage(ctx, author, page)
		if err != nil {
			// A failure partway through leaves a truncated catalogue, which is
			// exactly the bug this loop exists to fix — surface it rather than
			// returning a silently short list. The first page is the only one
			// with no partial results to lose.
			return nil, err
		}
		if len(parsed.Products) == 0 {
			break
		}
		for _, p := range parsed.Products {
			b := productToBook(p)
			if b == nil {
				continue
			}
			if _, dup := seen[b.ASIN]; dup {
				continue
			}
			seen[b.ASIN] = struct{}{}
			books = append(books, *b)
		}
		// Stop as soon as the advertised total has been enumerated. Counting
		// raw products (not accepted books) keeps the comparison against
		// total_results honest when productToBook rejects an entry.
		if parsed.TotalResults > 0 && (page+1)*maxResults >= parsed.TotalResults {
			break
		}
		// A short page means the catalogue is exhausted even if total_results
		// was absent or wrong.
		if len(parsed.Products) < maxResults {
			break
		}
	}
	return books, nil
}

// fetchCataloguePage requests a single page of the author's catalogue. page is
// 0-indexed, matching the API: page=0 and an omitted page parameter return the
// same first window.
func (c *Client) fetchCataloguePage(ctx context.Context, author string, page int) (catalogueResponse, error) {
	params := url.Values{
		"author":           {author},
		"num_results":      {strconv.Itoa(maxResults)},
		"page":             {strconv.Itoa(page)},
		"response_groups":  {"product_desc,product_attrs,media,contributors"},
		"products_sort_by": {"-ReleaseDate"},
	}
	endpoint := c.baseURL + "/1.0/catalog/products?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalogueResponse{}, err
	}
	req.Header.Set("User-Agent", useragent.Get())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return catalogueResponse{}, fmt.Errorf("audible search by author %q page %d: %w", author, page, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return catalogueResponse{}, fmt.Errorf("audible HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed catalogueResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return catalogueResponse{}, fmt.Errorf("decode audible response: %w", err)
	}
	return parsed, nil
}

// productToBook converts a catalogue entry to a Book. Returns nil when the
// product is missing an ASIN or title — both are required for downstream
// ingestion (the ASIN is the ForeignID, the title is the dedup key).
func productToBook(p catalogueProduct) *models.Book {
	asin := strings.ToUpper(strings.TrimSpace(p.ASIN))
	title := strings.TrimSpace(p.Title)
	if asin == "" || title == "" {
		return nil
	}
	if sub := strings.TrimSpace(p.Subtitle); sub != "" {
		title = title + ": " + sub
	}

	b := &models.Book{
		ForeignID:        IDPrefix + asin,
		Title:            title,
		SortTitle:        title,
		ASIN:             asin,
		MediaType:        models.MediaTypeAudiobook,
		Language:         normalizeLanguage(p.Language),
		Description:      strings.TrimSpace(p.PublisherSummary),
		DurationSeconds:  p.RuntimeLengthMin * 60,
		MetadataProvider: "audible",
		Status:           models.BookStatusWanted,
		Monitored:        true,
		Genres:           []string{},
	}

	if len(p.Narrators) > 0 {
		names := make([]string, 0, len(p.Narrators))
		for _, n := range p.Narrators {
			if n.Name != "" {
				names = append(names, n.Name)
			}
		}
		b.Narrator = strings.Join(names, ", ")
	}

	if cover := pickLargestCover(p.ProductImages); cover != "" {
		b.ImageURL = cover
	}

	if t := parseReleaseDate(p.ReleaseDate); t != nil {
		b.ReleaseDate = t
	}

	return b
}

// normalizeLanguage turns the full-word language names Audible returns
// ("english", "german") into the three-letter ISO 639-2/B codes Bindery uses
// everywhere else (OpenLibrary, DNB, metadata profiles). Anything unmapped
// passes through untouched so unusual values still reach the bead without
// being silently dropped.
//
// The name table used to live here. It is now shared with the release-name
// language filter and the book-level filter (#2463), because four private
// copies of it disagreed about which languages exist.
func normalizeLanguage(s string) string {
	return models.NormalizeLanguageCode(s)
}

// pickLargestCover returns the highest-resolution image URL in the product
// images map. Audible keys are pixel-width strings ("500", "1024"); we
// parse and pick the largest available so the persisted book row carries
// a cover usable for both thumbnails and detail views.
func pickLargestCover(images map[string]string) string {
	if len(images) == 0 {
		return ""
	}
	best := ""
	bestW := 0
	for k, v := range images {
		if v == "" {
			continue
		}
		w, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		if w > bestW {
			bestW = w
			best = v
		}
	}
	return best
}

// parseReleaseDate accepts the catalogue's ISO-8601 shape
// ("2007-08-07" or full RFC3339). Returns nil for empty/unparseable input.
func parseReleaseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	layouts := []string{"2006-01-02", time.RFC3339}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
