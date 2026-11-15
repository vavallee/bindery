// Package ranobedb is an opt in book enricher for light novels (#2012). It
// asks RanobeDB (https://ranobedb.org) for a book's official English title.
// Only the title is written, and only for a match that passes three checks:
// RanobeDB's own ranking must have a clear winner, the winner must carry the
// same volume numbers as the book, and the book's author must match one of the
// winner's authors.
//
// It is opt in. An install that has not switched it on sends RanobeDB no
// request at all: building the client sends nothing.
//
// RanobeDB's data is ODbL and its terms allow non commercial use; see
// docs/third-party-data.md.
package ranobedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/models"
)

const (
	providerName = "ranobedb"
	apiBase      = "https://ranobedb.org/api/v0"

	// RanobeDB asks API clients to stay under 60 requests a minute. One
	// request starts per second at most, one at a time, so the whole process
	// stays at that ceiling however many lookups run at once. A match costs
	// two requests (search, then the book for its authors), so this is about
	// 30 books a minute.
	requestInterval = time.Second

	// searchLimit is how many results a search asks for. Two would decide
	// the tie-break; a few more costs nothing and keeps the decoded ranking
	// honest if RanobeDB ever returns results out of order.
	searchLimit = 5

	maxResponseBytes = 2 << 20
	requestTimeout   = 20 * time.Second
)

// defaultGate holds every RanobeDB request in the process while RanobeDB is
// refusing (429 or 503), and defaultPacer spaces them; both are shared by
// every client New returns.
var (
	defaultGate  = providerhttp.NewGate()
	defaultPacer = &pacer{interval: requestInterval, slot: make(chan struct{}, 1)}
)

// Client implements metadata.Provider. Only SearchBooks answers; every other
// method reports nothing found, since this is an enricher and never a primary.
type Client struct {
	http *http.Client
	gate *providerhttp.Gate
	// base is the API root; tests point it at a fake server.
	base string
}

// New returns a client for the public RanobeDB API.
//
// User-Agent: requests go out with Bindery's own User-Agent, set by
// providerhttp.Do. Only RanobeDB's website refuses non browser clients; the
// API answered Bindery's User-Agent, Go's default and curl's alike when this
// was written (2026-11). Do not switch to a browser User-Agent to "fix" a
// refusal: if the API ever starts refusing Bindery, that is RanobeDB asking
// us to stop, and the answer is to ask them, not to disguise the client.
func New() *Client {
	return &Client{
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: &pacedTransport{base: httpsec.DefaultProxyTransport(), pacer: defaultPacer},
		},
		gate: defaultGate,
		base: apiBase,
	}
}

// Name implements metadata.Provider.
func (c *Client) Name() string { return providerName }

// SearchBooks implements metadata.Provider. The aggregator calls it with the
// book's title. It returns at most one book: RanobeDB's clear winner for that
// title, carrying the official English title and the authors, or nothing.
//
// The winner must score strictly higher than the runner up. RanobeDB's
// sim_score only separates volumes when the volume is in the query ("spice and
// wolf" scores 1 for all 24 volumes, "spice and wolf vol 3" scores 1 for
// volume 3 and 0.905 for the rest), so a tie means the title alone cannot tell
// the books apart, and is no match.
func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	if c == nil || c.http == nil {
		return nil, metadata.ErrProviderNotConfigured
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var search searchResponse
	endpoint := fmt.Sprintf("%s/books?q=%s&limit=%d", c.base, url.QueryEscape(query), searchLimit)
	if err := c.get(ctx, endpoint, &search); err != nil {
		return nil, err
	}
	winner, ok := clearWinner(search.Books)
	if !ok {
		return nil, nil
	}
	// The volume guard: a book's own numbers must all be in the winner's title
	// and no others. The aggregator's title match is a substring test, under
	// which "Vol. 1" sits inside "Vol. 10" both ways round, so it cannot be
	// left to catch a wrong volume.
	if !sameNumbers(query, winner.Title) {
		return nil, nil
	}

	var detail bookResponse
	if err := c.get(ctx, fmt.Sprintf("%s/book/%d", c.base, winner.ID), &detail); err != nil {
		return nil, err
	}
	title := englishTitle(detail.Book)
	authors := authorNames(detail.Book)
	if title == "" || len(authors) == 0 {
		// No official English title is nothing to write, and no author means
		// the aggregator's author check could not run: refuse rather than
		// match on the title alone.
		return nil, nil
	}
	out := make([]models.Book, 0, len(authors))
	for _, name := range authors {
		// One candidate per author, so the aggregator's author check can
		// accept a book credited to any of them. Every candidate is the same
		// RanobeDB book and carries the same title.
		out = append(out, models.Book{
			ForeignID:        "ranobedb:" + strconv.Itoa(detail.Book.ID),
			Title:            title,
			MetadataProvider: providerName,
			Author:           &models.Author{Name: name},
		})
	}
	return out, nil
}

// SearchAuthors implements metadata.Provider; this enricher does not search authors.
func (c *Client) SearchAuthors(context.Context, string) ([]models.Author, error) { return nil, nil }

// GetAuthor implements metadata.Provider; this enricher does not fetch authors.
func (c *Client) GetAuthor(context.Context, string) (*models.Author, error) { return nil, nil }

// GetBook implements metadata.Provider; this enricher is never a primary.
func (c *Client) GetBook(context.Context, string) (*models.Book, error) { return nil, nil }

// GetEditions implements metadata.Provider; this enricher is never a primary.
func (c *Client) GetEditions(context.Context, string) ([]models.Edition, error) { return nil, nil }

// GetBookByISBN implements metadata.Provider. ISBN lookup is left for a later
// change (#2012): a RanobeDB book has several releases, each with its own
// ISBN, and which one to take needs settling with real data first.
func (c *Client) GetBookByISBN(context.Context, string) (*models.Book, error) { return nil, nil }

type searchResponse struct {
	Books []searchResult `json:"books"`
}

type searchResult struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"`
	SimScore float64 `json:"sim_score"`
}

type bookResponse struct {
	Book bookDetail `json:"book"`
}

type bookDetail struct {
	ID       int       `json:"id"`
	Titles   []title   `json:"titles"`
	Editions []edition `json:"editions"`
}

type title struct {
	Lang     string `json:"lang"`
	Official bool   `json:"official"`
	Title    string `json:"title"`
}

type edition struct {
	Staff []staff `json:"staff"`
}

type staff struct {
	RoleType string `json:"role_type"`
	Name     string `json:"name"`
	Romaji   string `json:"romaji"`
}

// clearWinner returns the highest scoring result when it scores strictly
// higher than every other result.
func clearWinner(results []searchResult) (searchResult, bool) {
	if len(results) == 0 {
		return searchResult{}, false
	}
	best := 0
	for i := 1; i < len(results); i++ {
		if results[i].SimScore > results[best].SimScore {
			best = i
		}
	}
	for i := range results {
		if i != best && results[i].SimScore >= results[best].SimScore {
			return searchResult{}, false
		}
	}
	return results[best], true
}

var digitRun = regexp.MustCompile(`[0-9]+`)

// sameNumbers reports whether a and b carry the same numbers, as sets, with
// leading zeros ignored ("Vol. 03" is volume 3). No numbers on either side
// counts as the same.
func sameNumbers(a, b string) bool {
	return slices.Equal(numbers(a), numbers(b))
}

func numbers(s string) []string {
	var out []string
	for _, n := range digitRun.FindAllString(s, -1) {
		n = strings.TrimLeft(n, "0")
		if n == "" {
			n = "0"
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// englishTitle is the book's official English title, or "" when RanobeDB has
// none. The book's display title is not used: for a volume never licensed in
// English it is the Japanese one.
func englishTitle(b bookDetail) string {
	for _, t := range b.Titles {
		if t.Official && t.Lang == "en" {
			return strings.TrimSpace(t.Title)
		}
	}
	return ""
}

// authorNames lists the book's credited authors, each once, as Bindery can
// compare them.
//
// RanobeDB gives a Japanese author's name in kanji, with the romanisation
// alongside in Japanese order, family name first: 支倉凍砂 is "Hasekura
// Isuna", where Bindery's providers have "Isuna Hasekura". The name is
// returned as "Hasekura, Isuna": the comma tells the aggregator's author
// matcher which word is the family name, so it matches either order. A plain
// "Hasekura Isuna" would only match as an ambiguous reordering, which the
// enrichment check rightly refuses ("Stanley Paul" is not "Paul Stanley").
//
// Only a romanisation of exactly two words is reordered; a one word pen name
// has no order and a longer one cannot be split safely, so both are kept as
// given. A name with no romanisation is already in Latin script (a Western
// author) and is kept as given.
func authorNames(b bookDetail) []string {
	var out []string
	for _, e := range b.Editions {
		for _, s := range e.Staff {
			if s.RoleType != "author" {
				continue
			}
			name := strings.TrimSpace(s.Name)
			if romaji := strings.Fields(s.Romaji); len(romaji) == 2 {
				name = romaji[0] + ", " + romaji[1]
			} else if len(romaji) > 0 {
				name = strings.Join(romaji, " ")
			}
			if name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// get GETs endpoint and decodes the size limited JSON body into out. The
// shared provider request loop retries a 429 or 5xx with backoff and holds
// every RanobeDB request while one is refused. What outlives the retries is
// marked for the aggregator: a 429 or a local pacing refusal as
// providererr.ErrRateLimited, a 5xx or a network failure as
// providererr.ErrUnavailable.
func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		Provider: providerName,
		URL:      endpoint,
		Header:   http.Header{"Accept": {"application/json"}},
	})
	if err != nil {
		return classify(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode ranobedb response: %w", err)
	}
	return nil
}

func classify(err error) error {
	var statusErr *providerhttp.StatusError
	var heldErr *providerhttp.HeldError
	switch {
	case errors.Is(err, errPaced):
		return fmt.Errorf("%w: %w", providererr.ErrRateLimited, err)
	case errors.As(err, &statusErr), errors.As(err, &heldErr), errors.Is(err, context.Canceled):
		return err
	default:
		return fmt.Errorf("%w: %w", providererr.ErrUnavailable, err)
	}
}

// errPaced reports that waiting for the pacing would run past the caller's
// deadline; failing at once reports a (local) rate limit rather than letting
// the caller time out and read it as an outage.
var errPaced = errors.New("ranobedb: waiting for the request pacing would pass the caller's deadline")

// pacer lets one request run at a time and starts them at least interval
// apart. The slot is held until the response body is closed.
type pacer struct {
	interval time.Duration
	slot     chan struct{}

	mu   sync.Mutex
	next time.Time
}

func (p *pacer) acquire(ctx context.Context) (func(), error) {
	select {
	case p.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-p.slot }) }

	p.mu.Lock()
	start := time.Now()
	if p.next.After(start) {
		start = p.next
	}
	if deadline, ok := ctx.Deadline(); ok && start.After(deadline) {
		p.mu.Unlock()
		release()
		return nil, errPaced
	}
	p.next = start.Add(p.interval)
	p.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

type pacedTransport struct {
	base  http.RoundTripper
	pacer *pacer
}

func (t *pacedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.pacer.acquire(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = &releasingBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type releasingBody struct {
	io.ReadCloser
	release func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
