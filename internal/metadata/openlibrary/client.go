// Package openlibrary provides the primary book metadata client. It uses
// OpenLibrary's documented public APIs to fetch authors, works, and editions.
package openlibrary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// ErrNotFound signals a 404 from OpenLibrary. Callers use errors.Is to
// distinguish "this ISBN/work doesn't exist in the catalog" from genuine
// upstream failures so the UI can show a friendly message.
var ErrNotFound = errors.New("not found")

const (
	baseURL   = "https://openlibrary.org"
	coverURL  = "https://covers.openlibrary.org"
	userAgent = "Bindery/0.1 (https://github.com/vavallee/bindery)"
)

// Client implements the metadata.Provider interface for OpenLibrary.
type Client struct {
	http *http.Client
}

// New creates a new OpenLibrary client.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Name() string { return "openlibrary" }

func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	u := fmt.Sprintf("%s/search/authors.json?q=%s&limit=20", baseURL, url.QueryEscape(query))
	var resp authorSearchResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search authors: %w", err)
	}

	authors := make([]models.Author, 0, len(resp.Docs))
	for _, doc := range resp.Docs {
		a := models.Author{
			ForeignID:        doc.Key,
			Name:             doc.Name,
			SortName:         sortName(doc.Name),
			Disambiguation:   doc.TopWork,
			AverageRating:    doc.RatingsAvg,
			RatingsCount:     doc.RatingsCount,
			MetadataProvider: "openlibrary",
			Monitored:        true,
		}
		a.Statistics = &models.AuthorStats{
			BookCount: doc.WorkCount,
		}
		authors = append(authors, a)
	}
	return authors, nil
}

func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	u := fmt.Sprintf("%s/search.json?q=%s&fields=key,title,author_name,author_key,first_publish_year,cover_i,isbn,subject&limit=20",
		baseURL, url.QueryEscape(query))
	var resp searchResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search books: %w", err)
	}

	books := make([]models.Book, 0, len(resp.Docs))
	for _, doc := range resp.Docs {
		workID := strings.TrimPrefix(doc.Key, "/works/")
		b := models.Book{
			ForeignID:        workID,
			Title:            doc.Title,
			SortTitle:        doc.Title,
			Genres:           truncateSlice(doc.Subject, 10),
			MetadataProvider: "openlibrary",
			Monitored:        true,
			Status:           models.BookStatusWanted,
		}
		if doc.CoverI != nil {
			b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, *doc.CoverI)
		}
		if doc.FirstPublishYear > 0 {
			t := time.Date(doc.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
			b.ReleaseDate = &t
		}
		if len(doc.AuthorName) > 0 {
			b.Author = &models.Author{
				Name:     doc.AuthorName[0],
				SortName: sortName(doc.AuthorName[0]),
			}
			if len(doc.AuthorKey) > 0 {
				b.Author.ForeignID = doc.AuthorKey[0]
				b.Author.MetadataProvider = "openlibrary"
			}
		}
		books = append(books, b)
	}
	return books, nil
}

func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	// foreignID is like "OL123A"
	u := fmt.Sprintf("%s/authors/%s.json", baseURL, foreignID)
	var resp authorResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get author %s: %w", foreignID, err)
	}

	name := resp.Name
	if name == "" {
		name = resp.PersonalName
	}

	a := &models.Author{
		ForeignID:        foreignID,
		Name:             name,
		SortName:         sortName(name),
		Description:      extractText(resp.Bio),
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}

	if len(resp.Photos) > 0 && resp.Photos[0] > 0 {
		a.ImageURL = fmt.Sprintf("%s/a/id/%d-L.jpg", coverURL, resp.Photos[0])
	}

	return a, nil
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	u := fmt.Sprintf("%s/works/%s.json", baseURL, foreignID)
	var resp workResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get book %s: %w", foreignID, err)
	}

	b := &models.Book{
		ForeignID:        foreignID,
		Title:            resp.Title,
		SortTitle:        resp.Title,
		Description:      extractText(resp.Description),
		Genres:           truncateSlice(resp.Subjects, 10),
		MetadataProvider: "openlibrary",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}

	if len(resp.Covers) > 0 && resp.Covers[0] > 0 {
		b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, resp.Covers[0])
	}

	// Parse series membership.
	for i, s := range resp.Series {
		if s == "" {
			continue
		}
		ref := parseSeriesRef(s)
		ref.Primary = i == 0
		b.SeriesRefs = append(b.SeriesRefs, ref)
	}

	// Resolve author
	if len(resp.Authors) > 0 {
		authorKey := strings.TrimPrefix(resp.Authors[0].Author.Key, "/authors/")
		author, err := c.GetAuthor(ctx, authorKey)
		if err != nil {
			slog.Warn("failed to resolve author", "key", authorKey, "error", err)
		} else {
			b.Author = author
			b.AuthorID = author.ID
		}
	}

	return b, nil
}

// GetAuthorWorks fetches all works by an author using the dedicated author works endpoint.
// This is much more reliable than searching by author name.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	u := fmt.Sprintf("%s/authors/%s/works.json?limit=100", baseURL, authorForeignID)
	var resp authorWorksResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get author works %s: %w", authorForeignID, err)
	}

	// Supplement with language data from the search index (best-effort; don't fail on error)
	langMap := c.authorWorkLanguages(ctx, authorForeignID)

	books := make([]models.Book, 0, len(resp.Entries))
	for _, entry := range resp.Entries {
		workID := strings.TrimPrefix(entry.Key, "/works/")
		b := models.Book{
			ForeignID:        workID,
			Title:            entry.Title,
			SortTitle:        entry.Title,
			Description:      extractText(entry.Description),
			Genres:           truncateSlice(entry.Subjects, 10),
			Language:         langMap[workID],
			MetadataProvider: "openlibrary",
			Monitored:        true,
			Status:           models.BookStatusWanted,
		}
		if len(entry.Covers) > 0 && entry.Covers[0] > 0 {
			b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, entry.Covers[0])
		}
		// Set author reference
		b.Author = &models.Author{
			ForeignID:        authorForeignID,
			MetadataProvider: "openlibrary",
		}
		// Parse series membership from the OL series strings.
		for i, s := range entry.Series {
			if s == "" {
				continue
			}
			ref := parseSeriesRef(s)
			ref.Primary = i == 0
			b.SeriesRefs = append(b.SeriesRefs, ref)
		}
		books = append(books, b)
	}
	return books, nil
}

// authorWorkLanguages calls the OL search index to get the primary language for each
// work by this author. Returns a map of workID (e.g. "OL123W") → language code (e.g. "eng").
// Errors are silently swallowed — language data is best-effort.
func (c *Client) authorWorkLanguages(ctx context.Context, authorForeignID string) map[string]string {
	u := fmt.Sprintf("%s/search.json?author_key=%s&fields=key,language&limit=200", baseURL, authorForeignID)
	var resp struct {
		Docs []struct {
			Key      string   `json:"key"`
			Language []string `json:"language"`
		} `json:"docs"`
	}
	if err := c.getJSON(ctx, u, &resp); err != nil {
		slog.Debug("could not fetch language data for author works", "author", authorForeignID, "error", err)
		return nil
	}

	m := make(map[string]string, len(resp.Docs))
	for _, doc := range resp.Docs {
		if len(doc.Language) == 0 {
			continue
		}
		workID := strings.TrimPrefix(doc.Key, "/works/")
		// Prefer "eng" if present among the languages, otherwise use the first entry.
		lang := doc.Language[0]
		for _, l := range doc.Language {
			if l == "eng" {
				lang = "eng"
				break
			}
		}
		m[workID] = lang
	}
	return m
}

func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	u := fmt.Sprintf("%s/works/%s/editions.json?limit=50", baseURL, bookForeignID)
	var resp editionsResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get editions for %s: %w", bookForeignID, err)
	}

	editions := make([]models.Edition, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		editionID := strings.TrimPrefix(e.Key, "/books/")
		ed := models.Edition{
			ForeignID: editionID,
			Title:     e.Title,
			Publisher: first(e.Publishers),
			Format:    e.PhysicalFormat,
			NumPages:  nilIfZero(e.NumberOfPages),
			Monitored: true,
		}
		if len(e.ISBN13) > 0 {
			ed.ISBN13 = &e.ISBN13[0]
		}
		if len(e.ISBN10) > 0 {
			ed.ISBN10 = &e.ISBN10[0]
		}
		if len(e.Languages) > 0 {
			ed.Language = strings.TrimPrefix(e.Languages[0].Key, "/languages/")
		}
		if len(e.Covers) > 0 && e.Covers[0] > 0 {
			ed.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, e.Covers[0])
		}
		format := strings.ToLower(ed.Format)
		ed.IsEbook = strings.Contains(format, "ebook") || strings.Contains(format, "kindle")
		editions = append(editions, ed)
	}
	return editions, nil
}

// GetSubjectBooks fetches the top books for an OpenLibrary subject.
// subject should be a lowercase slug using underscores, e.g. "science_fiction" or "fantasy".
// Returns candidates suitable for use as genre-popular recommendations.
func (c *Client) GetSubjectBooks(ctx context.Context, subject string, limit int) ([]models.RecommendationCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	u := fmt.Sprintf("%s/subjects/%s.json?limit=%d", baseURL, url.PathEscape(subject), limit)
	var resp subjectBooksResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get subject books %q: %w", subject, err)
	}

	candidates := make([]models.RecommendationCandidate, 0, len(resp.Works))
	for _, w := range resp.Works {
		workID := strings.TrimPrefix(w.Key, "/works/")
		cand := models.RecommendationCandidate{
			ForeignID: workID,
			Title:     w.Title,
			Genres:    truncateSlice(w.Subject, 10),
			MediaType: models.MediaTypeEbook,
		}
		if w.CoverID != nil && *w.CoverID > 0 {
			cand.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, *w.CoverID)
		}
		if w.FirstPublishYear > 0 {
			t := time.Date(w.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
			cand.ReleaseDate = &t
		}
		if len(w.Authors) > 0 {
			cand.AuthorName = w.Authors[0].Name
		}
		candidates = append(candidates, cand)
	}
	return candidates, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	u := fmt.Sprintf("%s/isbn/%s.json", baseURL, isbn)
	var resp isbnResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		// Treat 404 as "no such ISBN" rather than an upstream error so the
		// API layer can respond with a friendly message (issue #284).
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("isbn lookup %s: %w", isbn, err)
	}

	// If we have a work reference, get the full work
	if len(resp.Works) > 0 {
		workID := strings.TrimPrefix(resp.Works[0].Key, "/works/")
		return c.GetBook(ctx, workID)
	}

	// Fallback: construct from ISBN response
	b := &models.Book{
		ForeignID:        strings.TrimPrefix(resp.Key, "/books/"),
		Title:            resp.Title,
		SortTitle:        resp.Title,
		MetadataProvider: "openlibrary",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}
	if len(resp.Covers) > 0 && resp.Covers[0] > 0 {
		b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, resp.Covers[0])
	}
	return b, nil
}

func (c *Client) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// extractText handles OpenLibrary's description field which can be a string
// or an object like {"type": "/type/text", "value": "..."}.
func extractText(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]interface{}:
		if val, ok := t["value"]; ok {
			if s, ok := val.(string); ok {
				return s
			}
		}
	}
	return ""
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

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func truncateSlice(s []string, max int) []string {
	if s == nil {
		return []string{}
	}
	if len(s) > max {
		return s[:max]
	}
	return s
}

// rePoundPos matches a series position suffix like "#1" or "#1.5".
var rePoundPos = regexp.MustCompile(`\s*#(\d+(?:\.\d+)?)\s*$`)

// reBookPos matches variants like ", Book 1", " -- Book 2", " Book 3".
var reBookPos = regexp.MustCompile(`(?:,?\s*-{1,2}\s*|,\s*|\s+)[Bb]ook\s+(\d+(?:\.\d+)?)\s*$`)

// parseSeriesRef parses an OpenLibrary series string (e.g. "Dune Chronicles #1")
// into a SeriesRef with a stable ForeignID slug, extracted title, and position.
func parseSeriesRef(raw string) models.SeriesRef {
	title := strings.TrimSpace(raw)
	position := ""

	if m := rePoundPos.FindStringSubmatchIndex(title); m != nil {
		position = title[m[2]:m[3]]
		title = strings.TrimSpace(title[:m[0]])
	} else if m := reBookPos.FindStringSubmatchIndex(title); m != nil {
		position = title[m[2]:m[3]]
		title = strings.TrimSpace(title[:m[0]])
	}

	return models.SeriesRef{
		ForeignID: "ol-series:" + seriesSlug(title),
		Title:     title,
		Position:  position,
	}
}

// seriesSlug converts a series title to a lowercase slug suitable for use as a
// foreign_id (e.g. "Dune Chronicles" → "dune-chronicles").
func seriesSlug(title string) string {
	var buf []byte
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			buf = append(buf, byte(r)) //nolint:gosec // r is gated to ASCII range above
			prevDash = false
		} else if !prevDash && len(buf) > 0 {
			buf = append(buf, '-')
			prevDash = true
		}
	}
	// trim trailing dash
	for len(buf) > 0 && buf[len(buf)-1] == '-' {
		buf = buf[:len(buf)-1]
	}
	return string(buf)
}
