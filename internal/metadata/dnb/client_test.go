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

// marcWithISBNs carries two MARC 020 entries — a paperback ISBN-13 with a
// trailing qualifier ("(pbk.)") and an ISBN-10 with hyphens — to exercise
// stripISBNQualifier across both forms.
const marcWithISBNs = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">2222</controlfield>
  <datafield tag="020" ind1=" " ind2=" ">
    <subfield code="a">9783499015717 (pbk.)</subfield>
  </datafield>
  <datafield tag="020" ind1=" " ind2=" ">
    <subfield code="a">3-499-01571-X</subfield>
  </datafield>
  <datafield tag="100" ind1="1" ind2=" ">
    <subfield code="a">Funke, Cornelia,</subfield>
  </datafield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Tintenherz</subfield>
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

// TestGetAuthorWorks_ByForeignID verifies that GetAuthorWorks with a "dnb:"
// prefixed ID performs a num= lookup first to resolve the author name, then
// performs a per= query. The test stubs two sequential HTTP calls.
func TestGetAuthorWorks_ByForeignID(t *testing.T) {
	calls := 0
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				// First call: num= lookup returns a record with 100 $a.
				// Second call: per= query returns the same record as a "work".
				body := sruXMLN("1", marcDuneGerman)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	books, err := c.GetAuthorWorks(context.Background(), "dnb:1234567890")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 HTTP calls (num= + per=), got %d", calls)
	}
	if len(books) == 0 {
		t.Errorf("expected at least 1 book, got 0")
	}
}

// TestGetAuthorWorks_ByPlainName verifies that GetAuthorWorks with a plain
// name (no "dnb:" prefix) goes straight to a per= query (single HTTP call).
func TestGetAuthorWorks_ByPlainName(t *testing.T) {
	calls := 0
	var gotURL string
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				gotURL = r.URL.String()
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(sruXMLN("1", marcDuneGerman))),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	books, err := c.GetAuthorWorks(context.Background(), "Herbert, Frank")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call (per= only), got %d", calls)
	}
	if !strings.Contains(gotURL, "per") {
		t.Errorf("expected per= CQL query in URL, got %q", gotURL)
	}
	if len(books) == 0 {
		t.Errorf("expected at least 1 book, got 0")
	}
}

// TestGetAuthorWorks_Empty verifies that GetAuthorWorks returns an empty slice
// (not nil) when no records match.
func TestGetAuthorWorks_Empty(t *testing.T) {
	calls := 0
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(sruXMLN("0"))),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	books, err := c.GetAuthorWorks(context.Background(), "Unknown Author")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("expected 0 books, got %d", len(books))
	}
}

// TestGetAuthorWorks_ForeignID_NumLookupFails verifies that when the initial
// num= lookup fails (network error), GetAuthorWorks falls back to using the
// raw ID as the per= query term rather than returning an error.
func TestGetAuthorWorks_ForeignID_NumLookupFails(t *testing.T) {
	calls := 0
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					// Simulate a network error on the num= lookup.
					return nil, fmt.Errorf("connection refused")
				}
				// Second call (per= fallback) succeeds.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(sruXMLN("1", marcDuneGerman))),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	books, err := c.GetAuthorWorks(context.Background(), "dnb:1234567890")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (num= fail + per= fallback), got %d", calls)
	}
	if len(books) == 0 {
		t.Errorf("expected at least 1 book from fallback, got 0")
	}
}

func TestSearchBooks_ExtractsISBNsFromMARC020(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcWithISBNs), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Tintenherz")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	b := books[0]
	if len(b.Editions) != 2 {
		t.Fatalf("expected 2 editions (one per ISBN), got %d", len(b.Editions))
	}
	if b.Editions[0].ISBN13 == nil || *b.Editions[0].ISBN13 != "9783499015717" {
		t.Errorf("Editions[0].ISBN13 = %v, want 9783499015717", b.Editions[0].ISBN13)
	}
	if b.Editions[0].ISBN10 != nil {
		t.Errorf("Editions[0].ISBN10 should be nil for an ISBN-13 row, got %v", *b.Editions[0].ISBN10)
	}
	if b.Editions[1].ISBN10 == nil || *b.Editions[1].ISBN10 != "349901571X" {
		t.Errorf("Editions[1].ISBN10 = %v, want 349901571X", b.Editions[1].ISBN10)
	}
}

// marcWithGND has a 100 $0 carrying the parenthesised DE-588 form. Used to
// verify recordToBook lifts the GND authority ID into Author.ForeignID.
const marcWithGND = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">7777</controlfield>
  <datafield tag="100" ind1="1" ind2=" ">
    <subfield code="a">Funke, Cornelia,</subfield>
    <subfield code="0">(DE-588)123456789</subfield>
    <subfield code="0">(DE-101)abc</subfield>
  </datafield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Tintenherz</subfield>
  </datafield>
</record>`

// marcWithGNDURL uses the URL form of the GND authority link.
const marcWithGNDURL = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">8888</controlfield>
  <datafield tag="100" ind1="1" ind2=" ">
    <subfield code="a">Müller, Heiner,</subfield>
    <subfield code="0">http://d-nb.info/gnd/118585665</subfield>
  </datafield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Werke</subfield>
  </datafield>
</record>`

// marcSyntheticAuthor has 100 $a but no $0; recordToBook should fall back
// to a "dnb:author:<slug>" ForeignID derived from the display name.
const marcSyntheticAuthor = `<record xmlns="http://www.loc.gov/MARC21/slim">
  <controlfield tag="001">9001</controlfield>
  <datafield tag="100" ind1="1" ind2=" ">
    <subfield code="a">Müller, Thomas,</subfield>
  </datafield>
  <datafield tag="245" ind1="1" ind2="0">
    <subfield code="a">Ein Buch</subfield>
  </datafield>
</record>`

func TestExtractGND(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"(DE-588)1234567X", "1234567X"},
		{"(DE-588)118585665", "118585665"},
		{" (DE-588) 99999X ", "99999X"},
		{"http://d-nb.info/gnd/1234567X", "1234567X"},
		{"https://d-nb.info/gnd/118585665", "118585665"},
		{"http://d-nb.info/gnd/118585665/about", "118585665"},
		{"", ""},
		{"(DE-101)abc", ""}, // wrong authority code → not a GND value
		{"(ISNI)0000000123456789", ""},
		{"random nonsense", ""},
		{"http://example.com/foo/bar", ""},
	}
	for _, tc := range cases {
		got := extractGND(tc.in)
		if got != tc.want {
			t.Errorf("extractGND(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Frank Herbert", "frank-herbert"},
		{"Müller, Thomas", "muller-thomas"},
		{"Heiner Müller", "heiner-muller"},
		{"  J.R.R.   Tolkien  ", "j-r-r-tolkien"},
		{"Anonyme – Auteur", "anonyme-auteur"},
		{"García Márquez", "garcia-marquez"},
		{"", ""},
		{"---", ""},
	}
	for _, tc := range cases {
		got := slug(tc.in)
		if got != tc.want {
			t.Errorf("slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRecordToBook_AuthorForeignID_FromGND(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcWithGND), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Tintenherz")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	b := books[0]
	if b.Author == nil {
		t.Fatal("expected Author to be populated")
	}
	if b.Author.ForeignID != "dnb:gnd:123456789" {
		t.Errorf("Author.ForeignID = %q, want dnb:gnd:123456789", b.Author.ForeignID)
	}
}

func TestRecordToBook_AuthorForeignID_FromGNDURL(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcWithGNDURL), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Werke")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 || books[0].Author == nil {
		t.Fatalf("expected 1 book with author, got %+v", books)
	}
	if books[0].Author.ForeignID != "dnb:gnd:118585665" {
		t.Errorf("Author.ForeignID = %q, want dnb:gnd:118585665", books[0].Author.ForeignID)
	}
}

func TestRecordToBook_AuthorForeignID_SyntheticWhenNoAuthorityLink(t *testing.T) {
	c := mockXMLClient(sruXMLN("1", marcSyntheticAuthor), http.StatusOK)
	books, err := c.SearchBooks(context.Background(), "Müller")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 || books[0].Author == nil {
		t.Fatalf("expected 1 book with author, got %+v", books)
	}
	if books[0].Author.ForeignID != "dnb:author:thomas-muller" {
		t.Errorf("Author.ForeignID = %q, want dnb:author:thomas-muller", books[0].Author.ForeignID)
	}
}

// TestGetAuthor_SyntheticIDsReturnNil verifies that the prefix-based
// aggregator dispatch can safely route GetAuthor calls for synthetic DNB
// author IDs without producing an error — the caller will construct an
// Author from name in that path.
func TestGetAuthor_SyntheticIDsReturnNil(t *testing.T) {
	cases := []string{"dnb:gnd:118585665", "dnb:author:frank-herbert"}
	for _, id := range cases {
		got, err := New().GetAuthor(context.Background(), id)
		if err != nil {
			t.Errorf("GetAuthor(%q): unexpected error: %v", id, err)
		}
		if got != nil {
			t.Errorf("GetAuthor(%q): expected nil, got %+v", id, got)
		}
	}
}

func TestStripISBNQualifier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"9783499015717", "9783499015717"},
		{"9783499015717 (pbk.)", "9783499015717"},
		{"3-499-01571-X", "349901571X"},
		{"  9783446123456  ", "9783446123456"},
		{"978 3 446 12345 6", "978"}, // first delimiter ends the run — accepted limitation
		{"", ""},
		{"no digits here", ""},
		{"349901571x", "349901571X"}, // lowercase x normalized
	}
	for _, tc := range cases {
		if got := stripISBNQualifier(tc.in); got != tc.want {
			t.Errorf("stripISBNQualifier(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- Issue #667 regression tests --------------------------------------------

// TestStripMARCNonSortingBrackets verifies that U+0098 (NSB) and U+009C
// (NSE) — the MARC non-sorting bracket pair DNB wraps around leading
// articles like "Der"/"Die"/"Das" — are removed before titles reach the
// UI. Without this strip, the C1 control bytes pass through and render
// as garbage in the frontend (issue #667 bug 2).
func TestStripMARCNonSortingBrackets(t *testing.T) {
	const nsb = "\u0098"
	const nse = "\u009c"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no markers", "Plain title", "Plain title"},
		{"Der wrapped", nsb + "Der" + nse + " war's", "Der war's"},
		{"Die wrapped", nsb + "Die" + nse + " Verwandlung", "Die Verwandlung"},
		{"Das wrapped", nsb + "Das" + nse + " Erbe", "Das Erbe"},
		{"only NSB", nsb + "stray", "stray"},
		{"only NSE", "stray" + nse, "stray"},
		{"multiple pairs", nsb + "Der" + nse + " " + nsb + "und" + nse + " Die", "Der und Die"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMARCNonSortingBrackets(tc.in); got != tc.want {
				t.Errorf("stripMARCNonSortingBrackets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMarcClean_StripsNSBNSE confirms marcClean integrates the
// non-sorting bracket strip alongside its existing trailing-punctuation
// trim (issue #667 bug 2).
func TestMarcClean_StripsNSBNSE(t *testing.T) {
	const nsb = "\u0098"
	const nse = "\u009c"
	in := nsb + "Der" + nse + " war's :"
	want := "Der war's"
	if got := marcClean(in); got != want {
		t.Fatalf("marcClean(%q) = %q, want %q", in, got, want)
	}
}

// TestRecordToBook_TitleHasNoNonSortingBrackets is the record-level guard:
// a DNB MARC record with NSB/NSE around the leading article must produce
// a Book whose Title contains neither U+0098 nor U+009C. Live SRU sample
// for ISBN 978-3-8449-3577-6 carries exactly this shape.
func TestRecordToBook_TitleHasNoNonSortingBrackets(t *testing.T) {
	const nsb = "\u0098"
	const nse = "\u009c"
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">1305873874</controlfield>
		<datafield tag="100" ind1="1" ind2=" ">
			<subfield code="a">Zeh, Juli</subfield>
			<subfield code="4">aut</subfield>
		</datafield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">` + nsb + `Der` + nse + ` war's</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "9783844935776")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("GetBookByISBN returned nil book")
	}
	if strings.ContainsRune(book.Title, '\u0098') || strings.ContainsRune(book.Title, '\u009c') {
		t.Fatalf("Title %q still contains MARC NSB/NSE control characters", book.Title)
	}
	if !strings.Contains(book.Title, "Der war's") {
		t.Fatalf("Title %q does not contain the cleaned phrase 'Der war's'", book.Title)
	}
}

// TestRecordToBook_FallsBackTo700AUT verifies that when MARC field 100
// is absent the parser uses the first 700 entry whose $4 relator code is
// "aut" (writer). This handles DNB records for translations/audiobooks
// where the original author is catalogued only in 700 (issue #667 —
// affects several of zippoking's failing ISBNs).
func TestRecordToBook_FallsBackTo700AUT(t *testing.T) {
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">123</controlfield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">Translated work</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Translator, Max</subfield>
			<subfield code="4">trl</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="0">(DE-588)999000111</subfield>
			<subfield code="a">Author, Original</subfield>
			<subfield code="4">aut</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Narrator, Some</subfield>
			<subfield code="4">nrt</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("nil book returned")
	}
	if book.Author == nil {
		t.Fatal("Author is nil — 700 fallback did not fire")
	}
	if book.Author.Name != "Original Author" {
		t.Errorf("Author.Name = %q, want %q (700 fallback must pick the 'aut' entry, not the 'trl' or 'nrt' one)", book.Author.Name, "Original Author")
	}
	if book.Author.ForeignID != "dnb:gnd:999000111" {
		t.Errorf("Author.ForeignID = %q, want dnb:gnd:999000111", book.Author.ForeignID)
	}
}

// TestRecordToBook_FallsBackTo700AUT_GermanLabel exercises the same path
// when the relator is in $e using the German "Verfasser" label rather
// than the LoC $4 "aut" code. DNB records in older catalogue snapshots
// sometimes use the $e form without $4.
func TestRecordToBook_FallsBackTo700AUT_GermanLabel(t *testing.T) {
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">123</controlfield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">Some book</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Schmidt, Anna</subfield>
			<subfield code="e">Verfasser</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil || book.Author == nil {
		t.Fatal("Author missing")
	}
	if book.Author.Name != "Anna Schmidt" {
		t.Errorf("Author.Name = %q, want 'Anna Schmidt'", book.Author.Name)
	}
}

// TestRecordToBook_LastResort700Fallback documents the third tier of
// author extraction: when MARC 100 is missing AND no 700 entry carries
// the explicit author relator ($4=aut / $e=Verfasser), the first 700
// with a name wins. This matches DNB audiobook cataloguing convention
// where the original author can be filed as $4=ctb (contributor) —
// e.g. ISBN 9783867173544 (Harry Potter audiobook) lists J. K. Rowling
// this way. Picking the first 700 is strictly better than leaving the
// book authorless: it gives the user a clickable name to refine if it's
// wrong, instead of a silent failure.
//
// Trade-off: for anthologies catalogued without $4=aut, this could pick
// the editor instead of an author. That's a known tolerated risk; PR2
// will address author identity properly via the rewrite.
func TestRecordToBook_LastResort700Fallback(t *testing.T) {
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">123</controlfield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">Audiobook with only contributors</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="0">(DE-588)111000222</subfield>
			<subfield code="a">Primary, Contributor</subfield>
			<subfield code="4">ctb</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Secondary, Voice</subfield>
			<subfield code="4">nrt</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil || book.Author == nil {
		t.Fatal("Author should be the first 700 entry, not nil")
	}
	if book.Author.Name != "Contributor Primary" {
		t.Errorf("Author.Name = %q, want %q (first 700 with $a)", book.Author.Name, "Contributor Primary")
	}
	if book.Author.ForeignID != "dnb:gnd:111000222" {
		t.Errorf("Author.ForeignID = %q, want dnb:gnd:111000222", book.Author.ForeignID)
	}
}

// TestRecordToBook_PrefersAUTOverFirstName confirms the precedence order
// is robust: even when an $4=aut entry appears AFTER an $4=ctb entry,
// the $4=aut entry wins. Without this we'd silently regress to picking
// translators or contributors when an explicit author is available.
func TestRecordToBook_PrefersAUTOverFirstName(t *testing.T) {
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">123</controlfield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">Book with mixed contributors</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">First, Translator</subfield>
			<subfield code="4">trl</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Second, Author</subfield>
			<subfield code="4">aut</subfield>
		</datafield>
		<datafield tag="700" ind1="1" ind2=" ">
			<subfield code="a">Third, Narrator</subfield>
			<subfield code="4">nrt</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil || book == nil || book.Author == nil {
		t.Fatalf("Author missing; book=%+v err=%v", book, err)
	}
	if book.Author.Name != "Author Second" {
		t.Errorf("Author.Name = %q, want 'Author Second' ($4=aut must beat first-700 fallback)", book.Author.Name)
	}
}

// TestRecordToBook_NoAuthorWhenNo100NoAny700 covers the only remaining
// "Author == nil" case: a record with neither MARC 100 nor any 700
// entry at all. The AddBook flow's resolveAuthorForBook fallback then
// kicks in via cross-provider ISBN lookup.
func TestRecordToBook_NoAuthorWhenNo100NoAny700(t *testing.T) {
	marc := `<record xmlns="http://www.loc.gov/MARC21/slim">
		<controlfield tag="001">123</controlfield>
		<datafield tag="245" ind1="1" ind2="0">
			<subfield code="a">Orphaned title</subfield>
		</datafield>
	</record>`
	body := sruXMLN("1", marc)
	c := mockXMLClient(body, 200)
	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("nil book")
	}
	if book.Author != nil {
		t.Fatalf("Author should be nil (no 100, no 700); got %+v", book.Author)
	}
}

// TestGetAuthorWorks_SyntheticIDsReturnEmptyFast verifies that synthetic
// DNB author IDs ("dnb:gnd:*" and "dnb:author:*") short-circuit to an
// empty slice without making a network request. Before issue #667 these
// IDs were fed into the SRU num=/per= queries, which always returned
// zero hits but spent up to 15s doing so — driving the
// "book not found after author sync" error.
func TestGetAuthorWorks_SyntheticIDsReturnEmptyFast(t *testing.T) {
	hits := 0
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				hits++
				return nil, fmt.Errorf("must not make a network call for synthetic IDs")
			}),
		},
	}
	cases := []string{
		"dnb:gnd:118540238",
		"dnb:gnd:123120802",
		"dnb:author:juli-zeh",
		"dnb:author:muller-thomas",
	}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			books, err := c.GetAuthorWorks(context.Background(), id)
			if err != nil {
				t.Fatalf("GetAuthorWorks(%q) = error %v, want nil", id, err)
			}
			if len(books) != 0 {
				t.Errorf("GetAuthorWorks(%q) returned %d books, want 0", id, len(books))
			}
		})
	}
	if hits != 0 {
		t.Fatalf("GetAuthorWorks made %d network calls; synthetic IDs must be local", hits)
	}
}

// TestGetAuthorWorks_BareDNBIDStillQueriesSRU is a regression guard: the
// short-circuit for synthetic IDs must NOT swallow bare "dnb:<num>" IDs
// (control numbers), which still legitimately resolve via SRU.
func TestGetAuthorWorks_BareDNBIDStillQueriesSRU(t *testing.T) {
	hits := 0
	body := sruXMLN("0") // empty result is fine; we only check it tried
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				hits++
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	if _, err := c.GetAuthorWorks(context.Background(), "dnb:1305873874"); err != nil {
		t.Fatalf("GetAuthorWorks(bare dnb id): %v", err)
	}
	if hits == 0 {
		t.Fatal("bare dnb:<num> ID must still make at least one SRU call")
	}
}
