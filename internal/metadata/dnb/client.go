// Package dnb provides a read-only client for the Deutsche Nationalbibliothek (DNB)
// via its public SRU endpoint. No API key is required.
//
// Role: enricher — fills description, publisher, language, and publication date
// for German-language titles where OpenLibrary metadata is thin. The DNB catalogue
// covers German, Austrian, and Swiss German publications since 1913.
//
// Endpoint: https://services.dnb.de/sru/dnb
// Protocol: SRU 1.1 with MARC21-XML record schema.
package dnb

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
)

const (
	sruBase      = "https://services.dnb.de/sru/dnb"
	idPrefix     = "dnb:"
	recordSchema = "MARC21-xml"
	userAgent    = "Bindery/1 (https://github.com/vavallee/bindery)"
)

// Client implements metadata.Provider for DNB via the public SRU endpoint.
type Client struct {
	http *http.Client
}

// New creates a new DNB client.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Name() string { return "dnb" }

// SearchAuthors queries DNB by person name and returns unique authors extracted
// from matching records.
func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	records, err := c.sruQuery(ctx, "per="+query, 20)
	if err != nil {
		return nil, fmt.Errorf("dnb search authors: %w", err)
	}
	seen := make(map[string]bool)
	var authors []models.Author
	for _, rec := range records {
		a := recordToAuthor(rec)
		if a == nil || seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		authors = append(authors, *a)
	}
	return authors, nil
}

// SearchBooks queries DNB by title and returns matching books.
func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	records, err := c.sruQuery(ctx, "tit="+query, 20)
	if err != nil {
		return nil, fmt.Errorf("dnb search books: %w", err)
	}
	books := make([]models.Book, 0, len(records))
	for _, rec := range records {
		if b := recordToBook(rec); b != nil {
			books = append(books, *b)
		}
	}
	return books, nil
}

// GetAuthor is not supported by the DNB SRU endpoint.
func (c *Client) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return nil, fmt.Errorf("dnb does not support author lookup by ID")
}

// GetBook fetches a single record by its DNB control number (the foreignID
// minus the "dnb:" prefix).
func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	id := strings.TrimPrefix(foreignID, idPrefix)
	records, err := c.sruQuery(ctx, "num="+id, 1)
	if err != nil {
		return nil, fmt.Errorf("dnb get book %s: %w", foreignID, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return recordToBook(records[0]), nil
}

// GetEditions is not supported; DNB doesn't expose edition lists via SRU.
func (c *Client) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return nil, nil
}

// GetBookByISBN looks up a record by ISBN-10 or ISBN-13.
func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	records, err := c.sruQuery(ctx, "isbn="+isbn, 1)
	if err != nil {
		return nil, fmt.Errorf("dnb get book by ISBN: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return recordToBook(records[0]), nil
}

// sruQuery executes a CQL query against the DNB SRU endpoint and returns the
// raw MARC21 records. The cql argument is a plain CQL expression such as
// "tit=Der letzte Sommer" or "isbn=9783446123456"; url.Values encodes it.
func (c *Client) sruQuery(ctx context.Context, cql string, maxRecords int) ([]marcRecord, error) {
	params := url.Values{
		"operation":      {"searchRetrieve"},
		"version":        {"1.1"},
		"query":          {cql},
		"recordSchema":   {recordSchema},
		"maximumRecords": {strconv.Itoa(maxRecords)},
	}
	endpoint := sruBase + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/xml")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result sruResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode SRU response: %w", err)
	}

	records := make([]marcRecord, 0, len(result.Records.Records))
	for _, r := range result.Records.Records {
		records = append(records, r.RecordData.MARCRecord)
	}
	return records, nil
}

// --- MARC converters ---

func recordToBook(rec marcRecord) *models.Book {
	id := rec.controlField("001")
	if id == "" {
		return nil
	}

	title := marcClean(rec.subfield("245", "a"))
	if title == "" {
		return nil
	}
	if sub := marcClean(rec.subfield("245", "b")); sub != "" {
		title = title + ": " + sub
	}

	b := &models.Book{
		ForeignID:        idPrefix + id,
		Title:            title,
		SortTitle:        title,
		Description:      marcClean(rec.subfield("520", "a")),
		Language:         rec.subfield("041", "a"), // 3-char code, e.g. "ger"
		MetadataProvider: "dnb",
		Monitored:        true,
		Status:           models.BookStatusWanted,
		Genres:           []string{},
	}

	// Publication year from field 264 $c (preferred) or 260 $c (older form).
	yearStr := rec.subfield("264", "c")
	if yearStr == "" {
		yearStr = rec.subfield("260", "c")
	}
	if t := parseYear(yearStr); t != nil {
		b.ReleaseDate = t
	}

	// Main entry personal name (100 $a). MARC format is "Last, First".
	if marcName := marcClean(rec.subfield("100", "a")); marcName != "" {
		displayName := invertMARCName(marcName)
		b.Author = &models.Author{
			Name:             displayName,
			SortName:         marcName, // already in "Last, First" sort form
			MetadataProvider: "dnb",
		}
	}

	return b
}

func recordToAuthor(rec marcRecord) *models.Author {
	marcName := marcClean(rec.subfield("100", "a"))
	if marcName == "" {
		return nil
	}
	id := rec.controlField("001")
	foreignID := ""
	if id != "" {
		foreignID = idPrefix + id
	}
	displayName := invertMARCName(marcName)
	return &models.Author{
		ForeignID:        foreignID,
		Name:             displayName,
		SortName:         marcName,
		MetadataProvider: "dnb",
	}
}

// --- String helpers ---

// marcClean strips common MARC trailing punctuation (" /", " :", ",", ".", ";")
// and surrounding whitespace. Applied iteratively until stable.
func marcClean(s string) string {
	s = strings.TrimSpace(s)
	for {
		prev := s
		s = strings.TrimRight(s, " /,.:;=")
		s = strings.TrimSpace(s)
		if s == prev {
			break
		}
	}
	return s
}

// invertMARCName converts MARC-form "Last, First" to display form "First Last".
// Names without a comma are returned as-is.
func invertMARCName(marc string) string {
	last, first, ok := strings.Cut(marc, ", ")
	if !ok {
		return marc
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return last
	}
	return first + " " + last
}

// parseYear extracts the first 4-digit year from a MARC date string such as
// "2023", "[2023]", or "c2020".  Returns nil when no valid year is found.
func parseYear(s string) *time.Time {
	// Extract runs of digits.
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			buf.WriteRune(r)
		}
	}
	digits := buf.String()
	if len(digits) < 4 {
		return nil
	}
	year, err := strconv.Atoi(digits[:4])
	if err != nil || year < 1400 || year > 2100 {
		return nil
	}
	t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return &t
}
