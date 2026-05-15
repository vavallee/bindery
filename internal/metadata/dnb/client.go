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

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	sruBase      = "https://services.dnb.de/sru/dnb"
	idPrefix     = "dnb:"
	recordSchema = "MARC21-xml"
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

// GetAuthor is largely unsupported by the DNB SRU endpoint. DNB's public SRU
// interface does not expose an authority record lookup by ID — author records
// in DNB live in a separate GND (Gemeinsame Normdatei) catalog that is not
// queryable by the same SRU endpoint. Callers that need the full author record
// must use SearchAuthors and pick the best match.
//
// However, when recordToBook synthesises an author ForeignID ("dnb:gnd:<id>"
// or "dnb:author:<slug>") the aggregator's prefix-based dispatch will route
// any subsequent GetAuthor here. We return (nil, nil) for those synthetic
// IDs so the AddBook fallback can construct an Author from name without
// the call erroring out. Real DNB control numbers ("dnb:<digits>") keep the
// original "not supported" behaviour for callers that still pass them.
func (c *Client) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	if strings.HasPrefix(foreignID, "dnb:gnd:") || strings.HasPrefix(foreignID, "dnb:author:") {
		return nil, nil
	}
	return nil, fmt.Errorf("dnb does not support author lookup by ID")
}

// GetAuthorWorks returns all books by the author identified by foreignID.
// When foreignID carries a "dnb:" prefix (e.g. "dnb:1234567890") the SRU
// num= index is used to look up the authority record's control number and
// then searches by author name.  When the foreignID looks like a plain name
// (i.e. no recognised prefix) it is used directly as a person-name query.
//
// Limitation: DNB's public SRU endpoint does not expose an author-ID →
// works relationship directly; this implementation performs a per=<name>
// query which may include works by different authors who share the same
// name.  For most use cases this is acceptable — DNB catalogue entries are
// generally unambiguous within the DACH publication space.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	// Derive a query term: if it looks like a plain name (no known prefix)
	// use it directly; otherwise try to find the name from a record lookup.
	query := authorForeignID
	if id := strings.TrimPrefix(authorForeignID, idPrefix); id != authorForeignID {
		// Had the "dnb:" prefix — look up the record to get the author name.
		records, err := c.sruQuery(ctx, "num="+id, 1)
		if err != nil || len(records) == 0 {
			// Fall back to a direct person query with the raw ID.
			query = id
		} else {
			if name := marcClean(records[0].subfield("100", "a")); name != "" {
				query = name
			} else {
				query = id
			}
		}
	}

	records, err := c.sruQuery(ctx, "per="+query, 50)
	if err != nil {
		return nil, fmt.Errorf("dnb get author works %s: %w", authorForeignID, err)
	}
	books := make([]models.Book, 0, len(records))
	for _, rec := range records {
		if b := recordToBook(rec); b != nil {
			books = append(books, *b)
		}
	}
	return books, nil
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
	req.Header.Set("User-Agent", useragent.Get())
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

	// Main entry personal name (100 $a). MARC format is "Last, First". Pair
	// it with the linked-authority identifier from $0 (GND) when present, or
	// synthesise a stable name-slug ID otherwise so the aggregator's "drop
	// results without an author ForeignID" guard does not silently swallow
	// the DNB result.
	if marcName := marcClean(rec.subfield("100", "a")); marcName != "" {
		displayName := invertMARCName(marcName)
		foreignID := ""
		for _, raw := range rec.subfieldAll("100", "0") {
			if gnd := extractGND(raw); gnd != "" {
				foreignID = "dnb:gnd:" + gnd
				break
			}
		}
		if foreignID == "" {
			foreignID = "dnb:author:" + slug(displayName)
		}
		b.Author = &models.Author{
			ForeignID:        foreignID,
			Name:             displayName,
			SortName:         marcName, // already in "Last, First" sort form
			MetadataProvider: "dnb",
		}
	}

	// ISBN(s) from MARC 020 $a. The field can repeat once per edition format
	// (paperback / hardcover / ebook), and the value typically contains a
	// qualifier in parentheses ("9783499015717 (pbk)"). Extract every digit
	// run, classify into ISBN-13 / ISBN-10, and surface as Editions so the
	// add-book endpoint can use them for cross-provider author resolution
	// when the DNB record has only an author name and no foreign author ID.
	for _, raw := range rec.subfieldAll("020", "a") {
		digits := stripISBNQualifier(raw)
		if digits == "" {
			continue
		}
		ed := models.Edition{}
		switch len(digits) {
		case 13:
			ed.ISBN13 = &digits
		case 10:
			ed.ISBN10 = &digits
		default:
			continue
		}
		b.Editions = append(b.Editions, ed)
	}

	return b
}

// stripISBNQualifier extracts the digit-run from a MARC 020 $a value. Real
// values look like "9783499015717", "3-499-01571-X", or
// "9783499015717 (pbk.)" — keep the digits (and a trailing X for ISBN-10
// check digits), drop everything else.
func stripISBNQualifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == 'X' || r == 'x' {
			// ISBN-10 check digit can be 'X' (value 10).
			b.WriteRune('X')
		} else if b.Len() > 0 && (r == ' ' || r == '(' || r == ',') {
			// Stop at the first delimiter after digits — qualifier text follows.
			break
		}
	}
	return b.String()
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

// extractGND returns the bare GND (Gemeinsame Normdatei) identifier from a
// MARC 100 $0 authority-link value. DNB records use two conventional forms:
//
//   - "(DE-588)1234567X"     — parenthesised authority-source prefix
//   - "http://d-nb.info/gnd/1234567X" — URL form
//
// Both encode the same authority record. Returns "" when neither form is
// present (e.g. an empty $0, or a non-GND authority like ISNI/VIAF).
func extractGND(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// URL form takes precedence: "…/gnd/<id>". We accept any case for the
	// path segment to tolerate "GND" or trailing path noise.
	if idx := strings.Index(strings.ToLower(raw), "/gnd/"); idx != -1 {
		rest := raw[idx+len("/gnd/"):]
		return trimGNDID(rest)
	}
	// Parenthesised form: "(DE-588)<id>" or "(DE-101) <id>" with whitespace.
	if strings.HasPrefix(raw, "(") {
		if end := strings.IndexByte(raw, ')'); end != -1 {
			authority := strings.TrimSpace(raw[1:end])
			if strings.EqualFold(authority, "DE-588") {
				return trimGNDID(strings.TrimSpace(raw[end+1:]))
			}
		}
	}
	return ""
}

// trimGNDID strips trailing slashes / whitespace / non-ID punctuation from a
// candidate GND identifier. GND IDs are 9–10 chars: digits with an optional
// trailing check character (digit or 'X').
func trimGNDID(s string) string {
	s = strings.TrimSpace(s)
	// Stop at first path/query/fragment delimiter — guards against trailing
	// path noise on URL-form values.
	for _, sep := range []string{"/", "?", "#", " "} {
		if idx := strings.Index(s, sep); idx != -1 {
			s = s[:idx]
		}
	}
	return s
}

// slug returns a lowercase ASCII-folded version of name with any run of
// non-alphanumeric characters collapsed to "-". Leading/trailing hyphens are
// trimmed. Used to synthesise a stable author ForeignID when the DNB record
// has no GND authority link (so the aggregator can persist + dedupe DNB-only
// authors).
//
// Example: "Müller, Thomas" → "muller-thomas".
func slug(name string) string {
	folded := norm.NFD.String(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(folded))
	prevDash := false
	for _, r := range folded {
		switch {
		case unicode.Is(unicode.Mn, r):
			// Combining mark from NFD decomposition — drop, keeping the base letter.
			continue
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevDash = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "-")
	return out
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
