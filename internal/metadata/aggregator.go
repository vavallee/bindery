// Package metadata aggregates book and author data from multiple public
// sources (OpenLibrary, Google Books, Hardcover) behind a unified interface.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata/audible"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// Aggregator fans out requests to multiple providers and merges results.
// OpenLibrary is always the primary source. Other providers enrich the data.
type Aggregator struct {
	primary   Provider
	enrichers []Provider
	audnex    AudnexBookClient
	audible   *audible.Client
	cache     *ttlCache
}

// AudnexBookClient is the narrow audnex capability the aggregator needs for
// ASIN-based audiobook metadata lookup.
type AudnexBookClient interface {
	GetBook(ctx context.Context, asin string) (*audnex.Book, error)
}

// NewAggregator creates an aggregator with OpenLibrary as primary and optional enrichers.
func NewAggregator(primary Provider, enrichers ...Provider) *Aggregator {
	return &Aggregator{
		primary:   primary,
		enrichers: enrichers,
		audnex:    audnex.New(""),
		audible:   audible.New(),
		cache:     newTTLCache(24 * time.Hour),
	}
}

// WithAudnexClient replaces the default audnex client. Tests use this to keep
// ASIN canonicalization deterministic without reaching the network.
func (a *Aggregator) WithAudnexClient(client AudnexBookClient) *Aggregator {
	a.audnex = client
	return a
}

// SearchAuthors queries the primary provider and every enricher in parallel,
// then merges: same-person records (by canonical name, treating "Last, First"
// and "First Last" as equal) collapse to the single most-complete record, and
// the result is ranked by how well each name matches the query. This both
// surfaces authors the primary lacks and de-fragments OpenLibrary's habit of
// returning several partial records for one author. Providers that error or
// time out are skipped; an error is returned only when every provider fails.
func (a *Aggregator) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	providers := a.providers()
	if len(providers) == 0 {
		return nil, nil
	}
	results, anySuccess, firstErr := searchFanOut(ctx, providers, func(c context.Context, p Provider) ([]models.Author, error) {
		return p.SearchAuthors(c, query)
	})
	if !anySuccess && firstErr != nil {
		return nil, firstErr
	}

	var all []models.Author
	for i := range providers {
		all = append(all, results[i]...)
	}
	merged := dedupeAuthorsByName(all)
	rerankAuthorsByRelevance(merged, query)
	return merged, nil
}

// ResolveCanonicalAuthor finds the richest OpenLibrary record for an author name
// — the canonical (inversion-aware) name match with the largest catalogue — and
// returns its full profile (bio/image from GetAuthor; ratings/disambiguation
// from the search record). It queries OpenLibrary directly, NOT the merged/
// deduped author search, so the OL record can't be collapsed away by another
// provider. Returns nil when no confident OL record (BookCount > 0) exists. Used
// to resolve the author of a name-only result (e.g. Google Books) when adding it.
func (a *Aggregator) ResolveCanonicalAuthor(ctx context.Context, name string) (*models.Author, error) {
	var ol Provider
	for _, p := range a.providers() {
		if p != nil && normalizedProviderName(p.Name()) == "openlibrary" {
			ol = p
			break
		}
	}
	if ol == nil {
		return nil, nil
	}
	results, err := ol.SearchAuthors(ctx, name)
	if err != nil {
		return nil, err
	}
	want := canonicalAuthorKey(name)
	var best *models.Author
	for i := range results {
		r := &results[i]
		if canonicalAuthorKey(r.Name) != want || authorBookCount(*r) <= 0 {
			continue
		}
		if best == nil || authorBookCount(*r) > authorBookCount(*best) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}

	full, err := a.GetAuthor(ctx, best.ForeignID)
	if err != nil {
		return nil, err
	}
	// Start from the search record (ratings, work count, disambiguation), then
	// overlay GetAuthor's richer profile (bio, image). Copy into a fresh value so
	// we never mutate GetAuthor's cached object.
	out := *best
	if full != nil {
		out.ForeignID = full.ForeignID
		out.Name = full.Name
		if full.Description != "" {
			out.Description = full.Description
		}
		if full.ImageURL != "" {
			out.ImageURL = full.ImageURL
		}
		if full.SortName != "" {
			out.SortName = full.SortName
		}
		if full.Disambiguation != "" {
			out.Disambiguation = full.Disambiguation
		}
		if full.RatingsCount > 0 {
			out.RatingsCount = full.RatingsCount
		}
		if full.AverageRating > 0 {
			out.AverageRating = full.AverageRating
		}
	}
	return &out, nil
}

// searchFanOut runs fn against the primary and every enricher in parallel (with
// a per-provider timeout), returning each provider's results in provider order
// (primary first), whether any provider returned without error, and the first
// non-"not configured" error seen.
func searchFanOut[T any](ctx context.Context, providers []Provider, fn func(context.Context, Provider) ([]T, error)) (results [][]T, anySuccess bool, firstErr error) {
	results = make([][]T, len(providers))
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		if p == nil {
			continue
		}
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, searchProviderTimeout)
			defer cancel()
			r, err := fn(pctx, p)
			results[i] = r
			errs[i] = err
		}(i, p)
	}
	wg.Wait()
	for i, p := range providers {
		if p == nil {
			continue
		}
		if errs[i] != nil {
			if !errors.Is(errs[i], ErrProviderNotConfigured) {
				slog.Warn("metadata search: provider failed", "provider", p.Name(), "error", errs[i])
				if firstErr == nil {
					firstErr = errs[i]
				}
			}
			results[i] = nil
			continue
		}
		anySuccess = true
	}
	return results, anySuccess, firstErr
}

// canonicalAuthorKey normalizes an author name to a comparison key, treating
// "Last, First" the same as "First Last" so a person's inverted and natural
// forms collapse together.
func canonicalAuthorKey(name string) string {
	return normalizeForDedup(uninvertAuthorName(name))
}

// CanonicalAuthorKey is the exported form of canonicalAuthorKey, so callers
// (e.g. the API layer matching a search result's author name against the
// library) identify authors the same way ResolveCanonicalAuthor does.
func CanonicalAuthorKey(name string) string {
	return canonicalAuthorKey(name)
}

// uninvertAuthorName converts "Last, First" to "First Last"; other forms are
// returned unchanged (whitespace-trimmed).
func uninvertAuthorName(name string) string {
	n := strings.TrimSpace(name)
	if i := strings.Index(n, ","); i > 0 {
		last := strings.TrimSpace(n[:i])
		first := strings.TrimSpace(n[i+1:])
		if last != "" && first != "" {
			return first + " " + last
		}
	}
	return n
}

// authorBookCount returns the author's known work count, or 0 when unknown
// (providers other than OpenLibrary leave Statistics nil).
func authorBookCount(a models.Author) int {
	if a.Statistics == nil {
		return 0
	}
	return a.Statistics.BookCount
}

// dedupeAuthorsByName collapses records that refer to the same person (canonical
// name) to the single most-complete one — most works, then most ratings (each
// compared only when both report it, since only some providers populate them),
// then the earliest (provider-first) occurrence. Records with an empty name pass
// through untouched. Output order follows the kept records' positions.
func dedupeAuthorsByName(authors []models.Author) []models.Author {
	best := make(map[string]int)
	for i := range authors {
		key := canonicalAuthorKey(authors[i].Name)
		if key == "" {
			continue
		}
		if j, ok := best[key]; !ok || betterAuthorRecord(authors[i], authors[j]) {
			best[key] = i
		}
	}
	out := make([]models.Author, 0, len(authors))
	for i := range authors {
		key := canonicalAuthorKey(authors[i].Name)
		if key == "" || best[key] == i {
			out = append(out, authors[i])
		}
	}
	return out
}

// betterAuthorRecord reports whether record a is a more complete representative
// of an author than b. A provider that reports a count (>0) is preferred over
// one that doesn't (0 = unknown), so OpenLibrary's work/ratings-bearing records
// win over enrichers that omit them; ties keep the earlier (provider-first) one.
func betterAuthorRecord(a, b models.Author) bool {
	if c := preferKnownGreater(authorBookCount(a), authorBookCount(b)); c != 0 {
		return c > 0
	}
	if c := preferKnownGreater(a.RatingsCount, b.RatingsCount); c != 0 {
		return c > 0
	}
	return false
}

// preferKnownGreater compares two counts where 0 means "unknown": a known value
// beats unknown, and two known values compare by magnitude. Returns +1 if a is
// preferable, -1 if b is, 0 if indistinguishable.
func preferKnownGreater(a, b int) int {
	if (a > 0) != (b > 0) {
		if a > 0 {
			return 1
		}
		return -1
	}
	if a > 0 && b > 0 && a != b {
		if a > b {
			return 1
		}
		return -1
	}
	return 0
}

// authorRelevance scores how well an author name matches the query, accounting
// for "Last, First" forms by also scoring the un-inverted name.
func authorRelevance(name, query string) float64 {
	r := searchRelevance(name, query)
	if inv := uninvertAuthorName(name); inv != strings.TrimSpace(name) {
		if ri := searchRelevance(inv, query); ri > r {
			r = ri
		}
	}
	return r
}

// rerankAuthorsByRelevance reorders authors so the best name matches rank first,
// tiebreaking by work count then ratings (both compared only when present), then
// original order.
func rerankAuthorsByRelevance(authors []models.Author, query string) {
	if len(authors) < 2 || !queryHasLetters(query) {
		return
	}
	scores := make([]float64, len(authors))
	for i := range authors {
		scores[i] = authorRelevance(authors[i].Name, query)
	}
	order := make([]int, len(authors))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		ix, iy := order[x], order[y]
		if scores[ix] != scores[iy] {
			return scores[ix] > scores[iy]
		}
		if c := preferKnownGreater(authorBookCount(authors[ix]), authorBookCount(authors[iy])); c != 0 {
			return c > 0
		}
		if c := preferKnownGreater(authors[ix].RatingsCount, authors[iy].RatingsCount); c != 0 {
			return c > 0
		}
		return ix < iy
	})
	reordered := make([]models.Author, len(authors))
	for i, j := range order {
		reordered[i] = authors[j]
	}
	copy(authors, reordered)
}

// searchProviderTimeout bounds how long any single provider may take during a
// merged book search, so one slow source can't stall the whole request.
const searchProviderTimeout = 8 * time.Second

// SearchBooks queries the primary provider and every configured enricher in
// parallel, then merges the results: primary hits rank first, followed by
// enricher hits the primary didn't already return. This surfaces books the
// primary (OpenLibrary) lacks — e.g. recent titles present in Google Books or
// Hardcover. Providers that error or time out are skipped rather than failing
// the search; an error is returned only when every provider fails. Duplicates
// are collapsed across providers by ISBN, then by normalized title+author.
func (a *Aggregator) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	providers := a.providers() // primary first, then enrichers
	if len(providers) == 0 {
		return nil, nil
	}
	results, anySuccess, firstErr := searchFanOut(ctx, providers, func(c context.Context, p Provider) ([]models.Book, error) {
		return p.SearchBooks(c, query)
	})
	// Only surface an error when no provider succeeded; otherwise return what we
	// found, even if some providers failed.
	if !anySuccess && firstErr != nil {
		return nil, firstErr
	}

	var merged []models.Book
	seenISBN := map[string]bool{}
	seenTA := map[string]bool{}
	for i := range providers {
		for _, b := range results[i] {
			if searchBookSeen(b, seenISBN, seenTA) {
				continue
			}
			merged = append(merged, b)
		}
	}

	rerankByRelevance(merged, query)
	return merged, nil
}

// rerankByRelevance reorders merged search results so the best title matches
// rank first regardless of which provider supplied them — without this, results
// stay grouped by provider and a strong match from an enricher sits below a
// weaker provider's whole block. Skipped for ISBN/numeric queries (no title to
// match). Ties fall back to popularity only when BOTH sides report it (OL and
// Hardcover populate RatingsCount inconsistently, so 0 means unknown, not
// unpopular), then to the original provider-first order for stability.
func rerankByRelevance(books []models.Book, query string) {
	if len(books) < 2 || !queryHasLetters(query) {
		return
	}
	scores := make([]float64, len(books))
	for i := range books {
		scores[i] = bookSearchRelevance(books[i], query)
	}
	order := make([]int, len(books))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		ix, iy := order[x], order[y]
		if scores[ix] != scores[iy] {
			return scores[ix] > scores[iy]
		}
		rx, ry := books[ix].RatingsCount, books[iy].RatingsCount
		if rx > 0 && ry > 0 && rx != ry {
			return rx > ry
		}
		return ix < iy
	})
	reordered := make([]models.Book, len(books))
	for i, j := range order {
		reordered[i] = books[j]
	}
	copy(books, reordered)
}

// searchStopwords are low-information English words ignored when comparing the
// query's tokens to a title (the exact/prefix/substring tiers still see them).
var searchStopwords = map[string]bool{
	"the": true, "of": true, "a": true, "an": true, "and": true, "to": true, "in": true, "for": true,
}

// bookSearchRelevance scores a book against the query, accounting for the author.
// Query tokens satisfied by the book's author are removed from what the TITLE
// must match — so for a "title author" query the real book (whose author usually
// isn't in its title) outranks summary/companion books that stuff the author name
// into their title. Taking the max with the plain title score means author
// awareness can only help, never hurt.
func bookSearchRelevance(b models.Book, query string) float64 {
	q := normalizeForDedup(query)
	if q == "" {
		return 0
	}
	if b.Author == nil || b.Author.Name == "" {
		return searchRelevance(b.Title, q)
	}
	authorTokens := make(map[string]bool)
	for _, tok := range strings.Fields(normalizeForDedup(b.Author.Name)) {
		authorTokens[tok] = true
	}
	titleToks := make([]string, 0, len(strings.Fields(q)))
	authorMatched := false
	for _, tok := range strings.Fields(q) {
		if authorTokens[tok] {
			authorMatched = true
			continue
		}
		titleToks = append(titleToks, tok)
	}
	if !authorMatched || len(titleToks) == 0 {
		return searchRelevance(b.Title, q)
	}
	reduced := strings.Join(titleToks, " ")
	score := max(searchRelevance(b.Title, reduced), searchRelevance(b.Title, q))
	// Small positive signal that the author matched the query, so a book BY the
	// queried author edges out a comparable-title match by a different author
	// (e.g. a real edition vs. a "summary of" companion). Too small to override a
	// clearly stronger title match.
	return score + authorQueryMatchBonus
}

const authorQueryMatchBonus = 0.05

// searchRelevance scores how well a book title matches the query (0..1, higher
// is better). Both are normalized (lowercase, alnum, single spaces). The score
// is continuous within each tier — a tighter (shorter) title outranks a looser
// one at the same tier — so title length, not provider order, separates matches.
func searchRelevance(title, query string) float64 {
	t := normalizeForDedup(title)
	q := normalizeForDedup(query)
	if t == "" || q == "" {
		return 0
	}
	if t == q {
		return 1.0
	}
	// Word-boundary prefix/substring via space padding.
	pt := " " + t + " "
	pq := " " + q + " "
	switch {
	case strings.HasPrefix(pt, pq):
		return 0.90 + 0.05*lenRatio(q, t)
	case strings.Contains(pt, pq):
		return 0.70 + 0.10*lenRatio(q, t)
	}
	qToks := nonStopwordTokens(q)
	if len(qToks) == 0 {
		qToks = strings.Fields(q)
	}
	if len(qToks) == 0 {
		return 0
	}
	tset := make(map[string]bool)
	for _, tok := range strings.Fields(t) {
		tset[tok] = true
	}
	matched := 0
	for _, tok := range qToks {
		if tset[tok] {
			matched++
		}
	}
	cov := float64(matched) / float64(len(qToks))
	if matched == len(qToks) {
		return 0.45 + 0.15*cov*lenRatio(q, t)
	}
	return 0.30 * cov
}

// lenRatio returns len(short)/len(long) clamped to [0,1]; longer (looser) titles
// get a smaller ratio, so a tighter match scores higher within its tier.
func lenRatio(short, long string) float64 {
	if len(long) == 0 {
		return 0
	}
	r := float64(len(short)) / float64(len(long))
	if r > 1 {
		return 1
	}
	return r
}

// nonStopwordTokens splits s into space-separated tokens minus stopwords.
func nonStopwordTokens(s string) []string {
	var out []string
	for _, tok := range strings.Fields(s) {
		if !searchStopwords[tok] {
			out = append(out, tok)
		}
	}
	return out
}

// queryHasLetters reports whether the query contains any letter; a purely
// numeric query (ISBN) has no title to rank against.
func queryHasLetters(q string) bool {
	for _, r := range q {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// searchBookSeen reports whether book b duplicates one already emitted — by any
// of its (normalized) ISBNs, or by its normalized title+author — and registers
// b's keys so later providers' copies are dropped. Because providers are walked
// primary-first, the primary's copy of a duplicated book is the one kept.
func searchBookSeen(b models.Book, seenISBN, seenTA map[string]bool) bool {
	var isbns []string
	dup := false
	for _, raw := range b.ISBNs {
		n := isbnutil.Normalize(raw)
		if n == "" {
			continue
		}
		isbns = append(isbns, n)
		if seenISBN[n] {
			dup = true
		}
	}
	ta := titleAuthorKey(b)
	if ta != "" && seenTA[ta] {
		dup = true
	}
	if dup {
		return true
	}
	for _, n := range isbns {
		seenISBN[n] = true
	}
	if ta != "" {
		seenTA[ta] = true
	}
	return false
}

// titleAuthorKey builds a normalized "title|author" dedup key, or "" when the
// title is empty (in which case the caller falls back to ISBN-only dedup).
func titleAuthorKey(b models.Book) string {
	title := normalizeForDedup(b.Title)
	if title == "" {
		return ""
	}
	author := ""
	if b.Author != nil {
		author = normalizeForDedup(b.Author.Name)
	}
	// Include the format so the same work in different formats (an ebook edition
	// from one provider, an audiobook from another) is NOT collapsed — the user
	// must be able to see and pick the format.
	return title + "|" + author + "|" + searchFormatKey(b.MediaType)
}

// searchFormatKey folds a media type into a dedup bucket. An unspecified type is
// treated as ebook (the common case for print/ebook-only providers like Google
// Books), so two ebook editions still collapse while an audiobook stays distinct.
func searchFormatKey(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case models.MediaTypeAudiobook:
		return "audiobook"
	case models.MediaTypeBoth:
		return "both"
	default:
		return "ebook"
	}
}

// normalizeForDedup lowercases s and keeps only letters and digits separated by
// single spaces, so trivial punctuation, spacing, and case differences between
// providers collapse to the same key.
func normalizeForDedup(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

func (a *Aggregator) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	key := "author:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Author), nil
	}

	provider := a.providerForForeignID(foreignID)
	if provider == nil {
		return nil, nil
	}
	author, err := provider.GetAuthor(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, author)
	return author, nil
}

func (a *Aggregator) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	key := "book:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	provider := a.providerForForeignID(foreignID)
	if provider == nil {
		return nil, nil
	}
	book, err := provider.GetBook(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	if book == nil {
		a.cache.set(key, book)
		return nil, nil
	}

	// Enrich from secondary providers if description is sparse or cover is missing.
	if len(book.Description) < 50 || book.ImageURL == "" {
		a.enrichBook(ctx, book)
	}

	a.cache.set(key, book)
	return book, nil
}

func (a *Aggregator) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	key := "editions:" + bookForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Edition), nil
	}

	provider := a.providerForForeignID(bookForeignID)
	if provider == nil {
		return nil, nil
	}
	editions, err := provider.GetEditions(ctx, bookForeignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, editions)
	return editions, nil
}

// GetEditionsFromProvider fetches editions from a named provider, bypassing
// prefix-based routing. This is used when callers know the provider from UI
// state but the stored foreign ID is an unprefixed provider-native value.
func (a *Aggregator) GetEditionsFromProvider(ctx context.Context, providerName, bookForeignID string) ([]models.Edition, error) {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	bookForeignID = strings.TrimSpace(bookForeignID)
	if providerName == "" || bookForeignID == "" {
		return nil, nil
	}
	key := "editions-provider:" + providerName + ":" + bookForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Edition), nil
	}

	for _, provider := range a.providers() {
		if provider == nil || normalizedProviderName(provider.Name()) != normalizedProviderName(providerName) {
			continue
		}
		editions, err := provider.GetEditions(ctx, bookForeignID)
		if err != nil {
			return nil, err
		}
		a.cache.set(key, editions)
		return editions, nil
	}
	return nil, ErrProviderNotConfigured
}

func (a *Aggregator) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	isbn = isbnutil.Normalize(isbn)
	key := "isbn:" + isbn
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	var errs []error
	skippedUnconfigured := false
	providers := a.providers()
	var primaryFallback *models.Book
	var firstFallback *models.Book
	for idx, provider := range providers {
		if provider == nil {
			continue
		}
		book, err := provider.GetBookByISBN(ctx, isbn)
		if err != nil {
			if errors.Is(err, ErrProviderNotConfigured) {
				skippedUnconfigured = true
				slog.Debug("isbn provider not configured", "provider", provider.Name())
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", provider.Name(), err))
			slog.Debug("isbn lookup provider failed", "provider", provider.Name(), "error", err)
			continue
		}
		if book == nil {
			continue
		}
		if canonical, status := a.lookupCanonicalPrimaryBook(ctx, isbn, *book); status != canonicalPrimaryBookNoMatch {
			if status == canonicalPrimaryBookMatched {
				book = canonical
				return a.cacheISBNBook(ctx, key, book), nil
			}
			if idx > 0 || len(providers) == 1 {
				return a.cacheISBNBook(ctx, key, book), nil
			}
		}
		if firstFallback == nil {
			firstFallback = book
		}
		if idx == 0 && len(providers) > 1 {
			primaryFallback = book
			continue
		}
		if primaryFallback != nil {
			continue
		}
		return a.cacheISBNBook(ctx, key, book), nil
	}

	if primaryFallback != nil {
		return a.cacheISBNBook(ctx, key, primaryFallback), nil
	}
	if firstFallback != nil {
		return a.cacheISBNBook(ctx, key, firstFallback), nil
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	var noBook *models.Book
	if !skippedUnconfigured {
		a.cache.set(key, noBook)
	}
	return nil, nil
}

func (a *Aggregator) cacheISBNBook(ctx context.Context, key string, book *models.Book) *models.Book {
	if book != nil && len(book.Description) < 50 {
		// Try fetching the full record from the provider before falling back to
		// enrichers. ISBN search results are often lightweight (title + ForeignID
		// only); GetBook returns the canonical description, cover, etc.
		if book.ForeignID != "" {
			if provider := a.providerForForeignID(book.ForeignID); provider != nil {
				if full, err := provider.GetBook(ctx, book.ForeignID); err == nil && full != nil {
					if len(full.Description) > len(book.Description) {
						book.Description = full.Description
					}
					if book.ImageURL == "" && full.ImageURL != "" {
						book.ImageURL = full.ImageURL
					}
					if book.AverageRating == 0 && full.AverageRating > 0 {
						book.AverageRating = full.AverageRating
						book.RatingsCount = full.RatingsCount
					}
					if book.MetadataProvider == "" && full.MetadataProvider != "" {
						book.MetadataProvider = full.MetadataProvider
					}
					if book.Language == "" && full.Language != "" {
						book.Language = full.Language
					}
				}
			}
		}
		if len(book.Description) < 50 {
			a.enrichBook(ctx, book)
		}
	}
	a.cache.set(key, book)
	return book
}

// GetBookFromProvider fetches a single book by foreign ID from the named
// provider ("openlibrary" or "hardcover"). It bypasses the TTL cache so the
// rebind flow always gets a fresh record. Returns ErrProviderNotConfigured
// when no matching provider is found.
func (a *Aggregator) GetBookFromProvider(ctx context.Context, providerName, foreignID string) (*models.Book, error) {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if a.primary.Name() == providerName {
		return a.primary.GetBook(ctx, foreignID)
	}
	for _, enricher := range a.enrichers {
		if enricher.Name() == providerName {
			return enricher.GetBook(ctx, foreignID)
		}
	}
	return nil, ErrProviderNotConfigured
}

// ResolveBookByISBN walks every provider (primary first, then enrichers) and
// returns the first hit whose author carries a usable foreignAuthorId. Used
// at add-time when the user picked a search result from a provider that
// doesn't expose author IDs (notably DNB), so we can fall back to a stronger
// provider for the author identity rather than synthesising an ID locally.
//
// Returns (nil, nil) when no provider has the ISBN — the caller should treat
// that as "couldn't resolve" and surface a friendly error to the user.
// A provider error on one source is logged at debug level and treated as a
// miss, so a single flaky provider doesn't block resolution.
func (a *Aggregator) ResolveBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	providers := append([]Provider{a.primary}, a.enrichers...)
	for _, p := range providers {
		book, err := p.GetBookByISBN(ctx, isbn)
		if err != nil {
			slog.Debug("resolve isbn: provider failed", "provider", p.Name(), "isbn", isbn, "error", err)
			continue
		}
		if book == nil || book.Author == nil || book.Author.ForeignID == "" {
			continue
		}
		return book, nil
	}
	return nil, nil
}
