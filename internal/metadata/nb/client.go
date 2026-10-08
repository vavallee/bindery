// Package nb provides a read-only client for Nasjonalbiblioteket, the
// National Library of Norway, via its public catalogue search API. No API key
// is required.
//
// Role: opt-in primary. NB holds every Norwegian publication by legal deposit
// under its original title, where OpenLibrary tends to catalogue Norwegian
// books under their English translation. It is wired only when selected as the
// primary provider, so installs that did not choose it never call NB.
//
// Endpoints:
//   - https://api.nb.no/catalog/v1/items — bibliographic search. The API
//     states no licence of its own; NB's metadata delivery (OAI-PMH, SRU)
//     publishes the same records under CC0. See docs/third-party-data.md.
//   - https://authority.bibsys.no — the Norwegian authority file, used only to
//     turn an author's authority ID back into a name, because NB's search has
//     no field that accepts the ID.
//
// NB catalogues editions, not works: every printing, translation and
// audiobook is its own record. groupWorks folds them into works.
package nb

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/models"
)

const (
	itemsBase     = "https://api.nb.no/catalog/v1/items"
	metadataBase  = "https://api.nb.no/catalog/v1/metadata/"
	authorityBase = "https://authority.bibsys.no/authority/rest/authorities/v2/"
	idPrefix      = "nb:"
	authorPrefix  = "nb:author:"
	// authorityIDPrefix is how NB records name a person's authority record.
	authorityIDPrefix = "bibsys.no:authority:"
	// pageSize is the API's largest accepted page; 101 is rejected with 400.
	pageSize = 100
	// maxWorksPages caps an author catalogue at 2000 edition records. A larger
	// author is reported as a partial snapshot rather than paged without end.
	// The creator index counts every translation, so a widely translated
	// author runs to well over a thousand.
	maxWorksPages = 20
	// maxResponseBytes bounds what a misbehaving host can make Bindery decode
	// (#2357). A full 100-record page with expanded metadata is ~1 MiB.
	maxResponseBytes = 32 << 20
)

// sesamIDRe matches NB's record identifier, a 32-char hex string. Validated
// before it is put in a request path.
var sesamIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// authorityIDRe matches a Norwegian authority file system control number.
var authorityIDRe = regexp.MustCompile(`^[0-9]+$`)

// Client implements metadata.Provider for Nasjonalbiblioteket.
type Client struct {
	http *http.Client
	// gate holds every request this client makes while NB is refusing. Nil
	// (a zero value Client in tests) holds each call on its own.
	gate *providerhttp.Gate
}

// New creates a new NB client.
func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
		gate: providerhttp.NewGate(),
	}
}

func (c *Client) Name() string { return "nb" }

// ready reports ErrProviderNotConfigured for a client that cannot make
// requests. NB needs no credentials, so New always returns a usable client;
// this guards the zero value so it is skipped rather than panicking.
func (c *Client) ready() error {
	if c == nil || c.http == nil {
		return metadata.ErrProviderNotConfigured
	}
	return nil
}

// SearchAuthors finds authors whose name matches every word of query, from the
// author credits of matching records. Each person is keyed by authority ID, so
// two people sharing a name stay apart.
func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, nil
	}
	params := url.Values{"q": {"*"}}
	for _, w := range words {
		params.Add("filter", "api_nameauthor:"+escapeQuery(w))
	}
	params.Add("filter", "mediatype:bøker")
	page, err := c.search(ctx, params, 0)
	if err != nil {
		return nil, fmt.Errorf("nb search authors: %w", err)
	}

	counts := make(map[string]int)
	byID := make(map[string]models.Author)
	var order []string
	for _, it := range page.Embedded.Items {
		for _, p := range it.Metadata.People {
			id := p.authorityID()
			if id == "" || !p.isAuthor() || !nameMatches(p.Name, words) {
				continue
			}
			if _, ok := byID[id]; !ok {
				byID[id] = personToAuthor(p)
				order = append(order, id)
			}
			counts[id]++
		}
	}
	authors := make([]models.Author, 0, len(order))
	for _, id := range order {
		a := byID[id]
		// A count of matching records, not works: editions are not grouped
		// here. It only ranks same-name candidates against each other.
		a.Statistics = &models.AuthorStats{BookCount: counts[id]}
		authors = append(authors, a)
	}
	return authors, nil
}

// SearchBooks searches catalogue metadata (not digitised full text) and folds
// the matching editions into works. An ISBN query is answered by
// GetBookByISBN.
func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	// Only a query that is nothing but an ISBN, optionally in the "isbn:<n>"
	// form the aggregator's canonical lookup sends; a title containing digits
	// stays a text search.
	bare := query
	if len(bare) > 5 && strings.EqualFold(bare[:5], "isbn:") {
		bare = strings.TrimSpace(bare[5:])
	}
	if isbn13, isbn10 := isbnutil.Extract(bare); isbnutil.Normalize(bare) == firstNonEmpty(isbn13, isbn10) {
		b, err := c.GetBookByISBN(ctx, bare)
		if err != nil || b == nil {
			return nil, err
		}
		return []models.Book{*b}, nil
	}
	params := url.Values{
		"q":          {escapeQuery(query)},
		"searchType": {"FIELD_RESTRICTED_SEARCH"},
		// The author catalogue's media types, so a work found here has the
		// same editions as in the catalogue.
		"filter": {"mediatype:(bøker OR lydopptak)"},
	}
	page, err := c.search(ctx, params, 0)
	if err != nil {
		return nil, fmt.Errorf("nb search books: %w", err)
	}
	return groupWorks(page.Embedded.Items, ""), nil
}

// GetAuthor resolves an "nb:author:<authority id>" through the Norwegian
// authority file. Returns (nil, nil) when the authority record does not exist.
func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	id, err := authorityIDFromForeignID(foreignID)
	if err != nil {
		return nil, err
	}
	rec, err := c.authority(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("nb get author %s: %w", foreignID, err)
	}
	if rec == nil {
		return nil, nil
	}
	a := personToAuthor(person{Name: rec.heading(), Identifier: authorityIDPrefix + id})
	// Bindery saves the ones that are another spelling of the same name as
	// aliases, so release names without the diacritics still match.
	a.AlternateNames = rec.variants()
	return &a, nil
}

// GetAuthorWorks returns the author's catalogue, folded into works.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	books, _, err := c.GetAuthorWorksSnapshot(ctx, authorForeignID)
	return books, err
}

// GetAuthorWorksSnapshot is GetAuthorWorks plus whether the catalogue is
// complete. It is not when the author has more records than maxWorksPages
// covers, so catalogue reconciliation must not read a missing work as removed.
//
// The records are fetched by the authority file's name heading and then kept
// only when they credit this authority ID as author, which drops homonyms and
// records where the person is translator or narrator. A failed name lookup is
// an error, never an empty catalogue: an empty result from a primary is taken
// as fact.
func (c *Client) GetAuthorWorksSnapshot(ctx context.Context, authorForeignID string) ([]models.Book, bool, error) {
	if err := c.ready(); err != nil {
		return nil, false, err
	}
	id, err := authorityIDFromForeignID(authorForeignID)
	if err != nil {
		return nil, false, err
	}
	name, found, err := c.authorityName(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("nb get author works %s: %w", authorForeignID, err)
	}
	if !found {
		// A record that is gone (Sikt merges duplicate records) says
		// nothing about the author's books, so it is not a complete,
		// empty catalogue that reconciliation could act on.
		return nil, false, nil
	}

	params := url.Values{
		"q": {"*"},
		// Audiobooks are included so their ISBNs land on the work: a
		// library file is as likely to be the audiobook edition.
		// namecreators, not nameauthor: NB leaves many records crediting
		// the author out of the author index, some authors' most of them.
		// The authority-ID check in groupWorks drops the creator index's
		// translator and narrator credits.
		"filter": {`namecreators:"` + escapeQuery(name) + `"`, "mediatype:(bøker OR lydopptak)"},
	}
	var items []item
	complete := false
	for p := 0; p < maxWorksPages; p++ {
		page, err := c.search(ctx, params, p)
		if err != nil {
			return nil, false, fmt.Errorf("nb get author works %s: %w", authorForeignID, err)
		}
		items = append(items, page.Embedded.Items...)
		if p+1 >= page.Page.TotalPages {
			complete = true
			break
		}
	}
	if len(items) == 0 {
		// The authority record exists, so the author does too; finding no
		// records is a gap in NB's name index (see recallSeriesVolumes),
		// not proof of an empty catalogue.
		complete = false
	}
	books := groupWorks(items, id)
	memo := &seriesMemo{}
	c.fillSeries(ctx, books, items, id, memo)
	recalled, recallComplete := c.recallSeriesVolumes(ctx, books, items, id)
	if !recallComplete {
		complete = false
	}
	if len(recalled) > 0 {
		items = append(items, recalled...)
		books = groupWorks(items, id)
		c.fillSeries(ctx, books, items, id, memo)
	}
	return books, complete, nil
}

// recallSeriesVolumes finds the author's records that the author query
// missed. NB leaves some records out of every name index although they
// credit the author, so no name search returns them; a search on the series
// name does. One search per series the catalogue links the author to,
// keeping only records crediting the author's authority ID that are not
// already in items.
//
// complete is false when a search failed, so a caller reconciling the
// catalogue does not read a volume found this way last time as removed.
// ponytail: first result page only (100 records); a series name matching
// more than that across all authors misses the rest.
func (c *Client) recallSeriesVolumes(ctx context.Context, books []models.Book, items []item, authorID string) (recalled []item, complete bool) {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		seen[it.ID] = true
	}
	var titles []string
	titleSeen := make(map[string]bool)
	for _, b := range books {
		for _, ref := range b.SeriesRefs {
			if !titleSeen[ref.Title] {
				titleSeen[ref.Title] = true
				titles = append(titles, ref.Title)
			}
		}
	}
	sort.Strings(titles)
	complete = true
	for _, title := range titles {
		params := url.Values{
			"q":          {`"` + escapeQuery(title) + `"`},
			"searchType": {"FIELD_RESTRICTED_SEARCH"},
			"filter":     {"mediatype:(bøker OR lydopptak)"},
		}
		page, err := c.search(ctx, params, 0)
		if err != nil {
			slog.Debug("nb: series recall search failed", "series", title, "error", err)
			complete = false
			continue
		}
		for _, it := range page.Embedded.Items {
			if seen[it.ID] || primaryAuthor(it.Metadata, authorID) == nil {
				continue
			}
			seen[it.ID] = true
			recalled = append(recalled, it)
		}
	}
	return recalled, complete
}

// GetBook fetches the edition record "nb:<sesam id>" and returns the work it
// belongs to. NB has no work record, so the record's siblings are found the
// way the author catalogue finds them: same authority-file author, folded by
// title. The aggregator refreshes ISBN matches through here, so a book built
// from one record would lose its other editions' ISBNs.
func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	id := strings.TrimPrefix(foreignID, idPrefix)
	if id == foreignID || !sesamIDRe.MatchString(id) {
		return nil, fmt.Errorf("nb: not a book id: %q", foreignID)
	}
	var it item
	found, err := c.getJSON(ctx, itemsBase+"/"+id, &it)
	if err != nil {
		return nil, fmt.Errorf("nb get book %s: %w", foreignID, err)
	}
	if !found {
		return nil, nil
	}
	if b := c.workOf(ctx, it); b != nil {
		return b, nil
	}
	books := groupWorks([]item{it}, "")
	if len(books) == 0 {
		return nil, nil
	}
	// A rebind replaces the book's series with these, so they are filled
	// here as well as in the catalogue.
	if a := primaryAuthor(it.Metadata, ""); a != nil && a.authorityID() != "" {
		c.fillSeries(ctx, books, []item{it}, a.authorityID(), nil)
	}
	return &books[0], nil
}

// workOf returns the work containing it, built from a search for the record's
// author and title, or nil when that cannot be done. Best-effort: the caller
// already holds the record and falls back to it alone.
func (c *Client) workOf(ctx context.Context, it item) *models.Book {
	author := primaryAuthor(it.Metadata, "")
	if author == nil || author.authorityID() == "" {
		return nil
	}
	params := url.Values{
		"q":          {escapeQuery(recordTitle(it.Metadata))},
		"searchType": {"FIELD_RESTRICTED_SEARCH"},
		// No name-index filter: NB leaves some records out of it (see
		// recallSeriesVolumes). groupWorks keeps only the author's records.
		"filter": {"mediatype:(bøker OR lydopptak)"},
	}
	page, err := c.search(ctx, params, 0)
	if err != nil {
		return nil
	}
	want := idPrefix + it.ID
	books := groupWorks(page.Embedded.Items, author.authorityID())
	for i := range books {
		for _, ed := range books[i].Editions {
			if ed.ForeignID != want {
				continue
			}
			// Keep the requested ID: callers look the book up by it.
			books[i].ForeignID = want
			c.fillSeries(ctx, books[i:i+1], page.Embedded.Items, author.authorityID(), nil)
			return &books[i]
		}
	}
	return nil
}

// GetEditions returns the editions of the work bookForeignID belongs to, as
// GetBook assembles it: the record and its siblings, or the record alone when
// the sibling search fails. Profile checks on ISBN and page count then have
// evidence instead of nothing.
func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	b, err := c.GetBook(ctx, bookForeignID)
	if err != nil || b == nil {
		return nil, err
	}
	return b.Editions, nil
}

// GetBookByISBN looks up an edition by ISBN-13 or ISBN-10, any media type, so
// an audiobook ISBN resolves as well as a print one.
func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	isbn13, isbn10 := isbnutil.Extract(isbn)
	want := firstNonEmpty(isbn13, isbn10)
	if want == "" {
		return nil, nil
	}
	page, err := c.search(ctx, url.Values{"q": {"isbn:" + want}}, 0)
	if err != nil {
		return nil, fmt.Errorf("nb get book by ISBN: %w", err)
	}
	books := groupWorks(page.Embedded.Items, "")
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// search runs one page of an items query with expanded metadata, which is
// what carries the author credits and uniform titles.
func (c *Client) search(ctx context.Context, params url.Values, page int) (*searchResponse, error) {
	params.Set("size", strconv.Itoa(pageSize))
	params.Set("page", strconv.Itoa(page))
	params.Set("expand", "metadata")
	var out searchResponse
	if _, err := c.getJSON(ctx, itemsBase+"?"+params.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// authority fetches an authority record, or nil when it is missing, deleted,
// or has no name heading.
func (c *Client) authority(ctx context.Context, id string) (*authorityRecord, error) {
	var rec authorityRecord
	found, err := c.getJSON(ctx, authorityBase+id+"?format=json", &rec)
	if err != nil || !found || rec.Deleted || rec.heading() == "" {
		return nil, err
	}
	return &rec, nil
}

// authorityName returns the authorised name heading ("Last, First") for an
// authority record. found is false when the record is missing or deleted.
func (c *Client) authorityName(ctx context.Context, id string) (string, bool, error) {
	rec, err := c.authority(ctx, id)
	if err != nil || rec == nil {
		return "", false, err
	}
	return rec.heading(), true, nil
}

// getJSON GETs endpoint and decodes the body into out. found is false on 404.
func (c *Client) getJSON(ctx context.Context, endpoint string, out any) (bool, error) {
	return c.get(ctx, endpoint, "application/json", func(r io.Reader) error {
		return json.NewDecoder(r).Decode(out)
	})
}

// getXML is getJSON for the MODS endpoint.
func (c *Client) getXML(ctx context.Context, endpoint string, out any) (bool, error) {
	return c.get(ctx, endpoint, "application/xml", func(r io.Reader) error {
		return xml.NewDecoder(r).Decode(out)
	})
}

// get GETs endpoint and decodes the size-limited body. found is false on 404.
func (c *Client) get(ctx context.Context, endpoint, accept string, decode func(io.Reader) error) (bool, error) {
	// The shared provider request loop retries a refusal or outage with
	// Retry-After and backoff, holds every NB request while one is refused,
	// and marks what outlives the retries with the shared provider errors so
	// scheduled discovery backs off (#2369). NB publishes no rate limits, so
	// its refusals are taken at their word.
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		URL:    endpoint,
		Header: http.Header{"Accept": {accept}},
		Pass:   []int{http.StatusNotFound},
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := decode(io.LimitReader(resp.Body, maxResponseBytes)); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	return true, nil
}

func authorityIDFromForeignID(foreignID string) (string, error) {
	id := strings.TrimPrefix(foreignID, authorPrefix)
	if id == foreignID || !authorityIDRe.MatchString(id) {
		return "", errors.New("nb: not an author id: " + strconv.Quote(foreignID))
	}
	return id, nil
}

// escapeQuery backslash-escapes the query_string syntax characters so user
// input is searched as text instead of being parsed as query operators.
func escapeQuery(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`+-=&|><!(){}[]^"~*?:\/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
