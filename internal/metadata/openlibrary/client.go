// Package openlibrary provides the primary book metadata client. It uses
// OpenLibrary's documented public APIs to fetch authors, works, and editions.
package openlibrary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
	"github.com/vavallee/bindery/internal/useragent"
)

// ErrNotFound signals a 404 from OpenLibrary. Callers use errors.Is to
// distinguish "this ISBN/work doesn't exist in the catalog" from genuine
// upstream failures so the UI can show a friendly message.
var ErrNotFound = errors.New("not found")

const (
	baseURL  = "https://openlibrary.org"
	coverURL = "https://covers.openlibrary.org"
)

// editionSampleCap bounds how many editions we fetch per work when deriving
// work-level fields from the edition list. Two derivations share this sample:
// the work language (OpenLibrary works carry none of their own, so the
// foreign-language filter has to sample editions — #891) and a missing cover
// (OL attaches covers to editions far more consistently than to works —
// #1748). The cap is deliberately small: the first handful of editions is
// enough to establish the dominant language and holds the most-held printing's
// cover, and the endpoint is expensive enough that OL throttles per-UA, so we
// keep the round-trip cheap (limit=N) rather than paging the full edition list.
//
// The two derivations used to be separate samplers with separate caches, which
// meant a work missing both language and cover cost TWO round trips to the
// byte-identical URL. An author refresh runs both phases over the same work
// list, so that doubled the pre-loop cost of every refresh (#1888).
const editionSampleCap = 5

// authorWorkSampleConcurrency bounds how many works are edition-sampled at
// once. The sampling loops used to be strictly sequential, so a 65-work author
// paid 65 serial OpenLibrary round trips per phase and a slow (or timing-out)
// OL turned an author refresh into an hour of wall clock (#1888). Four in
// flight is the pace the rest of the codebase already uses for provider
// fan-out (authorAutoSearchConcurrency, seriesFillSearchConcurrency) and stays
// well inside what a single browser would open against the same host.
const authorWorkSampleConcurrency = 4

// authorWorksPageSize is the per-request limit for the /authors/{id}/works
// endpoint, and authorWorksMaxFetch bounds total pagination so an author with
// an enormous (or maliciously inflated) catalogue can't trigger unbounded
// requests. 2000 covers even the most prolific real authors at 20 requests.
const (
	authorWorksPageSize = 100
	authorWorksMaxFetch = 2000
)

// workEditionSample is everything one edition-list sample derives for a work.
// Both fields may be empty: an empty value is a real answer ("the sampled
// editions carry no language / no cover") and is cached as such.
type workEditionSample struct {
	language string
	cover    string
}

// Client implements the metadata.Provider interface for OpenLibrary.
type Client struct {
	http *http.Client

	// workSampleCache memoizes the edition-derived (language, cover) pair keyed
	// on work ID so a later refresh pass — or the second derivation in the same
	// pass, or a second author sharing the work — doesn't re-hit the editions
	// endpoint. A zero-valued entry records a sample that yielded neither, so we
	// don't retry it within the process lifetime.
	workSampleMu    sync.Mutex
	workSampleCache map[string]workEditionSample
}

// New creates a new OpenLibrary client.
func New() *Client {
	return &Client{
		http:            &http.Client{Timeout: 15 * time.Second, Transport: httpsec.DefaultProxyTransport()},
		workSampleCache: map[string]workEditionSample{},
	}
}

func (c *Client) Name() string { return "openlibrary" }

func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	u := fmt.Sprintf("%s/search/authors.json?q=%s&limit=20", baseURL, url.QueryEscape(query))
	var resp authorSearchResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search authors: %w", err)
	}

	authors := make([]models.Author, 0, len(resp.Docs))
	for _, doc := range resp.Docs {
		a := models.Author{
			ForeignID:        doc.Key,
			Name:             doc.Name,
			SortName:         sortName(doc.Name),
			Disambiguation:   doc.TopWork,
			AverageRating:    doc.RatingsAvg,
			RatingsCount:     doc.RatingsCount,
			MetadataProvider: "openlibrary",
			Monitored:        true,
		}
		a.Statistics = &models.AuthorStats{
			BookCount: doc.WorkCount,
		}
		authors = append(authors, a)
	}
	return authors, nil
}

func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	// OpenLibrary's JSON search API is /search.json (now backed by FastAPI).
	// /search (without .json) is the HTML web-UI path (Solr-backed) and
	// returns HTTP 500 "DEPRECATED ENDPOINT ACCESSED" for API consumers
	// since their FastAPI rollout completed (see issue #462, follow-up to #408).
	u := fmt.Sprintf("%s/search.json?q=%s&fields=key,title,author_name,author_key,first_publish_year,cover_i,isbn,subject,ratings_count&limit=20",
		baseURL, url.QueryEscape(query))
	var resp searchResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search books: %w", err)
	}

	books := make([]models.Book, 0, len(resp.Docs))
	for _, doc := range resp.Docs {
		workID := strings.TrimPrefix(doc.Key, "/works/")
		b := models.Book{
			ForeignID:        workID,
			Title:            doc.Title,
			SortTitle:        doc.Title,
			Genres:           truncateSlice(doc.Subject, 10),
			RatingsCount:     doc.RatingsCount,
			ProviderISBNs:    doc.ISBN,
			MetadataProvider: "openlibrary",
			Monitored:        true,
			Status:           models.BookStatusWanted,
		}
		if doc.CoverI != nil {
			b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, *doc.CoverI)
		}
		if doc.FirstPublishYear > 0 {
			t := time.Date(doc.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
			b.ReleaseDate = &t
		}
		if len(doc.AuthorName) > 0 {
			b.Author = &models.Author{
				Name:     doc.AuthorName[0],
				SortName: sortName(doc.AuthorName[0]),
			}
			if len(doc.AuthorKey) > 0 {
				b.Author.ForeignID = doc.AuthorKey[0]
				b.Author.MetadataProvider = "openlibrary"
			}
		}
		books = append(books, b)
	}
	return books, nil
}

func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	// foreignID is like "OL123A"
	u := fmt.Sprintf("%s/authors/%s.json", baseURL, foreignID)
	var resp authorResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get author %s: %w", foreignID, err)
	}

	name := resp.Name
	if name == "" {
		name = resp.PersonalName
	}

	a := &models.Author{
		ForeignID:        foreignID,
		Name:             name,
		SortName:         sortName(name),
		Description:      extractText(resp.Bio),
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}

	if len(resp.Photos) > 0 && resp.Photos[0] > 0 {
		a.ImageURL = fmt.Sprintf("%s/a/id/%d-L.jpg", coverURL, resp.Photos[0])
	}

	a.AlternateNames = resp.AlternateNames

	return a, nil
}

func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	u := fmt.Sprintf("%s/works/%s.json", baseURL, foreignID)
	var resp workResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get book %s: %w", foreignID, err)
	}

	b := &models.Book{
		ForeignID:        foreignID,
		Title:            resp.Title,
		SortTitle:        resp.Title,
		Description:      extractText(resp.Description),
		Genres:           truncateSlice(resp.Subjects, 10),
		MetadataProvider: "openlibrary",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}

	if len(resp.Covers) > 0 && resp.Covers[0] > 0 {
		b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, resp.Covers[0])
	}

	// Parse series membership.
	for _, s := range resp.Series {
		if s == "" {
			continue
		}
		ref, ok := parseSeriesRef(s)
		if !ok {
			continue
		}
		// Primary tracks the first ref actually KEPT, not input index i: if the
		// first entry is dropped for having no usable slug, the next one is the
		// primary series rather than the book having none at all (#1645).
		ref.Primary = len(b.SeriesRefs) == 0
		b.SeriesRefs = append(b.SeriesRefs, ref)
	}

	// Resolve author
	if len(resp.Authors) > 0 {
		authorKey := strings.TrimPrefix(resp.Authors[0].Author.Key, "/authors/")
		author, err := c.GetAuthor(ctx, authorKey)
		if err != nil {
			slog.Warn("failed to resolve author", "key", authorKey, "error", err)
		} else {
			b.Author = author
			b.AuthorID = author.ID
		}
	}

	return b, nil
}

// GetAuthorWorks fetches all works by an author. It merges two OpenLibrary
// endpoints: the /authors/{id}/works endpoint is the primary source because it
// includes series membership data — critical for series reconciliation — and is
// the stable, non-deprecated API; the /search endpoint (new FastAPI version,
// formerly /search.json) is a secondary source that enriches books with
// language, cover image, and first-publish-year metadata not available from the
// works list.
//
// Previously the search index was primary and /authors/{id}/works was a
// backfill. This was reversed when OpenLibrary deprecated /search.json (HTTP
// 500 "DEPRECATED ENDPOINT ACCESSED"), which broke series reconciliation for
// all users (issue #408). The works endpoint now leads; the search endpoint
// enriches when available.
//
// Noise (study guides, screenplay companions, film adaptations, etc.) is
// filtered at this layer so the authors-ingestion pipeline never sees it.
// Both upstream calls are best-effort: as long as one returns, we proceed —
// the other's failure is logged.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	var (
		primary    []authorWorkEntry
		primaryErr error
		enrichment []models.Book
		enrichErr  error
		wg         sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		primary, primaryErr = c.authorWorksBackfill(ctx, authorForeignID)
	}()
	go func() {
		defer wg.Done()
		enrichment, enrichErr = c.searchAuthorWorks(ctx, authorForeignID)
	}()
	wg.Wait()

	if primaryErr != nil {
		slog.Warn("openlibrary: author works endpoint failed", "author", authorForeignID, "error", primaryErr)
	}
	if enrichErr != nil {
		slog.Debug("openlibrary: author search enrichment failed", "author", authorForeignID, "error", enrichErr)
	}
	if primaryErr != nil && enrichErr != nil {
		return nil, fmt.Errorf("get author works %s: primary=%w enrichment=%w", authorForeignID, primaryErr, enrichErr)
	}

	// Build enrichment index: workID → search result for fast lookup.
	enrichIndex := make(map[string]int, len(enrichment))
	for i, b := range enrichment {
		enrichIndex[b.ForeignID] = i
	}

	// index maps workID → position in `books`.
	index := make(map[string]int, len(primary))
	books := make([]models.Book, 0, len(primary)+len(enrichment))

	for _, entry := range primary {
		workID := strings.TrimPrefix(entry.Key, "/works/")
		if workID == "" || entry.Title == "" {
			continue
		}
		if shouldFilterOLNoise(entry.Title, entry.Subjects) {
			continue
		}
		b := models.Book{
			ForeignID:        workID,
			Title:            entry.Title,
			SortTitle:        entry.Title,
			Description:      extractText(entry.Description),
			Genres:           truncateSlice(entry.Subjects, 10),
			SeriesRefs:       seriesRefsFrom(entry.Series),
			MetadataProvider: "openlibrary",
			Monitored:        true,
			Status:           models.BookStatusWanted,
			Author: &models.Author{
				ForeignID:        authorForeignID,
				MetadataProvider: "openlibrary",
			},
		}
		for _, a := range entry.Authors {
			if key := strings.TrimPrefix(a.Author.Key, "/authors/"); key != "" {
				b.CreditedAuthorForeignIDs = append(b.CreditedAuthorForeignIDs, key)
			}
		}
		if len(entry.Covers) > 0 && entry.Covers[0] > 0 {
			b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, entry.Covers[0])
		}
		// Enrich with data the search endpoint carries that works endpoint omits.
		if i, ok := enrichIndex[workID]; ok {
			e := enrichment[i]
			if b.ImageURL == "" && e.ImageURL != "" {
				b.ImageURL = e.ImageURL
			}
			if e.Language != "" {
				b.Language = e.Language
			}
			if b.ReleaseDate == nil {
				b.ReleaseDate = e.ReleaseDate
			}
			if e.RatingsCount > 0 {
				b.RatingsCount = e.RatingsCount
				b.AverageRating = e.AverageRating
			}
			// Older works records sometimes omit the authors array; the
			// search index's author_key list fills the gap (#1405).
			if len(b.CreditedAuthorForeignIDs) == 0 {
				b.CreditedAuthorForeignIDs = e.CreditedAuthorForeignIDs
			}
		}
		index[workID] = len(books)
		books = append(books, b)
	}

	// Append enrichment-only entries: works in the search index that the
	// /authors/{id}/works endpoint hasn't returned (can happen when the works
	// API is paginated or temporarily behind).
	for _, e := range enrichment {
		if _, ok := index[e.ForeignID]; ok {
			continue // already handled above
		}
		if shouldFilterOLNoise(e.Title, e.Genres) {
			continue
		}
		e.Author = &models.Author{
			ForeignID:        authorForeignID,
			MetadataProvider: "openlibrary",
		}
		index[e.ForeignID] = len(books)
		books = append(books, e)
	}

	return books, nil
}

// searchAuthorWorks queries the OL search endpoint for all works by the given
// author. It returns one Book per indexed work, pre-populated with the fields
// the search index exposes (title, language, subjects, cover, first year).
// Series membership is not in the search response — callers get it from
// authorWorksBackfill (the /authors/{id}/works endpoint).
//
// Uses /search.json (FastAPI-backed JSON API). /search without .json is the
// HTML web-UI path still served by Solr, which returns HTTP 500
// "DEPRECATED ENDPOINT ACCESSED" for API consumers (issue #462).
func (c *Client) searchAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	u := fmt.Sprintf("%s/search.json?author_key=%s&fields=key,title,language,edition_count,first_publish_year,cover_i,isbn,subject,ratings_count,ratings_average,author_key&limit=200",
		baseURL, authorForeignID)
	var resp struct {
		Docs []struct {
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			Language         []string `json:"language"`
			EditionCount     int      `json:"edition_count"`
			FirstPublishYear int      `json:"first_publish_year"`
			CoverI           *int     `json:"cover_i"`
			ISBN             []string `json:"isbn"`
			Subject          []string `json:"subject"`
			RatingsCount     int      `json:"ratings_count"`
			RatingsAverage   float64  `json:"ratings_average"`
			AuthorKey        []string `json:"author_key"`
		} `json:"docs"`
	}
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	books := make([]models.Book, 0, len(resp.Docs))
	for _, doc := range resp.Docs {
		workID := strings.TrimPrefix(doc.Key, "/works/")
		if workID == "" || doc.Title == "" {
			continue
		}
		b := models.Book{
			ForeignID:                workID,
			Title:                    doc.Title,
			SortTitle:                doc.Title,
			Genres:                   truncateSlice(doc.Subject, 10),
			Language:                 pickPreferredLanguage(doc.Language),
			RatingsCount:             doc.RatingsCount,
			AverageRating:            doc.RatingsAverage,
			ProviderISBNs:            doc.ISBN,
			MetadataProvider:         "openlibrary",
			Monitored:                true,
			Status:                   models.BookStatusWanted,
			CreditedAuthorForeignIDs: doc.AuthorKey,
		}
		if doc.CoverI != nil && *doc.CoverI > 0 {
			b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, *doc.CoverI)
		}
		if doc.FirstPublishYear > 0 {
			t := time.Date(doc.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
			b.ReleaseDate = &t
		}
		books = append(books, b)
	}
	return books, nil
}

// authorWorksBackfill fetches the author's works list from OpenLibrary's
// /authors/{id}/works endpoint. The raw entries are returned so the caller
// can decide how to merge them with the primary search-index results.
//
// The endpoint is paginated: it returns at most authorWorksPageSize entries
// per call and advertises the catalogue total in the response's Size field.
// Previously only the first page was fetched, so authors with more than one
// page of works had their catalogue silently truncated (issue: users seeing a
// hard cap of 100 books on prolific authors). We now page through until the
// catalogue is exhausted, bounded by authorWorksMaxFetch to keep pathological
// or maliciously large responses from running away.
func (c *Client) authorWorksBackfill(ctx context.Context, authorForeignID string) ([]authorWorkEntry, error) {
	var entries []authorWorkEntry
	for offset := 0; offset < authorWorksMaxFetch; offset += authorWorksPageSize {
		u := fmt.Sprintf("%s/authors/%s/works.json?limit=%d&offset=%d",
			baseURL, authorForeignID, authorWorksPageSize, offset)
		var resp authorWorksResponse
		if err := c.getJSON(ctx, u, &resp); err != nil {
			if offset == 0 {
				return nil, err
			}
			// A later page failing shouldn't discard the works already
			// collected — return what we have and let enrichment fill gaps.
			slog.Warn("openlibrary: author works pagination stopped early",
				"author", authorForeignID, "offset", offset, "error", err)
			break
		}
		entries = append(entries, resp.Entries...)
		// Stop on a short page (end of list) or once we've collected the
		// advertised total. Size is omitted (0) by older/partial responses,
		// in which case the short-page check is the authoritative terminator.
		if len(resp.Entries) < authorWorksPageSize ||
			(resp.Size > 0 && len(entries) >= resp.Size) {
			break
		}
	}
	if len(entries) >= authorWorksMaxFetch {
		slog.Warn("openlibrary: author works hit pagination cap; catalogue may be truncated",
			"author", authorForeignID, "cap", authorWorksMaxFetch)
	}
	return entries, nil
}

// pickPreferredLanguage returns "eng" if present in the list, otherwise the
// first entry. Empty list returns "". Books with an empty language still pass
// through the profile language filter via the unknown-language fallback.
func pickPreferredLanguage(langs []string) string {
	if len(langs) == 0 {
		return ""
	}
	if slices.Contains(langs, "eng") {
		return "eng"
	}
	return langs[0]
}

// seriesRefsFrom parses the OL series strings attached to a works-endpoint
// entry into SeriesRefs, flagging the first KEPT entry Primary. Entries whose
// title yields no usable slug are dropped rather than sharing a ForeignID
// (#1645).
func seriesRefsFrom(series []string) []models.SeriesRef {
	if len(series) == 0 {
		return nil
	}
	refs := make([]models.SeriesRef, 0, len(series))
	for _, s := range series {
		if s == "" {
			continue
		}
		ref, ok := parseSeriesRef(s)
		if !ok {
			continue
		}
		// See GetBook: primary is the first ref KEPT, not input index (#1645).
		ref.Primary = len(refs) == 0
		refs = append(refs, ref)
	}
	return refs
}

// olNoiseSubjects are case-insensitive substrings in an OL work's subjects
// array that signal companion material, criticism, or adaptations rather
// than a primary authored work.
var olNoiseSubjects = []string{
	"study guides",
	"study and teaching",
	"literary criticism",
	"criticism and interpretation",
	"cliffsnotes",
	"sparknotes",
	"motion picture adaptations",
	"film adaptations",
	"television adaptations",
	"screenplays",
	// "history and criticism" is the LoC subject phrase OL attaches to
	// secondary works (companion guides, art books, critical studies) about
	// an author rather than by them — distinct from "literary criticism" /
	// "criticism and interpretation" above, which use different word order
	// and were confirmed (via a real author's catalogue) not to substring-match
	// it.
	"history and criticism",
	// "authors, biography" is OL's subject tag for a biography written about
	// an author. Deliberately not bare "biography": that substring also
	// appears inside "autobiography", which is legitimately self-authored
	// and must not be filtered.
	"authors, biography",
}

// olNoiseTitleFragments are case-insensitive substrings in a work title that
// flag summaries, study guides, or audio-only physical editions that OL
// sometimes represents as separate Works rather than editions.
var olNoiseTitleFragments = []string{
	"summary and analysis",
	"summary & analysis",
	"summary of",
	"study guide",
	"reader's guide",
	"reading guide",
	"teacher's guide",
	"cliffsnotes",
	"sparknotes",
	"supersummary",
	"instaread",
	"workbook",
	"audio cd",
}

// shouldFilterOLNoise returns true when an OpenLibrary work looks like
// companion material (study guide, summary, adaptation, audio-CD edition)
// rather than a real authored work. The goal is to keep an author's
// catalogue clean without being aggressive enough to drop legitimate works.
func shouldFilterOLNoise(title string, subjects []string) bool {
	lt := strings.ToLower(title)
	for _, f := range olNoiseTitleFragments {
		if strings.Contains(lt, f) {
			return true
		}
	}
	for _, s := range subjects {
		ls := strings.ToLower(s)
		for _, n := range olNoiseSubjects {
			if strings.Contains(ls, n) {
				return true
			}
		}
	}
	return false
}

func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	u := fmt.Sprintf("%s/works/%s/editions.json?limit=50", baseURL, bookForeignID)
	var resp editionsResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get editions for %s: %w", bookForeignID, err)
	}

	editions := make([]models.Edition, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		editionID := strings.TrimPrefix(e.Key, "/books/")
		ed := models.Edition{
			ForeignID: editionID,
			Title:     e.Title,
			Publisher: first(e.Publishers),
			Format:    e.PhysicalFormat,
			NumPages:  nilIfZero(e.NumberOfPages),
			Monitored: true,
		}
		if len(e.ISBN13) > 0 {
			ed.ISBN13 = &e.ISBN13[0]
		}
		if len(e.ISBN10) > 0 {
			ed.ISBN10 = &e.ISBN10[0]
		}
		if len(e.Languages) > 0 {
			ed.Language = strings.TrimPrefix(e.Languages[0].Key, "/languages/")
		}
		if len(e.Covers) > 0 && e.Covers[0] > 0 {
			ed.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, e.Covers[0])
		}
		format := strings.ToLower(ed.Format)
		ed.IsEbook = strings.Contains(format, "ebook") || strings.Contains(format, "kindle")
		editions = append(editions, ed)
	}
	return editions, nil
}

// FillMissingWorkLanguages derives a language for any book that arrived with an
// empty Language by sampling its OpenLibrary editions. OL works carry no
// language at the work level; the /search.json enricher only backfills language
// for works it indexes, so a tail of works (often translations) reach the
// allowed_languages filter with Language="" and pass through the
// unknown-language fallback (#891).
//
// For each such book it fetches /works/{id}/editions.json?limit=N (N capped by
// editionSampleCap) and takes the majority edition language. Results are
// memoized per work ID so refresh passes — and the cover derivation, which
// reads the same sample — don't re-fetch. The mutation is done in place.
// Per-work fetch failures are logged at debug and leave Language="" so the
// caller's unknown-language behavior still applies.
//
// Sampling runs authorWorkSampleConcurrency works at a time: each book is
// written by exactly one goroutine and the sample cache is mutex-guarded, so
// the fan-out is a pure latency win over the previous strictly-serial loop
// (#1888).
//
// Callers should only invoke this when the active metadata profile actually
// restricts language (allowed_languages != "any"); there is no point spending
// the editions round-trips when the filter would pass everything anyway.
// It returns the number of books whose Language was populated from sampling so
// callers can surface it in their filter-decision logs.
func (c *Client) FillMissingWorkLanguages(ctx context.Context, books []models.Book) int {
	targets := make([]int, 0, len(books))
	for i := range books {
		if books[i].Language != "" || books[i].ForeignID == "" {
			continue
		}
		targets = append(targets, i)
	}

	var filled atomic.Int64
	concurrency.RunBounded(ctx, targets, authorWorkSampleConcurrency, func(ctx context.Context, i int) {
		if lang := c.sampleWorkEditions(ctx, books[i].ForeignID).language; lang != "" {
			books[i].Language = lang
			filled.Add(1)
		}
	})
	return int(filled.Load())
}

// sampleWorkEditions returns the language and cover derived from a work's first
// editionSampleCap editions. Results (including the all-empty result) are cached
// per work ID, so the language and cover derivations cost ONE round trip between
// them rather than one each (#1888).
func (c *Client) sampleWorkEditions(ctx context.Context, workID string) workEditionSample {
	if sample, ok := c.cachedWorkSample(workID); ok {
		return sample
	}

	u := fmt.Sprintf("%s/works/%s/editions.json?limit=%d", baseURL, workID, editionSampleCap)
	var resp editionsResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		slog.Debug("openlibrary: edition sampling failed", "work", workID, "error", err)
		// Cache the miss so we don't retry a flaky/expensive call this run.
		c.setCachedWorkSample(workID, workEditionSample{})
		return workEditionSample{}
	}

	sample := workEditionSample{
		language: majorityEditionLanguage(resp.Entries),
		cover:    firstEditionCover(resp.Entries),
	}
	c.setCachedWorkSample(workID, sample)
	return sample
}

// majorityEditionLanguage returns the most common language across the sampled
// editions, preferring "eng" on a tie so an author's English work isn't
// reclassified by a couple of foreign editions. Empty languages are ignored;
// "" is returned when no edition carries a language.
func majorityEditionLanguage(entries []editionEntry) string {
	counts := map[string]int{}
	for _, e := range entries {
		for _, l := range e.Languages {
			code := strings.TrimPrefix(l.Key, "/languages/")
			if code != "" {
				counts[code]++
			}
		}
	}
	if len(counts) == 0 {
		return ""
	}
	best := ""
	bestN := 0
	for code, n := range counts {
		if n > bestN || (n == bestN && code == "eng") {
			best, bestN = code, n
		}
	}
	return best
}

func (c *Client) cachedWorkSample(workID string) (workEditionSample, bool) {
	if c.workSampleCache == nil {
		return workEditionSample{}, false
	}
	c.workSampleMu.Lock()
	defer c.workSampleMu.Unlock()
	sample, ok := c.workSampleCache[workID]
	return sample, ok
}

func (c *Client) setCachedWorkSample(workID string, sample workEditionSample) {
	if c.workSampleCache == nil {
		return
	}
	c.workSampleMu.Lock()
	defer c.workSampleMu.Unlock()
	c.workSampleCache[workID] = sample
}

// FillMissingWorkCovers backfills a cover for any book that arrived with an
// empty ImageURL by sampling its OpenLibrary editions. Work records frequently
// carry no cover even when their editions do (#1748); the /search.json and
// /works enrichers only ever read the work-level cover, so those books reach
// the UI with image_url="". For each such book it fetches
// /works/{id}/editions.json?limit=N (N capped by editionSampleCap) and takes
// the first edition that has a cover. Results are memoized per work ID
// (including misses) and shared with the language derivation, so a refresh that
// runs both phases samples each work once (#1888). The mutation is in place and
// runs authorWorkSampleConcurrency works at a time.
//
// Only OpenLibrary works are sampled: a book merged in from another provider
// (Hardcover "hc:", DNB "dnb:", Audible "audible:") has no OL editions endpoint
// and would 404, so it is skipped. It returns the number of books whose cover
// was populated.
func (c *Client) FillMissingWorkCovers(ctx context.Context, books []models.Book) int {
	targets := make([]int, 0, len(books))
	for i := range books {
		if books[i].ImageURL != "" || books[i].ForeignID == "" {
			continue
		}
		if p := books[i].MetadataProvider; p != "" && p != "openlibrary" {
			continue
		}
		targets = append(targets, i)
	}

	var filled atomic.Int64
	concurrency.RunBounded(ctx, targets, authorWorkSampleConcurrency, func(ctx context.Context, i int) {
		if cover := c.sampleWorkEditions(ctx, books[i].ForeignID).cover; cover != "" {
			books[i].ImageURL = cover
			filled.Add(1)
		}
	})
	return int(filled.Load())
}

// firstEditionCover returns the cover URL of the first edition that has one, in
// the order OpenLibrary returns them (most-held printings first), or "" when no
// sampled edition carries a cover.
func firstEditionCover(entries []editionEntry) string {
	for _, e := range entries {
		if len(e.Covers) > 0 && e.Covers[0] > 0 {
			return fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, e.Covers[0])
		}
	}
	return ""
}

// GetSubjectBooks fetches the top books for an OpenLibrary subject.
// subject should be a lowercase slug using underscores, e.g. "science_fiction" or "fantasy".
// Returns candidates suitable for use as genre-popular recommendations.
func (c *Client) GetSubjectBooks(ctx context.Context, subject string, limit int) ([]models.RecommendationCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	u := fmt.Sprintf("%s/subjects/%s.json?limit=%d", baseURL, url.PathEscape(subject), limit)
	var resp subjectBooksResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("get subject books %q: %w", subject, err)
	}

	candidates := make([]models.RecommendationCandidate, 0, len(resp.Works))
	for _, w := range resp.Works {
		workID := strings.TrimPrefix(w.Key, "/works/")
		cand := models.RecommendationCandidate{
			ForeignID: workID,
			Title:     w.Title,
			Genres:    truncateSlice(w.Subject, 10),
			MediaType: models.MediaTypeEbook,
		}
		if w.CoverID != nil && *w.CoverID > 0 {
			cand.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, *w.CoverID)
		}
		if w.FirstPublishYear > 0 {
			t := time.Date(w.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
			cand.ReleaseDate = &t
		}
		if len(w.Authors) > 0 {
			cand.AuthorName = w.Authors[0].Name
		}
		candidates = append(candidates, cand)
	}
	return candidates, nil
}

func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	u := fmt.Sprintf("%s/isbn/%s.json", baseURL, isbn)
	var resp isbnResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		// Treat 404 as "no such ISBN" rather than an upstream error so the
		// API layer can respond with a friendly message (issue #284).
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("isbn lookup %s: %w", isbn, err)
	}

	// If we have a work reference, get the full work
	if len(resp.Works) > 0 {
		workID := strings.TrimPrefix(resp.Works[0].Key, "/works/")
		return c.GetBook(ctx, workID)
	}

	// Fallback: construct from ISBN response
	b := &models.Book{
		ForeignID:        strings.TrimPrefix(resp.Key, "/books/"),
		Title:            resp.Title,
		SortTitle:        resp.Title,
		MetadataProvider: "openlibrary",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}
	if len(resp.Covers) > 0 && resp.Covers[0] > 0 {
		b.ImageURL = fmt.Sprintf("%s/b/id/%d-L.jpg", coverURL, resp.Covers[0])
	}
	return b, nil
}

// getJSONMaxRetries bounds how many times a retryable failure (429, 502, 503,
// 504, or a transport-level error) is retried before getJSON gives up.
// OpenLibrary throttles per-UA (see editionSampleCap's comment) and a bulk
// author import used to storm it with dozens of concurrent requests and no
// backoff at all — #2075 reproduced 429s escalating into timeouts and
// connection refusals with every request retried instantly forever.
const getJSONMaxRetries = 3

// getJSONBaseDelay and getJSONMaxDelay bound the exponential backoff applied
// between retries when the response carries no Retry-After header.
const (
	getJSONBaseDelay = 500 * time.Millisecond
	getJSONMaxDelay  = 8 * time.Second
)

// retryAfterCap bounds how long a single Retry-After value is honored for.
// OpenLibrary is expected to send small values; capping defensively means a
// malformed or unexpectedly large header can't stall a request indefinitely.
const retryAfterCap = 30 * time.Second

// maxDrainBytes bounds how much of a retryable error response's body gets
// read (beyond the 512 bytes already sampled for the error message) purely
// to let the connection be reused. OpenLibrary error bodies are small JSON
// objects; this is generous headroom, not an expected size.
const maxDrainBytes = 1 << 20 // 1 MiB

func (c *Client) getJSON(ctx context.Context, rawURL string, target interface{}) error {
	var lastErr error
	var retryAfter time.Duration

	for attempt := 0; attempt <= getJSONMaxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepForRetry(ctx, backoffDelay(attempt, retryAfter)); err != nil {
				return err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", useragent.Get())
		req.Header.Set("Accept", "application/json")

		resp, doErr := c.http.Do(req)
		if doErr != nil {
			// Wrap with %w so the error chain survives: callers (and tests) rely on
			// errors.Is(err, context.Canceled)/context.DeadlineExceeded to classify
			// cancellations and timeouts. OpenLibrary is a keyless API — every
			// request URL is built from public path/query params (no key/token/auth),
			// so the transport error's embedded URL carries no secret to redact.
			// (#1144 added RedactSecrets here, but it matched nothing in OL URLs and
			// only flattened the chain, breaking errors.Is classification.)
			wrapped := fmt.Errorf("openlibrary request: %w", doErr)
			// A canceled operation is never worth retrying, whatever canceled it.
			// ctx.Err() != nil additionally means the CALLER's own deadline fired
			// (as opposed to just this one request's timeout) — respect that
			// budget too rather than retrying into a context that's already gone.
			if errors.Is(doErr, context.Canceled) || ctx.Err() != nil || attempt == getJSONMaxRetries {
				return wrapped
			}
			lastErr = wrapped
			retryAfter = 0
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return ErrNotFound
		}
		if resp.StatusCode == http.StatusOK {
			err := json.NewDecoder(resp.Body).Decode(target)
			_ = resp.Body.Close()
			return err
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// Drain and discard whatever's left (bounded) before closing. Go's
		// transport only returns a connection to its pool for reuse once the
		// body has been read to EOF; closing after a partial read forces a
		// fresh TCP connection on the very next retry — leaning into the
		// exact "connection refused" symptom #2075 reported under a storm of
		// retried requests.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
		statusErr := fmt.Errorf("HTTP %d: %s", resp.StatusCode, httpsec.RedactSecrets(string(body)))

		if !isRetryableStatus(resp.StatusCode) || attempt == getJSONMaxRetries {
			return statusErr
		}
		lastErr = statusErr
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	// Unreachable: every iteration above returns on its last attempt. Kept so
	// the compiler doesn't need to prove that itself.
	return lastErr
}

// isRetryableStatus reports whether status is a transient upstream failure
// worth retrying rather than a real answer (a 4xx other than 429 means the
// request itself is wrong, not that OpenLibrary is struggling).
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// parseRetryAfter reads an HTTP Retry-After header, accepting either a
// delay-in-seconds or an HTTP-date, and returns 0 when absent or unparsable
// (the caller falls back to computed backoff in that case).
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > retryAfterCap {
			return retryAfterCap
		}
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			if d > retryAfterCap {
				return retryAfterCap
			}
			return d
		}
	}
	return 0
}

// backoffDelay picks how long to wait before the given retry attempt
// (1-indexed: the first retry is attempt 1). retryAfter, when positive, wins
// outright — the server told us exactly how long to wait. Otherwise this
// backs off exponentially from getJSONBaseDelay, capped at getJSONMaxDelay,
// with equal jitter (half the computed delay, plus a random amount up to the
// other half) so a burst of requests that all failed together don't all
// retry in lockstep and produce a second burst.
func backoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := getJSONBaseDelay << uint(attempt-1) //nolint:gosec // attempt is bounded by getJSONMaxRetries, no overflow risk
	if d <= 0 || d > getJSONMaxDelay {
		d = getJSONMaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1)) //nolint:gosec // backoff jitter, not security-sensitive
}

// sleepForRetry waits for d, returning early with a wrapped ctx error if the
// context is canceled or expires first.
func sleepForRetry(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("openlibrary request: %w", ctx.Err())
	}
}

// extractText handles OpenLibrary's description field which can be a string
// or an object like {"type": "/type/text", "value": "..."}.
func extractText(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]interface{}:
		if val, ok := t["value"]; ok {
			if s, ok := val.(string); ok {
				return s
			}
		}
	}
	return ""
}

func sortName(name string) string {
	return textutil.SortName(name)
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func truncateSlice(s []string, max int) []string {
	if s == nil {
		return []string{}
	}
	if len(s) > max {
		return s[:max]
	}
	return s
}

// rePoundPos matches a series position suffix like "#1" or "#1.5".
var rePoundPos = regexp.MustCompile(`\s*#(\d+(?:\.\d+)?)\s*$`)

// reBookPos matches variants like ", Book 1", " -- Book 2", " Book 3".
var reBookPos = regexp.MustCompile(`(?:,?\s*-{1,2}\s*|,\s*|\s+)[Bb]ook\s+(\d+(?:\.\d+)?)\s*$`)

// parseSeriesRef parses an OpenLibrary series string (e.g. "Dune Chronicles #1")
// into a SeriesRef with a stable ForeignID slug, extracted title, and position.
//
// Reports ok=false when the title yields no usable slug, in which case the
// caller must DROP the ref. Emitting a prefix-only "ol-series:" ForeignID would
// make every such series collide onto one row (#1645).
func parseSeriesRef(raw string) (models.SeriesRef, bool) {
	title := strings.TrimSpace(raw)
	position := ""

	if m := rePoundPos.FindStringSubmatchIndex(title); m != nil {
		position = title[m[2]:m[3]]
		title = strings.TrimSpace(title[:m[0]])
	} else if m := reBookPos.FindStringSubmatchIndex(title); m != nil {
		position = title[m[2]:m[3]]
		title = strings.TrimSpace(title[:m[0]])
	}

	slug := seriesSlug(title)
	if slug == "" {
		return models.SeriesRef{}, false
	}
	return models.SeriesRef{
		ForeignID: "ol-series:" + slug,
		Title:     title,
		Position:  position,
	}, true
}

// seriesSlug converts a series title to a lowercase slug suitable for use as a
// foreign_id (e.g. "Dune Chronicles" → "dune-chronicles").
// seriesSlug builds the stable identity part of a series ForeignID.
//
// It NFD-decomposes and drops combining marks, so accented Latin folds to its
// base letters ("Ödland" → "odland"), then keeps Unicode letters and numbers
// and collapses everything else to a single "-".
//
// Both of those matter (#1645). The previous version kept only ASCII [a-z0-9]
// and dropped every other rune, which produced two distinct failures:
//
//   - A title with NO ASCII alphanumerics slugged to the empty string, so the
//     ForeignID degenerated to the bare prefix "ol-series:". Since
//     SeriesRepo.CreateOrGet is INSERT OR IGNORE keyed on foreign_id, the first
//     non-Latin series to arrive claimed that row and every later Japanese,
//     Chinese, Russian, Greek, Hebrew or Arabic series bound to it — one row
//     holding books from every unrelated series.
//   - Accented Latin silently collided: "Ödland" and "Ådland" both dropped
//     their leading rune and slugged to "dland".
//
// Keeping \p{L}/\p{N} rather than transliterating means "三体" stays "三体".
// That is fine for an identity key — it only has to be stable and distinct, not
// readable — and it is what makes non-Latin series distinguishable at all.
//
// Returns "" when the title carries no letters or digits at all (pure
// punctuation or emoji). Callers MUST treat that as "no usable identity" and
// skip the series rather than emitting a prefix-only ForeignID.
func seriesSlug(title string) string {
	var b strings.Builder
	prevDash := false
	// textutil.FoldForSlug has already NFC-composed, lowercased, and stripped
	// diacritics from Latin and Greek. Marks that survive it belong to scripts
	// where they are letters rather than accents (kana dakuten, Devanagari
	// vowel signs, Hebrew niqqud), so they count as word characters here —
	// treating them as separators shatters one title into fragments and lets
	// unrelated titles collide, which is what this slug exists to prevent.
	for _, r := range textutil.FoldForSlug(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) ||
			unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
