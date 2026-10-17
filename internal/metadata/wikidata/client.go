// Package wikidata is an author enricher (#3287): it finds an author's
// Wikidata item by an authority ID Bindery already stores, never by name, and
// reads a photo from Wikimedia Commons and a summary from Wikipedia, each with
// the attribution its licence asks for.
//
// It is opt in. An install that has not switched it on sends Wikimedia no
// request at all: building the client sends nothing.
package wikidata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	// cacheTTL is how long an answer, a miss included, is kept. Photos and
	// summaries change rarely, and a miss asked again every day is exactly
	// the traffic this enricher must not cause.
	cacheTTL = 30 * 24 * time.Hour
	// partialTTL is how long an answer is kept when Commons or Wikipedia
	// failed while building it: long enough not to retry every author view,
	// short enough that one bad response doesn't cost a month without a
	// photo or bio.
	partialTTL = 6 * time.Hour
	// defaultHold is how long the client stays quiet after Wikimedia answers
	// 429 or 503 without a usable Retry-After.
	defaultHold = 15 * time.Minute
	// failureHold is how long it stays quiet after failureLimit requests in a
	// row failed outright (no answer, or an unexpected status): during an
	// outage each author would otherwise wait out the 15 second timeout.
	failureHold  = 5 * time.Minute
	failureLimit = 3
	// thumbWidth is the width asked of Commons; the author page shows a
	// 128px circle, so 400px is plenty for a high density screen.
	thumbWidth = 400
	maxBody    = 2 << 20

	wikipediaLicense    = "CC BY-SA 4.0"
	wikipediaLicenseURL = "https://creativecommons.org/licenses/by-sa/4.0/"
)

// ErrHeld is returned while the client is backing off after a rate limit.
var ErrHeld = errors.New("wikidata: holding after a rate limit")

// Client looks authors up on Wikidata, Wikimedia Commons and Wikipedia.
type Client struct {
	http        *http.Client
	wikidataAPI string
	commonsAPI  string
	// wikipediaREST is a format string taking the language code.
	wikipediaREST string
	now           func() time.Time

	mu        sync.Mutex
	cache     map[string]cacheEntry
	holdUntil time.Time
	failures  int // requests failed in a row; see failureLimit
}

type cacheEntry struct {
	profile *metadata.AuthorProfile
	at      time.Time
	ttl     time.Duration
}

// New returns a client for the public Wikimedia endpoints.
func New() *Client {
	return &Client{
		http:          &http.Client{Timeout: 15 * time.Second},
		wikidataAPI:   "https://www.wikidata.org/w/api.php",
		commonsAPI:    "https://commons.wikimedia.org/w/api.php",
		wikipediaREST: "https://%s.wikipedia.org/api/rest_v1",
		now:           time.Now,
		cache:         map[string]cacheEntry{},
	}
}

// Name implements metadata.AuthorEnricher.
func (c *Client) Name() string { return "wikidata" }

// EnrichAuthor implements metadata.AuthorEnricher. It answers (nil, nil) for
// an author whose primary ID Wikidata cannot be asked about, and for one it
// finds nothing for.
func (c *Client) EnrichAuthor(ctx context.Context, author *models.Author) (*metadata.AuthorProfile, error) {
	if author == nil {
		return nil, nil
	}
	property, value, ok := authorityID(author.ForeignID)
	if !ok {
		return nil, nil
	}
	key := property + "=" + value
	if profile, ok := c.cached(key); ok {
		return profile, nil
	}
	if c.held() {
		return nil, ErrHeld
	}
	profile, partial, err := c.lookup(ctx, property, value, wikiOrder(author.ForeignID))
	if err != nil {
		return nil, err
	}
	ttl := cacheTTL
	if partial {
		// Marked, so the refresh doesn't stamp it as an answer.
		if profile == nil {
			profile = &metadata.AuthorProfile{}
		}
		profile.Partial = true
		ttl = partialTTL
	}
	c.store(key, profile, ttl)
	return profile, nil
}

// authorityID maps a primary author ID onto the Wikidata property that holds
// the same identifier. Only IDs Wikidata records are mapped: Hardcover and
// Google Books have no property, and a synthetic "dnb:author:<slug>" is not
// an authority ID at all.
func authorityID(foreignID string) (property, value string, ok bool) {
	id := strings.TrimSpace(foreignID)
	switch {
	case strings.HasPrefix(id, "nb:author:"):
		// NORAF, the Norwegian authority file (BIBSYS), is what NB keys on.
		v := strings.TrimPrefix(id, "nb:author:")
		return "P1015", v, isDigits(v)
	case strings.HasPrefix(id, "dnb:gnd:"):
		v := strings.TrimPrefix(id, "dnb:gnd:")
		return "P227", v, v != "" && !strings.ContainsAny(v, " \"")
	case strings.HasPrefix(id, "bnf:author:"):
		v := strings.TrimPrefix(id, "bnf:author:")
		if len(v) != 8 || !isDigits(v) {
			return "", "", false
		}
		// Wikidata stores the BnF identifier with its check character.
		return "P268", v + bnfCheckChar("cb"+v), true
	case olAuthorID.MatchString(id):
		return "P648", id, true
	}
	return "", "", false
}

var olAuthorID = regexp.MustCompile(`^OL[0-9]+A$`)

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// bnfCheckChar is the NOID check character BnF appends to its ARK names: each
// character's value in the NOID alphabet times its 1-based position, summed,
// modulo 29.
func bnfCheckChar(name string) string {
	const alphabet = "0123456789bcdfghjkmnpqrstvwxz"
	sum := 0
	for i, r := range name {
		if v := strings.IndexRune(alphabet, r); v >= 0 {
			sum += v * (i + 1)
		}
	}
	return string(alphabet[sum%len(alphabet)])
}

// wikiOrder is the Wikipedia editions asked for the summary, in order: the
// language of the author's primary provider first, then English.
func wikiOrder(foreignID string) []string {
	switch models.AuthorProviderFromForeignID(foreignID) {
	case "nb":
		return []string{"no", "nn", "en"}
	case "bnf":
		return []string{"fr", "en"}
	case "dnb":
		return []string{"de", "en"}
	}
	return []string{"en"}
}

func (c *Client) cached(key string) (*metadata.AuthorProfile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || c.now().Sub(e.at) > e.ttl {
		return nil, false
	}
	return cloneProfile(e.profile), true
}

func (c *Client) store(key string, profile *metadata.AuthorProfile, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = cacheEntry{profile: cloneProfile(profile), at: c.now(), ttl: ttl}
}

func cloneProfile(p *metadata.AuthorProfile) *metadata.AuthorProfile {
	if p == nil {
		return nil
	}
	out := *p
	if p.ImageSource != nil {
		src := *p.ImageSource
		out.ImageSource = &src
	}
	if p.DescriptionSource != nil {
		src := *p.DescriptionSource
		out.DescriptionSource = &src
	}
	return &out
}

func (c *Client) held() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Before(c.holdUntil)
}

func (c *Client) hold(retryAfter string) {
	d := defaultHold
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		d = time.Duration(secs) * time.Second
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holdForLocked(d)
}

func (c *Client) holdForLocked(d time.Duration) {
	if until := c.now().Add(d); until.After(c.holdUntil) {
		c.holdUntil = until
	}
}

// noteResult counts requests that failed outright in a row, and holds the
// client for failureHold once failureLimit of them have. Any answer resets
// the count.
func (c *Client) noteResult(failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !failed {
		c.failures = 0
		return
	}
	if c.failures++; c.failures >= failureLimit {
		c.failures = 0
		c.holdForLocked(failureHold)
	}
}

// lookup resolves the item and reads the photo and summary. A missing photo
// or summary is not an error; the item lookup failing is. partial reports
// that Commons or Wikipedia failed along the way, so the answer may lack a
// photo or bio it would otherwise have, or carry a bio in a later language.
func (c *Client) lookup(ctx context.Context, property, value string, wikis []string) (profile *metadata.AuthorProfile, partial bool, err error) {
	qid, err := c.findItem(ctx, property, value)
	if err != nil || qid == "" {
		return nil, false, err
	}
	image, sitelinks, err := c.itemFacts(ctx, qid)
	if err != nil {
		return nil, false, err
	}
	profile = &metadata.AuthorProfile{}
	if image != "" {
		if url, src, err := c.commonsImage(ctx, image); err != nil {
			if errors.Is(err, ErrHeld) {
				return nil, false, err
			}
			partial = true
		} else if url != "" {
			profile.ImageURL, profile.ImageSource = url, src
		}
	}
	for _, lang := range wikis {
		title := sitelinks[lang+"wiki"]
		if title == "" {
			continue
		}
		text, page, err := c.wikipediaSummary(ctx, lang, title)
		if errors.Is(err, ErrHeld) {
			return nil, false, err
		}
		if err != nil {
			partial = true
			continue
		}
		if text != "" {
			profile.Description = text
			profile.DescriptionSource = &models.ProfileSource{
				Provider:   "wikipedia",
				URL:        page,
				License:    wikipediaLicense,
				LicenseURL: wikipediaLicenseURL,
			}
			break
		}
	}
	if profile.ImageURL == "" && profile.Description == "" {
		return nil, partial, nil
	}
	return profile, partial, nil
}

// findItem returns the one item holding property=value. Zero or several items
// is no answer: an authority ID shared by two items is ambiguous, and a guess
// would put one person's photo on another.
func (c *Client) findItem(ctx context.Context, property, value string) (string, error) {
	q := url.Values{
		"action":   {"query"},
		"list":     {"search"},
		"srsearch": {fmt.Sprintf(`haswbstatement:%s=%s`, property, value)},
		"srlimit":  {"2"},
		"format":   {"json"},
	}
	var resp struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := c.getJSON(ctx, c.wikidataAPI+"?"+q.Encode(), &resp); err != nil {
		return "", err
	}
	if len(resp.Query.Search) != 1 {
		return "", nil
	}
	return resp.Query.Search[0].Title, nil
}

// itemFacts returns the item's first P18 image file name and its Wikipedia
// sitelinks, keyed by site ("nowiki" -> title).
func (c *Client) itemFacts(ctx context.Context, qid string) (string, map[string]string, error) {
	q := url.Values{
		"action": {"wbgetentities"},
		"ids":    {qid},
		"props":  {"claims|sitelinks"},
		"format": {"json"},
	}
	var resp struct {
		Entities map[string]struct {
			Claims map[string][]struct {
				Mainsnak struct {
					Datavalue struct {
						Value json.RawMessage `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
			Sitelinks map[string]struct {
				Title string `json:"title"`
			} `json:"sitelinks"`
		} `json:"entities"`
	}
	if err := c.getJSON(ctx, c.wikidataAPI+"?"+q.Encode(), &resp); err != nil {
		return "", nil, err
	}
	entity, ok := resp.Entities[qid]
	if !ok {
		return "", nil, nil
	}
	image := ""
	if claims := entity.Claims["P18"]; len(claims) > 0 {
		_ = json.Unmarshal(claims[0].Mainsnak.Datavalue.Value, &image)
	}
	links := make(map[string]string, len(entity.Sitelinks))
	for site, link := range entity.Sitelinks {
		links[site] = link.Title
	}
	return image, links, nil
}

// commonsImage resolves a Commons file to a thumbnail URL and its credit. A
// file whose licence Commons does not state is skipped: without it the photo
// cannot be attributed.
func (c *Client) commonsImage(ctx context.Context, file string) (string, *models.ProfileSource, error) {
	q := url.Values{
		"action":     {"query"},
		"titles":     {"File:" + file},
		"prop":       {"imageinfo"},
		"iiprop":     {"url|extmetadata"},
		"iiurlwidth": {strconv.Itoa(thumbWidth)},
		"format":     {"json"},
	}
	// extmetadata values are usually strings, but some fields carry a
	// number (CommonsMetadataExtension), so each is decoded raw and only the
	// ones read here are turned into text.
	type metaValue struct {
		Value json.RawMessage `json:"value"`
	}
	var resp struct {
		Query struct {
			Pages map[string]struct {
				ImageInfo []struct {
					ThumbURL       string               `json:"thumburl"`
					DescriptionURL string               `json:"descriptionurl"`
					ExtMetadata    map[string]metaValue `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := c.getJSON(ctx, c.commonsAPI+"?"+q.Encode(), &resp); err != nil {
		return "", nil, err
	}
	for _, page := range resp.Query.Pages {
		if len(page.ImageInfo) == 0 {
			continue
		}
		info := page.ImageInfo[0]
		meta := func(field string) string {
			var text string
			if json.Unmarshal(info.ExtMetadata[field].Value, &text) != nil {
				return ""
			}
			return plainText(text)
		}
		license := meta("LicenseShortName")
		if info.ThumbURL == "" || license == "" {
			return "", nil, nil
		}
		return stripTracking(info.ThumbURL), &models.ProfileSource{
			Provider:   "wikimedia-commons",
			URL:        info.DescriptionURL,
			Credit:     meta("Artist"),
			License:    license,
			LicenseURL: meta("LicenseUrl"),
		}, nil
	}
	return "", nil, nil
}

// wikipediaSummary returns the lead summary of one article and its page URL.
// Disambiguation pages have no summary to give.
func (c *Client) wikipediaSummary(ctx context.Context, lang, title string) (string, string, error) {
	endpoint := fmt.Sprintf(c.wikipediaREST, lang) + "/page/summary/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	var resp struct {
		Type        string `json:"type"`
		Extract     string `json:"extract"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return "", "", err
	}
	if resp.Type == "disambiguation" {
		return "", "", nil
	}
	return strings.TrimSpace(resp.Extract), resp.ContentURLs.Desktop.Page, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	// Wikimedia's User-Agent policy asks every client to identify itself
	// with a way to reach its operator.
	req.Header.Set("User-Agent", useragent.Get())
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A cancelled lookup says nothing about Wikimedia.
		if ctx.Err() == nil {
			c.noteResult(true)
		}
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		c.hold(resp.Header.Get("Retry-After"))
		return ErrHeld
	case resp.StatusCode == http.StatusNotFound:
		c.noteResult(false)
		return nil
	case resp.StatusCode != http.StatusOK:
		c.noteResult(true)
		return fmt.Errorf("wikidata: %s answered %d", req.URL.Host, resp.StatusCode)
	}
	c.noteResult(false)
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out)
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText strips the markup Commons puts in its metadata (an Artist value is
// often a link to the photographer's user page) down to the readable text.
func plainText(s string) string {
	s = html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
	return strings.Join(strings.Fields(s), " ")
}

// stripTracking drops the utm_* parameters Commons appends to thumbnail URLs,
// so the stored URL carries no campaign tracking and stays stable for the
// image cache.
func stripTracking(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	for key := range q {
		if strings.HasPrefix(key, "utm_") {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
