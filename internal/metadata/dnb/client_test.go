package dnb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- Test transport helpers ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockXMLClient(body string, status int) *Client {
	return &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
}

// sruXMLN wraps MARC21-XML records in a minimal SRU searchRetrieveResponse.
// n is the numberOfRecords string (e.g. "1", "0").
func sruXMLN(n string, marcRecords ...string) string {
	var recs strings.Builder
	for _, r := range marcRecords {
		recs.WriteString(`<record><recordData>`)
		recs.WriteString(r)
		recs.WriteString(`</recordData></record>`)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<searchRetrieveResponse xmlns="http://www.loc.gov/zing/srw/">
  <version>1.1</version>
  <numberOfRecords>` + n + `</numberOfRecords>
  <records>` + recs.String() + `</records>
</searchRetrieveResponse>`
}

// marcRec builds a minimal MARC21-XML record string.
const marcDuneGerman = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">1234567890</controlfield>
  <datafield tag="100" ind1="1" ind2=" ">
    <subfield code="a">Herbert, Frank,</subfield>
    <subfield code="e">Verfasser</subfield>
  </datafield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Der Wüstenplanet :</subfield>
    <subfield code="b">Roman /</subfield>
    <subfield code="c">Frank Herbert</subfield>
  </datafield>
  <datafield tag="264" ind1=" " ind2="1">
    <subfield code="a">München</subfield>
    <subfield code="b">Heyne</subfield>
    <subfield code="c">2019</subfield>
  </datafield>
  <datafield tag="041" ind1="0" ind2=" ">
    <subfield code="a">ger</subfield>
  </datafield>
  <datafield tag="520" ind1=" " ind2=" ">
    <subfield code="a">Science-Fiction-Roman über den Wüstenplaneten Arrakis.</subfield>
  </datafield>
</record>`

const marcNoTitle = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">9999</controlfield>
</record>`

// marcNoID has a title but no 001 controlfield, so recordToBook returns nil.
const marcNoID = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="003">DE-101</controlfield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Title Without ID</subfield>
  </datafield>
</record>`

// --- Tests ---

func TestName(t *testing.T) {
	if New().Name() != "dnb" {
		t.Errorf("Name() = %q, want %q", New().Name(), "dnb")
	}
}

func TestSearchBooks_Success(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcDuneGerman), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Wüstenplanet")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	b := books[0]
	if b.ForeignID != "dnb:1234567890" {
		t.Errorf("ForeignID = %q, want dnb:1234567890", b.ForeignID)
	}
	if b.Title != "Der Wüstenplanet: Roman" {
		t.Errorf("Title = %q, want 'Der Wüstenplanet: Roman'", b.Title)
	}
	if b.Language != "ger" {
		t.Errorf("Language = %q, want ger", b.Language)
	}
	if b.Description == "" {
		t.Error("Description should be populated")
	}
	if b.ReleaseDate == nil || b.ReleaseDate.Year() != 2019 {
		t.Errorf("ReleaseDate year = %v, want 2019", b.ReleaseDate)
	}
	if b.Author == nil {
		t.Fatal("Author should be non-nil")
	}
	if b.Author.Name != "Frank Herbert" {
		t.Errorf("Author.Name = %q, want 'Frank Herbert'", b.Author.Name)
	}
	if b.Author.SortName != "Herbert, Frank" {
		t.Errorf("Author.SortName = %q, want 'Herbert, Frank'", b.Author.SortName)
	}
}

func TestSearchBooks_Empty(t *testing.T) {
	c := mockXMLClient(sruXMLN("0"), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "doesnotexist")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("expected 0 books, got %d", len(books))
	}
}

func TestSearchBooks_HTTPError(t *testing.T) {
	c := mockXMLClient("rate limited", http.StatusTooManyRequests)
	_, err := c.SearchBooks(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 429")
	}
}

func TestSearchBooks_SkipsRecordWithoutTitle(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcNoTitle), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "whatever")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("expected 0 books (record has no title), got %d", len(books))
	}
}

// TestSearchBooks_SkipsRecordWithoutID exercises the controlField miss path:
// a record with a controlfield of a different tag (003) but no 001 should be
// skipped because recordToBook returns nil when id is empty.
func TestSearchBooks_SkipsRecordWithoutID(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcNoID), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "whatever")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("expected 0 books (record has no 001 ID), got %d", len(books))
	}
}

// TestSearchBooks_NetworkError verifies that a transport-level error is
// surfaced as an error rather than silently returning empty results.
func TestSearchBooks_NetworkError(t *testing.T) {
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("connection refused")
			}),
		},
	}
	_, err := c.SearchBooks(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on network failure")
	}
}

func TestSearchAuthors_DeduplicatesNames(t *testing.T) {
	// Two records with the same author should produce a single Author entry.
	c := mockXMLClient(sruXMLN("2", marcDuneGerman, marcDuneGerman), http.StatusOK)
	authors, err := c.SearchAuthors(context.Background(), "Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 1 {
		t.Errorf("expected 1 author (deduped), got %d", len(authors))
	}
	if authors[0].Name != "Frank Herbert" {
		t.Errorf("Author.Name = %q, want 'Frank Herbert'", authors[0].Name)
	}
}

func TestGetAuthor_Unsupported(t *testing.T) {
	_, err := New().GetAuthor(context.Background(), "dnb:123")
	if err == nil {
		t.Fatal("expected error: DNB does not support author lookup by ID")
	}
}

func TestGetBook_Success(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcDuneGerman), http.StatusOK)
	book, err := c.GetBook(context.Background(), "dnb:1234567890")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.ForeignID != "dnb:1234567890" {
		t.Errorf("ForeignID = %q, want dnb:1234567890", book.ForeignID)
	}
}

func TestGetBook_NotFound(t *testing.T) {
	c := mockXMLClient(sruXMLN("0"), http.StatusOK)
	book, err := c.GetBook(context.Background(), "dnb:0000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book != nil {
		t.Errorf("expected nil book for missing record, got %+v", book)
	}
}

func TestGetEditions_ReturnsNil(t *testing.T) {
	editions, err := New().GetEditions(context.Background(), "dnb:123")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if editions != nil {
		t.Errorf("expected nil, got %v", editions)
	}
}

func TestGetBookByISBN_Found(t *testing.T) {
	var gotURL string
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotURL = r.URL.String()
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(sruXMLN("1", marcDuneGerman))),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	book, err := c.GetBookByISBN(context.Background(), "9783453198975")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if !strings.Contains(gotURL, "isbn") {
		t.Errorf("expected 'isbn' in SRU query URL, got %q", gotURL)
	}
}

func TestGetBookByISBN_NotFound(t *testing.T) {
	c := mockXMLClient(sruXMLN("0"), http.StatusOK)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book != nil {
		t.Errorf("expected nil for missing ISBN")
	}
}

// --- Unit tests for helpers ---

func TestMARCClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Der Titel /", "Der Titel"},
		{"Der Titel :", "Der Titel"},
		{"Müller, Thomas,", "Müller, Thomas"},
		{"  Spaces  ", "Spaces"},
		{"", ""},
		{"Trailing.", "Trailing"},
		{"No trailing", "No trailing"},
	}
	for _, tc := range cases {
		got := marcClean(tc.in)
		if got != tc.want {
			t.Errorf("marcClean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInvertMARCName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Herbert, Frank", "Frank Herbert"},
		{"Bach, Johann Sebastian", "Johann Sebastian Bach"},
		{"García Márquez, Gabriel", "Gabriel García Márquez"},
		{"Madonna", "Madonna"},   // no comma → unchanged
		{"Last", "Last"},         // no comma → unchanged
		{"Surname, ", "Surname"}, // comma present but no first name after trim
		{"", ""},
	}
	for _, tc := range cases {
		got := invertMARCName(tc.in)
		if got != tc.want {
			t.Errorf("invertMARCName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseYear(t *testing.T) {
	cases := []struct {
		in   string
		want int // 0 = expect nil
	}{
		{"2023", 2023},
		{"[2019]", 2019},
		{"c2020", 2020},
		{"", 0},
		{"not a year", 0},
		{"99", 0}, // too short
	}
	for _, tc := range cases {
		got := parseYear(tc.in)
		if tc.want == 0 {
			if got != nil {
				t.Errorf("parseYear(%q) = %v, want nil", tc.in, got)
			}
		} else {
			if got == nil {
				t.Errorf("parseYear(%q) = nil, want %d", tc.in, tc.want)
			} else if got.Year() != tc.want {
				t.Errorf("parseYear(%q) year = %d, want %d", tc.in, got.Year(), tc.want)
			}
		}
	}
}

// TestSearchBooks_NoAuthorField verifies that records without a 100 $a are
// still returned as books (Author field is nil).
const marcNoAuthor = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">5555</controlfield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Anonymous Work</subfield>
  </datafield>
</record>`

func TestSearchBooks_NoAuthorField(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcNoAuthor), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Anonymous")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if books[0].Author != nil {
		t.Errorf("Author should be nil when 100 $a is absent, got %+v", books[0].Author)
	}
}

// TestSearchBooks_LegacyYearField verifies the 260 $c fallback for the
// publication year when the modern 264 $c is absent.
const marcLegacyYear = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">6666</controlfield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Altes Buch</subfield>
  </datafield>
  <datafield tag="260" ind1=" " ind2=" ">
    <subfield code="a">Berlin</subfield>
    <subfield code="b">Verlag</subfield>
    <subfield code="c">1985</subfield>
  </datafield>
</record>`

func TestSearchBooks_LegacyYearField(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcLegacyYear), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Altes")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if books[0].ReleaseDate == nil || books[0].ReleaseDate.Year() != 1985 {
		t.Errorf("ReleaseDate year = %v, want 1985 (from 260 $c)", books[0].ReleaseDate)
	}
}

// TestSearchAuthors_FiltersNoName verifies that records without a 100 $a
// are silently skipped (recordToAuthor returns nil) rather than causing a
// panic or producing an empty-named author.
func TestSearchAuthors_FiltersNoName(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcNoAuthor), http.StatusOK)
	authors, err := c.SearchAuthors(context.Background(), "Anonymous")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 0 {
		t.Errorf("expected 0 authors for record with no 100 $a, got %d", len(authors))
	}
}

// TestSRUQuery_XMLDecodeError verifies that a malformed XML response is
// surfaced as an error rather than silently producing zero records.
func TestSRUQuery_XMLDecodeError(t *testing.T) {
	c := mockXMLClient("not xml at all <<<", http.StatusOK)
	_, err := c.SearchBooks(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on malformed XML response")
	}
}

func TestSRUQueryBuildsCorrectURL(t *testing.T) {
	var gotURL string
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotURL = r.URL.String()
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(sruXMLN("0"))),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	_, _ = c.SearchBooks(context.Background(), "Test Title")
	if !strings.Contains(gotURL, "operation=searchRetrieve") {
		t.Errorf("URL missing operation param: %q", gotURL)
	}
	if !strings.Contains(gotURL, "MARC21-xml") {
		t.Errorf("URL missing recordSchema param: %q", gotURL)
	}
	if !strings.Contains(gotURL, "tit") {
		t.Errorf("URL missing CQL title field: %q", gotURL)
	}
}
