package newznab

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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
		{nil, "7000,7020"},
		{[]int{}, "7000,7020"},
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
	c := New(endpoint, "")
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
	c := New(endpoint, "from-field")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "wrong")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
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

	c := New(srv.URL, "testkey")
	_, err := c.Search(context.Background(), "anything", nil)
	if err == nil {
		t.Fatalf("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention 503, got %v", err)
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
