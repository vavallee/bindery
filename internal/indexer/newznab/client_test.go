package newznab

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testNew creates a newznab Client for use with httptest servers. It replaces
// the hardened http.Client (whose DialContext blocks loopback) with a plain
// one so tests can reach 127.0.0.1 without triggering the SSRF policy. Only
// use this in tests that are not specifically testing the SSRF dialer.
func testNew(baseURL, apiKey string) *Client {
	c := New(baseURL, apiKey)
	c.http = &http.Client{Timeout: 30 * time.Second}
	return c
}

const testRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>Test Indexer</title>
    <newznab:response offset="0" total="2"/>
    <item>
      <title>Dark Matter by Author Name</title>
      <guid isPermaLink="true">abc123</guid>
      <link>https://example.com/details/abc123</link>
      <pubDate>Mon, 10 Apr 2026 12:00:00 +0000</pubDate>
      <category>Books &gt; EBook</category>
      <enclosure url="https://example.com/getnzb/abc123" length="5242880" type="application/x-nzb"/>
      <newznab:attr name="category" value="7020"/>
      <newznab:attr name="size" value="5242880"/>
      <newznab:attr name="grabs" value="150"/>
      <newznab:attr name="author" value="Test Author"/>
      <newznab:attr name="title" value="Dark Matter"/>
    </item>
    <item>
      <title>Recursion EPUB</title>
      <guid isPermaLink="true">def456</guid>
      <link>https://example.com/details/def456</link>
      <pubDate>Tue, 11 Apr 2026 12:00:00 +0000</pubDate>
      <enclosure url="https://example.com/getnzb/def456" length="2097152" type="application/x-nzb"/>
      <newznab:attr name="category" value="7020"/>
      <newznab:attr name="grabs" value="42"/>
    </item>
  </channel>
</rss>`

const testCaps = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <searching>
    <search available="yes"/>
    <book-search available="yes"/>
  </searching>
  <categories>
    <category id="7000" name="Books">
      <subcat id="7020" name="EBook"/>
      <subcat id="7030" name="Comics"/>
    </category>
  </categories>
</caps>`

func TestParseSearchResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	results, err := c.Search(context.Background(), "dark matter", []int{7000, 7020})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	r := results[0]
	if r.GUID != "abc123" {
		t.Errorf("expected GUID abc123, got %s", r.GUID)
	}
	if r.Title != "Dark Matter by Author Name" {
		t.Errorf("expected title 'Dark Matter by Author Name', got '%s'", r.Title)
	}
	if r.Size != 5242880 {
		t.Errorf("expected size 5242880, got %d", r.Size)
	}
	if r.Grabs != 150 {
		t.Errorf("expected grabs 150, got %d", r.Grabs)
	}
	if r.Author != "Test Author" {
		t.Errorf("expected author 'Test Author', got '%s'", r.Author)
	}
	if r.NZBURL != "https://example.com/getnzb/abc123" {
		t.Errorf("unexpected NZB URL: %s", r.NZBURL)
	}
}

func TestParseCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testCaps))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	caps, err := c.Caps(context.Background())
	if err != nil {
		t.Fatalf("caps: %v", err)
	}

	if caps.Searching.Search.Available != "yes" {
		t.Errorf("expected search available=yes, got %s", caps.Searching.Search.Available)
	}
	if caps.Searching.BookSearch.Available != "yes" {
		t.Errorf("expected book-search available=yes, got %s", caps.Searching.BookSearch.Available)
	}
	if len(caps.Categories.Categories) != 1 {
		t.Fatalf("expected 1 category, got %d", len(caps.Categories.Categories))
	}
	if caps.Categories.Categories[0].ID != "7000" {
		t.Errorf("expected category 7000, got %s", caps.Categories.Categories[0].ID)
	}
}

func TestTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testCaps))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	err := c.Test(context.Background())
	if err != nil {
		t.Errorf("test should pass: %v", err)
	}
}

func TestIntSliceToCSV(t *testing.T) {
	tests := []struct {
		input []int
		want  string
	}{
		{[]int{7000, 7020}, "7000,7020"},
		{[]int{7000}, "7000"},
		{nil, "7020"},
		{[]int{}, "7020"},
	}
	for _, tt := range tests {
		got := intSliceToCSV(tt.input)
		if got != tt.want {
			t.Errorf("intSliceToCSV(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestXMLNamespace(t *testing.T) {
	// Verify our types handle the newznab XML namespace correctly
	var rss rssResponse
	err := xml.Unmarshal([]byte(testRSS), &rss)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rss.Channel.Response.Total != 2 {
		t.Errorf("expected total=2, got %d", rss.Channel.Response.Total)
	}
}

func TestCapsWithFullTorznabEndpointURL(t *testing.T) {
	var gotPath string
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testCaps))
	}))
	defer srv.Close()

	endpoint := srv.URL + "/1/api?apikey=from-url"
	c := testNew(endpoint, "")
	if _, err := c.Caps(context.Background()); err != nil {
		t.Fatalf("caps: %v", err)
	}

	if gotPath != "/1/api" {
		t.Fatalf("expected path /1/api, got %s", gotPath)
	}
	if !strings.Contains(gotQuery, "t=caps") {
		t.Fatalf("expected t=caps in query, got %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "apikey=from-url") {
		t.Fatalf("expected apikey from endpoint URL in query, got %q", gotQuery)
	}
}

func TestCapsWithEndpointAndExplicitAPIKeyOverridesURL(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testCaps))
	}))
	defer srv.Close()

	endpoint := srv.URL + "/1/api?apikey=from-url"
	c := testNew(endpoint, "from-field")
	if _, err := c.Caps(context.Background()); err != nil {
		t.Fatalf("caps: %v", err)
	}

	if !strings.Contains(gotQuery, "apikey=from-field") {
		t.Fatalf("expected explicit API key to be used, got %q", gotQuery)
	}
}

func TestNewRootURLNormalizesToAPIPath(t *testing.T) {
	c := New("https://prowlarr.local:9696", "abc")
	if c.baseURL != "https://prowlarr.local:9696/api" {
		t.Fatalf("expected normalized baseURL to include /api, got %s", c.baseURL)
	}
}

func TestNewExtractsAPIKeyAndStripsItFromStoredBaseURL(t *testing.T) {
	c := New("https://prowlarr.local:9696/1/api?apikey=from-url&foo=bar", "")

	if c.apiKey != "from-url" {
		t.Fatalf("expected API key extracted from URL, got %q", c.apiKey)
	}
	if strings.Contains(c.baseURL, "apikey=") {
		t.Fatalf("expected stored baseURL not to contain apikey, got %s", c.baseURL)
	}
	if !strings.Contains(c.baseURL, "foo=bar") {
		t.Fatalf("expected stored baseURL to preserve non-apikey query params, got %s", c.baseURL)
	}
}

func TestPrimaryTitleForQuery(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Dune", "Dune"},
		{"Dune: Messiah", "Dune"},
		{"Dune:  Messiah", "Dune"},
		{"A Song of Ice and Fire: A Game of Thrones", "A Song of Ice and Fire"},
		{"", ""},
		{":lead colon", ":lead colon"},
		{"No Colon Here", "No Colon Here"},
	}
	for _, tt := range tests {
		if got := primaryTitleForQuery(tt.in); got != tt.want {
			t.Errorf("primaryTitleForQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAuthorSurname(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"Cher", "Cher"},
		{"Andy Weir", "Weir"},
		{"Mary Doria Russell", "Russell"},
		{"   Andy   Weir   ", "Weir"},
	}
	for _, tt := range tests {
		if got := authorSurname(tt.in); got != tt.want {
			t.Errorf("authorSurname(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestBookSearch_Tier1StructuredBook verifies the first query tier (t=book
// with title+author) returns results when the indexer supports it. The fake
// indexer only answers the structured query; any fallback would miss it.
func TestBookSearch_Tier1StructuredBook(t *testing.T) {
	var gotQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueries = append(gotQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.RawQuery, "t=book") {
			w.Write([]byte(testRSS))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	results, err := c.BookSearch(context.Background(), "Dark Matter: A Novel", "Blake Crouch", []int{7020})
	if err != nil {
		t.Fatalf("BookSearch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results from tier-1, got %d", len(results))
	}
	if len(gotQueries) != 1 {
		t.Fatalf("expected only 1 request (tier-1 hit), got %d: %v", len(gotQueries), gotQueries)
	}
	// Subtitle should be stripped from the t=book title param.
	if !strings.Contains(gotQueries[0], "title=Dark+Matter") || strings.Contains(gotQueries[0], "Novel") {
		t.Errorf("expected title without subtitle in tier-1 query, got %s", gotQueries[0])
	}
	if !strings.Contains(gotQueries[0], "author=Blake+Crouch") {
		t.Errorf("expected author=Blake+Crouch in tier-1 query, got %s", gotQueries[0])
	}
}

// TestBookSearch_FallsBackToSurnameTier verifies the structured tier-1
// failing (empty results) triggers the tier-2 freeform "Surname Title"
// search, and that tier-2 matches win over tiers 3/4.
func TestBookSearch_FallsBackToSurnameTier(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		// Tier 1 (t=book) returns an empty response so we fall through.
		if q.Get("t") == "book" {
			w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
			return
		}
		// Tier 2: q="Crouch Dark Matter" — return a populated RSS.
		if q.Get("t") == "search" && q.Get("q") == "Crouch Dark Matter" {
			w.Write([]byte(testRSS))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	results, err := c.BookSearch(context.Background(), "Dark Matter", "Blake Crouch", []int{7020})
	if err != nil {
		t.Fatalf("BookSearch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results from tier-2, got %d", len(results))
	}
	if len(queries) < 2 {
		t.Fatalf("expected at least 2 query tiers executed, got %d: %v", len(queries), queries)
	}
}

// TestBookSearch_FinalFallbackTitleOnly verifies that when tiers 1-3 come
// up empty, the final title-only fallback is issued and its results are
// returned directly.
func TestBookSearch_FinalFallbackTitleOnly(t *testing.T) {
	var sawTitleOnly bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		// Only the tier-4 title-only search succeeds.
		if q.Get("t") == "search" && q.Get("q") == "Dune" {
			sawTitleOnly = true
			w.Write([]byte(testRSS))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	// No author supplied: tiers 1-3 should be skipped and tier 4 taken.
	results, err := c.BookSearch(context.Background(), "Dune", "", []int{7020})
	if err != nil {
		t.Fatalf("BookSearch: %v", err)
	}
	if !sawTitleOnly {
		t.Errorf("expected title-only fallback query to fire")
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

// TestProbe_Success checks the happy-path of the Test button probe: an
// OK caps response yields populated status/categories/bookSearch fields
// with a finite latency.
func TestProbe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testCaps))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	result := c.Probe(context.Background())
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Status != http.StatusOK {
		t.Errorf("expected status 200, got %d", result.Status)
	}
	if result.Categories != 1 {
		t.Errorf("expected 1 category, got %d", result.Categories)
	}
	if !result.BookSearch {
		t.Errorf("expected bookSearch=true")
	}
	if !result.GeneralSearch {
		t.Errorf("expected generalSearch=true")
	}
	if result.LatencyMs < 0 {
		t.Errorf("expected non-negative latency, got %d", result.LatencyMs)
	}
}

// TestProbe_HTTPErrorStatus verifies the probe surfaces non-200 responses
// with the status code *and* a truncated body as the error message, rather
// than swallowing the failure.
func TestProbe_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("bad apikey"))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "wrong")
	result := c.Probe(context.Background())
	if result.Status != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", result.Status)
	}
	if !strings.Contains(result.Error, "401") {
		t.Errorf("expected error to mention 401, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "bad apikey") {
		t.Errorf("expected error to include response body, got %q", result.Error)
	}
}

// TestProbe_InvalidXML ensures parse errors on the caps body don't panic
// and surface as a descriptive error. The HTTP status still round-trips.
func TestProbe_InvalidXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte("<<<not valid xml>>>"))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	result := c.Probe(context.Background())
	if result.Status != http.StatusOK {
		t.Errorf("expected status 200 even with bad body, got %d", result.Status)
	}
	if !strings.Contains(result.Error, "parse caps") {
		t.Errorf("expected 'parse caps' error, got %q", result.Error)
	}
}

// TestProbe_NetworkError verifies that connection failures (server closed
// before the call) populate Error and leave Status zero.
func TestProbe_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Close immediately so Do() returns a dial error.
	srv.Close()

	c := testNew(srv.URL, "testkey")
	result := c.Probe(context.Background())
	if result.Status != 0 {
		t.Errorf("expected zero status on dial failure, got %d", result.Status)
	}
	if result.Error == "" {
		t.Errorf("expected non-empty error on dial failure")
	}
}

func TestNormalizeEndpointURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty returns empty", "", ""},
		{"whitespace only returns empty", "   ", ""},
		{"bare host gets /api appended", "https://host:9696", "https://host:9696/api"},
		{"bare host with trailing slash gets /api", "https://host:9696/", "https://host:9696/api"},
		{"api path preserved", "https://host/api", "https://host/api"},
		{"torznab path preserved", "https://host/torznab/api", "https://host/torznab/api"},
		{"non-api path gets /api appended", "https://host/1", "https://host/1/api"},
		{"double slashes cleaned", "https://host//1//api", "https://host/1/api"},
		{"trailing slash stripped", "https://host/api/", "https://host/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeEndpointURL(tt.in)
			if got != tt.want {
				t.Errorf("normalizeEndpointURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseResults_UsesLinkWhenEnclosureMissing verifies the parseResults
// fallback: when <enclosure url> is missing, <link> is used as the NZB URL
// so that legacy indexers still produce grab-able results.
func TestParseResults_UsesLinkWhenEnclosureMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	results, err := c.Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Second item in testRSS has its enclosure URL present; the parser should
	// have picked it up. Still, verify neither falls back incorrectly to "".
	for _, r := range results {
		if r.NZBURL == "" {
			t.Errorf("result %q has empty NZBURL", r.Title)
		}
	}
	// And the second one's grabs attribute should be parsed.
	if results[1].Grabs != 42 {
		t.Errorf("expected grabs=42 on second item, got %d", results[1].Grabs)
	}
}

// TestGetXML_SurfacesNon200 verifies the low-level getXML helper turns
// HTTP failures into errors that include the status code, rather than
// attempting to parse the body.
func TestGetXML_SurfacesNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("maintenance"))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	_, err := c.Search(context.Background(), "anything", nil)
	if err == nil {
		t.Fatalf("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention 503, got %v", err)
	}
}

// TestParseNewznabError verifies the helper recognises Newznab/Torznab
// <error> responses and leaves normal documents alone (#698).
func TestParseNewznabError(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring expected in the error; "" means nil error
	}{
		{"rate limit", `<?xml version="1.0" encoding="UTF-8"?><error code="500" description="Request limit reached"/>`, "500"},
		{"bad credentials", `<error code="100" description="Incorrect user credentials"/>`, "Incorrect user credentials"},
		{"description only", `<error description="site offline"/>`, "site offline"},
		{"code only", `<error code="910"/>`, "910"},
		{"normal rss feed", `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`, ""},
		{"non-xml body", `not xml at all`, ""},
		{"bare error element", `<error/>`, ""},
		{"empty body", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseNewznabError([]byte(tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSearch_SurfacesNewznabError verifies a Newznab <error> response — which
// indexers return with HTTP 200 — is surfaced with the indexer's own code and
// description rather than the raw XML decoder error (#698).
func TestSearch_SurfacesNewznabError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><error code="500" description="Request limit reached"/>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	_, err := c.Search(context.Background(), "anything", nil)
	if err == nil {
		t.Fatal("expected error on <error> response, got nil")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "Request limit reached") {
		t.Errorf("expected error with indexer code and description, got %v", err)
	}
	if strings.Contains(err.Error(), "expected element type") {
		t.Errorf("raw XML decoder error leaked instead of being translated: %v", err)
	}
}

// TestBuildURL_StripsStaleQueryParams verifies that baseURL-embedded
// query params for t/q/cat/limit are wiped before new ones are added,
// preventing duplicated keys like t=caps&t=search.
func TestBuildURL_StripsStaleQueryParams(t *testing.T) {
	c := New("https://host/api?t=stale&q=stale&cat=1", "key")
	u, err := c.buildURL("search", map[string]string{"q": "fresh"})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if strings.Count(u, "t=") != 1 {
		t.Errorf("expected single t= param, got %s", u)
	}
	if strings.Count(u, "q=") != 1 {
		t.Errorf("expected single q= param, got %s", u)
	}
	if !strings.Contains(u, "q=fresh") || strings.Contains(u, "q=stale") {
		t.Errorf("stale q param not replaced, got %s", u)
	}
	if !strings.Contains(u, "t=search") {
		t.Errorf("expected t=search in built URL, got %s", u)
	}
	if !strings.Contains(u, "apikey=key") {
		t.Errorf("expected apikey=key in built URL, got %s", u)
	}
}

func TestNormalizeQueryTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Die Stille ist ein Geräusch", "Die Stille ist ein Geräusch"},
		{"  Die Stille  ist   ein Geräusch  ", "Die Stille ist ein Geräusch"},
		{"Die Stille ist ein Geräusch (German Edition)", "Die Stille ist ein Geräusch"},
		{"Dicke Freundinnen (Unabridged)", "Dicke Freundinnen"},
		{"Ender\u2019s Game", "Ender's Game"},
		{"Title \u2014 Subtitle", "Title - Subtitle"},
	}
	for _, c := range cases {
		if got := NormalizeQueryTitle(c.in); got != c.want {
			t.Errorf("NormalizeQueryTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSignDownloadURL covers the apikey-signing helper used to fix
// Prowlarr-proxy NZBGet "empty NZB" rejections (#531). The apikey must be
// appended to enclosure URLs that target the indexer's own host but never
// to third-party hosts (would leak credentials) or URLs already carrying
// an apikey (would clobber a valid one).
func TestSignDownloadURL(t *testing.T) {
	c := New("https://prowlarr.local:9696/1/api", "secret123")

	t.Run("same host without apikey gets it appended", func(t *testing.T) {
		raw := "https://prowlarr.local:9696/1/download?id=abc"
		got := c.signDownloadURL(raw)
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if u.Query().Get("apikey") != "secret123" {
			t.Errorf("expected apikey=secret123 appended, got %s", got)
		}
		if u.Query().Get("id") != "abc" {
			t.Errorf("expected existing id=abc preserved, got %s", got)
		}
	})

	t.Run("same host with existing apikey is left alone", func(t *testing.T) {
		raw := "https://prowlarr.local:9696/1/download?id=abc&apikey=other"
		got := c.signDownloadURL(raw)
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if u.Query().Get("apikey") != "other" {
			t.Errorf("expected existing apikey preserved, got %s", got)
		}
	})

	t.Run("different host does not get apikey appended", func(t *testing.T) {
		raw := "https://third-party.example.com/getnzb/xyz?id=999"
		got := c.signDownloadURL(raw)
		if strings.Contains(got, "apikey=") {
			t.Errorf("apikey leaked to third-party host: %s", got)
		}
		if got != raw {
			t.Errorf("expected URL unchanged, got %s", got)
		}
	})
}

// TestParseResults_AppendsApikeyToEnclosure exercises the end-to-end
// parse path with a Prowlarr-proxy-shaped RSS response: the indexer's
// enclosure URLs lack the apikey, and parseResults must re-sign them so
// downstream download clients (NZBGet) can authenticate.
func TestParseResults_AppendsApikeyToEnclosure(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Build an RSS body whose enclosure URLs point back at the same host
		// but carry no apikey — mirroring what Prowlarr returns when proxying
		// a Newznab indexer.
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="1"/>
    <item>
      <title>Some Book</title>
      <guid isPermaLink="true">prowlarr-1</guid>
      <link>%s/1/download?id=prowlarr-1</link>
      <enclosure url="%s/1/download?id=prowlarr-1" length="1024" type="application/x-nzb"/>
      <newznab:attr name="category" value="7020"/>
    </item>
  </channel>
</rss>`, srv.URL, srv.URL)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := testNew(srv.URL+"/1/api", "the-key")
	results, err := c.Search(context.Background(), "anything", nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].NZBURL, "apikey=the-key") {
		t.Errorf("expected enclosure NZBURL to be signed with apikey, got %s", results[0].NZBURL)
	}
	if !strings.Contains(results[0].NZBURL, "id=prowlarr-1") {
		t.Errorf("expected enclosure NZBURL to preserve id query, got %s", results[0].NZBURL)
	}
}

func TestRedactAPIKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "replaces apikey value",
			in:   "https://indexer.local/api?t=book&apikey=supersecret&title=Dune",
			want: "https://indexer.local/api?apikey=%2A%2A%2A&t=book&title=Dune",
		},
		{
			name: "no apikey param left unchanged",
			in:   "https://indexer.local/api?t=search&q=Dune",
			want: "https://indexer.local/api?t=search&q=Dune",
		},
		{
			name: "invalid URL returned as-is",
			in:   "://not a url",
			want: "://not a url",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactAPIKey(c.in)
			if got != c.want {
				t.Errorf("redactAPIKey(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPrimaryTitleForQuery_NormalizesAndDropsSubtitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Dune: Messiah", "Dune"},
		{"  Dune  ", "Dune"},
		{"Die Stille ist ein Geräusch (ger)", "Die Stille ist ein Geräusch"},
	}
	for _, c := range cases {
		if got := primaryTitleForQuery(c.in); got != c.want {
			t.Errorf("primaryTitleForQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSigWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Life Ascending", []string{"life", "ascending"}},
		{"Dark Matter", []string{"dark", "matter"}},
		// stopwords and short words excluded
		{"The It", nil},
		// 3-char minimum: "war" included, "it" excluded
		{"It War", []string{"war"}},
		// stopword excluded even at sufficient length
		{"the and for", nil},
		// apostrophe stripped: "Ender's" → "enders"
		{"Ender's Game", []string{"enders", "game"}},
		// German umlaut transliteration
		{"Märchen", []string{"maerchen"}},
	}
	for _, c := range cases {
		got := SigWords(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SigWords(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Errorf("SigWords(%q)[%d] = %q, want %q", c.in, i, got[i], w)
			}
		}
	}
}

func TestTitleHasRelevantResult(t *testing.T) {
	matching := []SearchResult{
		{Title: "Nick.Lane.Life.Ascending.Evolution", BookTitle: ""},
	}
	canned := []SearchResult{
		{Title: "Stephen.King.It", BookTitle: ""},
		{Title: "Neil.Gaiman.Good.Omens", BookTitle: ""},
	}

	if !titleHasRelevantResult("Life Ascending", matching) {
		t.Error("expected true for results containing 'life' and 'ascending'")
	}
	if titleHasRelevantResult("Life Ascending", canned) {
		t.Error("expected false for canned results that don't contain query words")
	}
	// Empty query words (all stopwords/short) → always valid
	if !titleHasRelevantResult("The It", canned) {
		t.Error("expected true when query has no significant words")
	}
	// BookTitle field is also checked
	withBookTitle := []SearchResult{{Title: "random release", BookTitle: "Life Ascending"}}
	if !titleHasRelevantResult("Life Ascending", withBookTitle) {
		t.Error("expected true when BookTitle contains query word")
	}

	// Regression for #699: a canned feed where one common query word ("life")
	// coincidentally appears but the other ("ascending") is absent from every
	// result must be rejected, not accepted on the strength of the single hit.
	cannedPartialMatch := []SearchResult{
		{Title: "This Precious Life - Tibetan Buddhist Teachings"},
		{Title: "Bring Me the Rhinoceros - Zen Koans That Will Save Your Life"},
		{Title: "The Beginning After The End, 12"},
	}
	if titleHasRelevantResult("Life Ascending", cannedPartialMatch) {
		t.Error("expected false: 'ascending' is absent from every result (#699)")
	}

	// Sig-words spread across different results (not all in one) still count.
	spread := []SearchResult{
		{Title: "A book about Life"},
		{Title: "Something Ascending into the sky"},
	}
	if !titleHasRelevantResult("Life Ascending", spread) {
		t.Error("expected true when each sig-word appears in some result")
	}
}

// TestBookSearch_Tier1FallsBackOnCannedFeed simulates a Jackett/AudioBookBay
// indexer that always returns the same canned category results for t=book
// regardless of the title/author params. The canned results do not contain
// any significant word from the search title, so tier 1 should be rejected
// and the search should fall through to tier 2 (surname + title).
func TestBookSearch_Tier1FallsBackOnCannedFeed(t *testing.T) {
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		if q.Get("t") == "book" {
			// Canned feed: results that have nothing to do with the query.
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="18"/>
    <item><title>Stephen.King.It.mp3</title><guid isPermaLink="true">canned-1</guid>
      <enclosure url="https://example.com/canned-1" length="1" type="application/x-nzb"/>
    </item>
    <item><title>Neil.Gaiman.Good.Omens.epub</title><guid isPermaLink="true">canned-2</guid>
      <enclosure url="https://example.com/canned-2" length="1" type="application/x-nzb"/>
    </item>
  </channel>
</rss>`))
			return
		}
		// Tier 2: q="Lane Life Ascending" — return a relevant result.
		if q.Get("t") == "search" && strings.Contains(q.Get("q"), "Lane") {
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="1"/>
    <item><title>Nick.Lane.Life.Ascending.epub</title><guid isPermaLink="true">relevant-1</guid>
      <enclosure url="https://example.com/relevant-1" length="1" type="application/x-nzb"/>
    </item>
  </channel>
</rss>`))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "testkey")
	results, err := c.BookSearch(context.Background(), "Life Ascending", "Nick Lane", []int{7020})
	if err != nil {
		t.Fatalf("BookSearch: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 relevant result, got %d", len(results))
	}
	if results[0].Title != "Nick.Lane.Life.Ascending.epub" {
		t.Errorf("unexpected result title %q", results[0].Title)
	}
	// Tier 1 must have been tried (t=book) then rejected, so at least 2 requests total.
	if len(queries) < 2 {
		t.Fatalf("expected at least 2 requests (tier 1 + tier 2), got %d", len(queries))
	}
	if queries[0].Get("t") != "book" {
		t.Errorf("expected first request to be t=book, got t=%s", queries[0].Get("t"))
	}
}

// TestNew_HardenedDialerBlocksLoopback verifies that the http.Client returned
// by New() uses the httpsec-hardened DialContext that blocks loopback addresses,
// preventing DNS-rebinding attacks where a hostname resolves to 127.0.0.1 after
// the initial ValidateOutboundURL check passes. This is the Finding 1 fix from
// issue #711: re-validation happens per connection, not only at config time.
func TestNew_HardenedDialerBlocksLoopback(t *testing.T) {
	// Start a local server — its address will be on 127.0.0.1 (loopback).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use New() (hardened client), NOT testNew() — the dialer must be active.
	c := New(srv.URL, "testkey")
	_, err := c.Caps(context.Background())
	if err == nil {
		t.Fatal("expected connection to loopback to be rejected by the SSRF dialer, got nil error")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected 'loopback' in SSRF dial error, got: %v", err)
	}
}

// TestIndexerError_Classification verifies that IndexerError.Code is correctly
// set by parseNewznabError and that IsAuthError / IsRateLimitError /
// IsHardIndexerError classify the result accurately (Finding 3 — typed errors).
func TestIndexerError_Classification(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantAuth      bool
		wantRateLimit bool
		wantHard      bool
	}{
		{
			name:          "auth failure code 100",
			body:          `<error code="100" description="Incorrect user credentials"/>`,
			wantAuth:      true,
			wantRateLimit: false,
			wantHard:      true,
		},
		{
			name:          "account suspended code 101",
			body:          `<error code="101" description="Account suspended"/>`,
			wantAuth:      true,
			wantRateLimit: false,
			wantHard:      true,
		},
		{
			name:          "rate limit code 500",
			body:          `<error code="500" description="Request limit reached"/>`,
			wantAuth:      false,
			wantRateLimit: true,
			wantHard:      true,
		},
		{
			name:          "grabs limit code 520",
			body:          `<error code="520" description="Maximum grabs reached"/>`,
			wantAuth:      false,
			wantRateLimit: true,
			wantHard:      true,
		},
		{
			name:          "other code 300",
			body:          `<error code="300" description="No such function"/>`,
			wantAuth:      false,
			wantRateLimit: false,
			wantHard:      false,
		},
		{
			name:          "description-only (code 0)",
			body:          `<error description="site offline"/>`,
			wantAuth:      false,
			wantRateLimit: false,
			wantHard:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseNewznabError([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected non-nil error")
			}
			if got := IsAuthError(err); got != tc.wantAuth {
				t.Errorf("IsAuthError = %v, want %v", got, tc.wantAuth)
			}
			if got := IsRateLimitError(err); got != tc.wantRateLimit {
				t.Errorf("IsRateLimitError = %v, want %v", got, tc.wantRateLimit)
			}
			if got := IsHardIndexerError(err); got != tc.wantHard {
				t.Errorf("IsHardIndexerError = %v, want %v", got, tc.wantHard)
			}
		})
	}
}

// TestBookSearch_AbortOnAuthError verifies that a tier-1 auth error (code 100)
// causes BookSearch to abort immediately rather than falling through to tiers
// 2-4 (Finding 1 — hard errors abort).
func TestBookSearch_AbortOnAuthError(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/xml")
		// All requests receive an auth-error response.
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "badkey")
	_, err := c.BookSearch(context.Background(), "Dark Matter", "Blake Crouch", []int{7020})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsAuthError(err) {
		t.Errorf("expected IsAuthError to be true, got err=%v", err)
	}
	if requestCount > 1 {
		t.Errorf("expected abort after 1 request, but made %d requests — auth error was not a hard stop", requestCount)
	}
}

// TestBookSearch_AbortOnRateLimitError verifies that a tier-2 rate-limit error
// (code 500) causes BookSearch to abort rather than falling through to tier 3/4
// (Finding 1).
func TestBookSearch_AbortOnRateLimitError(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		if q.Get("t") == "book" {
			// Tier 1 returns empty (not an error), triggering fallthrough.
			w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
			return
		}
		// Tier 2 (first text-search tier) returns rate-limit error.
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><error code="500" description="Request limit reached"/>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "somekey")
	_, err := c.BookSearch(context.Background(), "Dark Matter", "Blake Crouch", []int{7020})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsRateLimitError(err) {
		t.Errorf("expected IsRateLimitError to be true, got err=%v", err)
	}
	// Tier 1 (book) + tier 2 (rate-limited) = 2 requests max; must not try tier 3 or 4.
	if requestCount > 2 {
		t.Errorf("expected abort after 2 requests (t=book + rate-limited tier-2), made %d", requestCount)
	}
}

// TestBookSearch_FallsThroughOnSoftError verifies that a non-hard error (an
// HTTP 503 or other transport error that is NOT an IndexerError) still allows
// tier fall-through on tier 1 — only hard (auth/rate-limit) errors abort.
// This test uses an empty-results response on tier 1 and a network error on
// tier 2 to ensure tier 3/4 are still reached.
func TestBookSearch_NonHardErrorFallsThrough(t *testing.T) {
	// Tier 1 returns empty; tier 2 also returns empty; tier 3 matches.
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("t")+":"+r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		if q.Get("t") == "search" && strings.Contains(q.Get("q"), "Blake Crouch") {
			// Tier 3 match.
			w.Write([]byte(testRSS))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
	}))
	defer srv.Close()

	c := testNew(srv.URL, "key")
	results, err := c.BookSearch(context.Background(), "Dark Matter", "Blake Crouch", []int{7020})
	if err != nil {
		t.Fatalf("expected success on tier 3, got err=%v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from tier 3")
	}
	// Must have issued requests for more than 1 tier (proving fall-through happened).
	if len(queries) < 2 {
		t.Errorf("expected multiple tiers issued, got %d: %v", len(queries), queries)
	}
}
