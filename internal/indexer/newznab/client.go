// Package newznab provides a client for Newznab- and Torznab-compatible
// indexers used for book search.
package newznab

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

// Client interacts with a single Newznab-compatible indexer.
type Client struct {
	baseURL  string
	baseHost string
	apiKey   string
	http     *http.Client
}

// New creates a Newznab client for a specific indexer.
func New(baseURL, apiKey string) *Client {
	parsedURL := normalizeEndpointURL(baseURL)
	resolvedAPIKey := strings.TrimSpace(apiKey)
	var baseHost string
	if u, err := url.Parse(parsedURL); err == nil {
		q := u.Query()
		if resolvedAPIKey == "" {
			if qKey := strings.TrimSpace(q.Get("apikey")); qKey != "" {
				resolvedAPIKey = qKey
			}
		}
		q.Del("apikey")
		u.RawQuery = q.Encode()
		parsedURL = strings.TrimRight(u.String(), "/")
		baseHost = strings.ToLower(u.Host)
	}

	return &Client{
		baseURL:  parsedURL,
		baseHost: baseHost,
		apiKey:   resolvedAPIKey,
		http:     newHTTPClient(),
	}
}

// SetHTTPClient replaces the client's internal http.Client. Intended for tests
// that need to reach httptest servers on loopback without triggering the SSRF
// dialer that newHTTPClient installs by default.
func (c *Client) SetHTTPClient(h *http.Client) {
	c.http = h
}

// sharedTransport is the single *http.Transport used by every newznab client
// in the process. Hoisting it to a package-level singleton was finding 9 of
// the Wave 3 deep audit: previously newHTTPClient was called from New() on
// every search, so the connection pool, idle-conn cache, and TLS-session
// cache were thrown away on each call. With one shared transport every
// indexer search reuses keep-alive TCP connections (and resumed TLS sessions
// on https indexers), eliminating per-search handshake cost in hot paths
// like manual search and auto-grab cycles.
//
// The DialContext re-validates the resolved IP address on every new TCP
// connection so DNS-rebinding attacks are still blocked: ValidateOutboundURL
// runs at indexer create/update time, but an attacker who controls the
// indexer hostname can flip its DNS record to 169.254.169.254 (cloud
// metadata) or an RFC1918 address after the initial check passes. The
// per-connection re-validation catches a post-TTL rebind before connect(2).
// PolicyLAN is used because indexers legitimately run on LAN addresses;
// loopback, link-local, and cloud-metadata remain blocked under all
// policies.
var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
	// transportBuildCount counts how many times the shared transport
	// initialisation has run. Tests assert this stays at 1 across many
	// New() calls; production code never reads it.
	transportBuildCount atomic.Int64
)

func sharedTransportInstance() *http.Transport {
	sharedTransportOnce.Do(func() {
		transportBuildCount.Add(1)
		sharedTransport = &http.Transport{
			DialContext:           httpsec.NewDialContext(httpsec.PolicyLANLoopback),
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	})
	return sharedTransport
}

// TransportBuildCount returns the number of times the shared transport has
// been constructed. Exposed for tests verifying the pooling invariant
// (should remain 1 for the life of the process). Production callers should
// not rely on this value.
func TransportBuildCount() int64 {
	return transportBuildCount.Load()
}

// newHTTPClient returns an *http.Client backed by the package-shared
// transport. Each client gets its own *http.Client wrapper so per-call
// timeouts can be tuned independently, but they all funnel into the same
// connection pool.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: sharedTransportInstance(),
	}
}

// signDownloadURL appends the indexer's apikey query parameter to a download
// URL when, and only when, the URL points at the indexer's own host. This
// fixes Prowlarr-proxy enclosure URLs (which omit the apikey and get rejected
// by NZBGet as empty content) without leaking the apikey to third-party
// direct-from-uploader links an indexer might return.
func (c *Client) signDownloadURL(raw string) string {
	if raw == "" || c.apiKey == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.EqualFold(u.Host, c.baseHost) {
		return raw
	}
	q := u.Query()
	if q.Get("apikey") != "" {
		return raw
	}
	q.Set("apikey", c.apiKey)
	u.RawQuery = q.Encode()
	return u.String()
}

// Caps fetches the indexer capabilities.
func (c *Client) Caps(ctx context.Context) (*capsResponse, error) {
	u, err := c.buildURL("caps", map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("caps: %w", err)
	}
	var caps capsResponse
	if err := c.getXML(ctx, u, &caps); err != nil {
		return nil, fmt.Errorf("caps: %w", err)
	}
	return &caps, nil
}

// Search performs a general search with optional category filtering.
func (c *Client) Search(ctx context.Context, query string, categories []int) ([]SearchResult, error) {
	cats := intSliceToCSV(categories)
	u, err := c.buildURL("search", map[string]string{
		"q":     query,
		"cat":   cats,
		"limit": "100",
	})
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	slog.Debug("indexer query", "url", redactAPIKey(u))

	var rss rssResponse
	if err := c.getXML(ctx, u, &rss); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	return c.parseResults(rss.Channel.Items), nil
}

// BookSearch tries a series of query variants against a Newznab/Torznab
// indexer and returns the first variant that yields results. Query order:
//
//  1. Structured t=book with title+author (primary title only — subtitles
//     are dropped for the query; filterRelevant still matches on the full).
//  2. t=search "Lastname Title" — most disambiguating freeform tier for
//     short titles (e.g. "Russell The Sparrow" beats "The Sparrow" alone).
//  3. t=search "Author Title" — full author name + title.
//  4. t=search "Title" — last-resort fallback.
func (c *Client) BookSearch(ctx context.Context, title, author string, categories []int) ([]SearchResult, error) {
	queryTitle := primaryTitleForQuery(title)
	surname := authorSurname(author)
	cats := intSliceToCSV(categories)

	// Tier 1: structured t=book
	if author != "" {
		u, err := c.buildURL("book", map[string]string{
			"title":  queryTitle,
			"author": author,
			"cat":    cats,
			"limit":  "100",
		})
		if err == nil {
			slog.Debug("indexer query", "tier", 1, "url", redactAPIKey(u))
			var rss rssResponse
			if err := c.getXML(ctx, u, &rss); err != nil {
				// Hard errors (auth failure, rate limit) mean the indexer has
				// explicitly rejected this session. Firing tiers 2-4 at the same
				// indexer would repeat the same rejection — abort immediately so
				// the caller can surface the real error rather than logging "0
				// results". Network timeouts and context cancellations are also
				// treated as hard stops: the indexer is unreachable for the
				// duration of this search.
				if IsHardIndexerError(err) || ctx.Err() != nil {
					return nil, err
				}
				slog.Debug("indexer query tier fallthrough", "tier", 1, "error", err)
			} else if len(rss.Channel.Items) > 0 && rss.Channel.Response.Total < 1000 {
				parsed := c.parseResults(rss.Channel.Items)
				if titleHasRelevantResult(queryTitle, parsed) {
					slog.Debug("indexer query tier matched", "tier", 1, "count", len(parsed))
					return parsed, nil
				}
				// Indexer returned a fixed category feed that ignored title/author
				// (Jackett/AudioBookBay pattern). Fall through to text-search tiers.
				slog.Debug("indexer query tier 1 canned feed, falling through",
					"count", len(parsed),
					"words_checked", SigWords(queryTitle))
			} else {
				slog.Debug("indexer query tier fallthrough", "tier", 1, "items", len(rss.Channel.Items))
			}
		}
	}

	// Tier 2: surname + title (short, disambiguating)
	if surname != "" && !strings.EqualFold(surname, author) {
		results, err := c.Search(ctx, surname+" "+queryTitle, categories)
		if err != nil {
			if IsHardIndexerError(err) || ctx.Err() != nil {
				return nil, err
			}
		} else if len(results) > 0 {
			slog.Debug("indexer query tier matched", "tier", 2, "count", len(results))
			return results, nil
		}
	}

	// Tier 3: full author + title
	if author != "" {
		results, err := c.Search(ctx, author+" "+queryTitle, categories)
		if err != nil {
			if IsHardIndexerError(err) || ctx.Err() != nil {
				return nil, err
			}
		} else if len(results) > 0 {
			slog.Debug("indexer query tier matched", "tier", 3, "count", len(results))
			return results, nil
		}
	}

	// Tier 4: title only
	slog.Debug("indexer query tier 4 (title only)", "title", queryTitle)
	return c.Search(ctx, queryTitle, categories)
}

// redactAPIKey replaces the apikey query parameter value with *** so URLs
// can be logged without leaking credentials.
func redactAPIKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Get("apikey") != "" {
		q.Set("apikey", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// primaryTitleForQuery returns the portion of a book title before a colon,
// so "Dune: Messiah" queries as "Dune". Indexers rarely have the subtitle
// in the release name and including it can cause all-keyword-match failures.
//
// The title is also normalized so two book rows that differ only in
// incidental metadata (leading/trailing whitespace, collapsed interior
// whitespace, smart-quote variants, parenthesised language suffixes like
// "(German Edition)") produce identical queries. Without this, ingesting
// the same work from multiple metadata providers yields two rows that
// search differently — see issue #250.
func primaryTitleForQuery(title string) string {
	if i := strings.Index(title, ":"); i > 0 {
		title = title[:i]
	}
	return NormalizeQueryTitle(title)
}

// parenSuffixRe matches a trailing parenthesised qualifier used by metadata
// providers to distinguish editions of the same work, e.g. "(German Edition)",
// "(Unabridged)", "(2nd ed.)". Indexers almost never carry these qualifiers
// in the release name, so including them reliably zeros out the result set.
var parenSuffixRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// NormalizeQueryTitle strips incidental differences that would cause two
// metadata-provider rows for the same work to generate different indexer
// queries: Unicode smart quotes are folded to ASCII, whitespace is trimmed
// and collapsed, and a single trailing parenthesised qualifier is removed.
// Called by primaryTitleForQuery and exported so ingestion paths can use
// the same normalization for title-based deduplication.
func NormalizeQueryTitle(title string) string {
	title = strings.NewReplacer(
		"\u2018", "'", "\u2019", "'",
		"\u201C", `"`, "\u201D", `"`,
		"\u2013", "-", "\u2014", "-",
	).Replace(title)
	title = parenSuffixRe.ReplaceAllString(title, "")
	return strings.Join(strings.Fields(title), " ")
}

func authorSurname(author string) string {
	fields := strings.Fields(author)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// titleHasRelevantResult returns true when *every* significant word of
// queryTitle (as determined by SigWords) appears in at least one result —
// the words may be spread across different results, not all in one. A false
// return means the indexer likely returned a fixed category feed that ignored
// the search params (Jackett/AudioBookBay pattern), so callers should fall
// through to text-search tiers.
//
// The earlier "any sig-word in any result" check was too weak: for a query
// like "Life Ascending", the common word "life" coincidentally matches an
// unrelated title in AudioBookBay's canned feed, so the whole canned feed was
// accepted as a tier-1 hit. The downstream relevance filter then rejected all
// of it, leaving the user with zero results instead of falling through (#699).
// A genuine t=book response covers every query word; a canned feed only
// matches words by coincidence and rarely covers all of them. The failure
// mode of this stricter check is a benign false negative — fall through to
// text-search tiers — which beats a false positive that empties the results.
//
// Single-sig-word titles (e.g. "Dune") remain inherently ambiguous: there is
// no second word to disambiguate a coincidental canned-feed match.
func titleHasRelevantResult(queryTitle string, results []SearchResult) bool {
	words := SigWords(queryTitle)
	if len(words) == 0 {
		return true // query has no checkable words; assume results are valid
	}
	combined := make([]string, len(results))
	for i, r := range results {
		combined[i] = strings.ToLower(r.Title + " " + r.BookTitle)
	}
	for _, w := range words {
		found := false
		for _, c := range combined {
			if strings.Contains(c, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Test verifies the indexer is reachable and the API key is valid.
func (c *Client) Test(ctx context.Context) error {
	_, err := c.Caps(ctx)
	return err
}

// ProbeResult summarizes a connectivity and search check against the indexer.
type ProbeResult struct {
	Status        int    `json:"status"`
	Categories    int    `json:"categories"`
	BookSearch    bool   `json:"bookSearch"`
	GeneralSearch bool   `json:"generalSearch"`
	LatencyMs     int64  `json:"latencyMs"`
	SearchResults int    `json:"searchResults"` // results from the test search query
	SearchError   string `json:"searchError,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Probe performs a capabilities fetch followed by a lightweight test search,
// returning a structured summary without writing anything to the database.
//
// The two-step approach catches a class of misconfiguration that caps-only
// probes miss: an indexer can report HTTP 200 and valid capabilities while
// still returning zero results for every query (wrong API key permissions,
// no books indexed, category mismatch). The test search uses "t=search&q=book"
// against the configured book categories and records how many results came
// back so the UI can warn when connectivity succeeds but searches return nothing.
func (c *Client) Probe(ctx context.Context) ProbeResult {
	u, err := c.buildURL("caps", map[string]string{})
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	req.Header.Set("User-Agent", useragent.Get())

	resp, err := c.http.Do(req)
	if err != nil {
		return ProbeResult{LatencyMs: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	defer resp.Body.Close()

	result := ProbeResult{Status: resp.StatusCode, LatencyMs: time.Since(start).Milliseconds()}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		result.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return result
	}

	var caps capsResponse
	if err := xml.NewDecoder(resp.Body).Decode(&caps); err != nil {
		result.Error = fmt.Sprintf("parse caps: %v", err)
		return result
	}

	result.Categories = len(caps.Categories.Categories)
	result.BookSearch = strings.EqualFold(caps.Searching.BookSearch.Available, "yes")
	result.GeneralSearch = strings.EqualFold(caps.Searching.Search.Available, "yes")

	// Run a real test search against the book categories to catch the case
	// where caps succeeds but actual queries return nothing.
	hits, err := c.Search(ctx, "book", bookCategoriesFromCaps(caps))
	if err != nil {
		result.SearchError = err.Error()
	} else {
		result.SearchResults = len(hits)
	}

	return result
}

// bookCategoriesFromCaps extracts 7xxx (ebook) category IDs from a caps
// response. Falls back to [7020] when none are advertised so the test
// search always targets book content.
func bookCategoriesFromCaps(caps capsResponse) []int {
	var out []int
	for _, cat := range caps.Categories.Categories {
		id, err := strconv.Atoi(cat.ID)
		if err != nil {
			continue
		}
		if id/1000 == 7 {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return []int{7020}
	}
	return out
}

func (c *Client) parseResults(items []rssItem) []SearchResult {
	results := make([]SearchResult, 0, len(items))
	for _, item := range items {
		r := SearchResult{
			GUID:    item.GUID.Value,
			Title:   item.Title,
			Size:    item.Enclosure.Length,
			NZBURL:  c.signDownloadURL(item.Enclosure.URL),
			PubDate: item.PubDate,
		}

		// Parse newznab attributes
		for _, attr := range item.Attrs {
			switch attr.Name {
			case "size":
				if s, err := strconv.ParseInt(attr.Value, 10, 64); err == nil {
					r.Size = s
				}
			case "grabs":
				if g, err := strconv.Atoi(attr.Value); err == nil {
					r.Grabs = g
				}
			case "category":
				r.Category = attr.Value
			case "author":
				r.Author = attr.Value
			case "title":
				r.BookTitle = attr.Value
			case "language":
				r.Language = attr.Value
			}
		}

		if r.NZBURL == "" {
			r.NZBURL = c.signDownloadURL(item.Link)
		}

		results = append(results, r)
	}
	return results
}

func (c *Client) getXML(ctx context.Context, rawURL string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", useragent.Get())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("read indexer response: %w", err)
	}

	// Newznab/Torznab indexers report failures (bad API key, rate limit, site
	// disabled, …) as a top-level <error code="N" description="..."/> element
	// instead of an <rss> feed — on either a 2xx or an error status. Surface
	// the indexer's own code and description rather than leaking the XML
	// decoder's unhelpful "expected element type <rss> but have <error>".
	if nzErr := parseNewznabError(body); nzErr != nil {
		return nzErr
	}

	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet)
	}

	return xml.Unmarshal(body, target)
}

// parseNewznabError returns a non-nil *IndexerError when body is a
// Newznab/Torznab <error> response. It returns nil for any other document —
// including a normal <rss> feed or non-XML content — so callers proceed with
// their usual decoding.
//
// The returned error is a *IndexerError so callers can use IsAuthError /
// IsRateLimitError / IsHardIndexerError to classify the failure and decide
// whether to abort tier fall-through or log differently.
func parseNewznabError(body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil // not XML, or no start element — let the caller decide
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue // skip the prolog, comments, and whitespace
		}
		if !strings.EqualFold(start.Name.Local, "error") {
			return nil // root is <rss> or something else; not an error response
		}
		var codeStr, desc string
		for _, attr := range start.Attr {
			switch strings.ToLower(attr.Name.Local) {
			case "code":
				codeStr = strings.TrimSpace(attr.Value)
			case "description":
				desc = strings.TrimSpace(attr.Value)
			}
		}
		if codeStr == "" && desc == "" {
			return nil // a bare <error/> tells us nothing actionable
		}
		code, _ := strconv.Atoi(codeStr)
		return &IndexerError{Code: code, Description: desc}
	}
}

func (c *Client) buildURL(command string, params map[string]string) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid indexer URL: %w", err)
	}

	q := u.Query()
	q.Del("t")
	q.Del("q")
	q.Del("cat")
	q.Del("limit")
	q.Del("title")
	q.Del("author")
	q.Del("apikey")

	q.Set("t", command)
	for k, v := range params {
		q.Set(k, v)
	}
	if c.apiKey != "" {
		q.Set("apikey", c.apiKey)
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func normalizeEndpointURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return strings.TrimRight(trimmed, "/")
	}

	p := strings.TrimSpace(u.Path)
	if p == "" || p == "/" {
		u.Path = "/api"
		u.RawPath = ""
		return strings.TrimRight(u.String(), "/")
	}

	normalized := strings.TrimRight(path.Clean(p), "/")
	if normalized == "" || normalized == "." {
		normalized = "/api"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	if !strings.Contains(strings.ToLower(normalized), "torznab") && !strings.HasSuffix(strings.ToLower(normalized), "/api") {
		normalized = strings.TrimRight(normalized, "/") + "/api"
	}

	u.Path = normalized
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/")
}

func intSliceToCSV(ints []int) string {
	if len(ints) == 0 {
		return "7020"
	}
	parts := make([]string, len(ints))
	for i, v := range ints {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}
