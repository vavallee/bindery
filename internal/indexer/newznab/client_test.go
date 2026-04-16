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
