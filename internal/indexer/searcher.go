// Package indexer coordinates book searches across multiple Newznab/Torznab
// indexers, filters and ranks the returned releases, and exposes a release-
// name parser shared by the filter pipeline and the import path.
package indexer

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// searchBookTimeout is the outer deadline applied to a full SearchBook call.
// Each per-indexer BookSearch may issue up to 4 sequential HTTP calls; with a
// 30 s transport timeout per call the theoretical maximum is 4 × 30 s = 120 s
// per indexer. 60 s is a pragmatic bound that still allows a slow indexer to
// respond on tier 1 while preventing a hung connection from blocking the caller
// for multiple minutes.
const searchBookTimeout = 60 * time.Second

// Searcher coordinates searches across multiple Newznab indexers.
type Searcher struct {
	// newClient is the factory used to create per-indexer newznab clients
	// on a cache miss. nil uses newznab.New, which builds a client with the
	// SSRF-hardened transport. Tests that run against httptest servers can
	// inject a factory that bypasses the dialer.
	newClient func(baseURL, apiKey string) *newznab.Client

	// cache pools *newznab.Client instances across calls so connection
	// reuse via the shared transport actually pays off (finding 9, Wave 3
	// deep audit). Lazily initialised on first SearchBook / SearchQuery so
	// tests that mutate Searcher.newClient before any call still get a
	// cache that respects their factory.
	cacheOnce sync.Once
	cache     *clientCache

	// cooldowns holds the indexers that have answered a search with a
	// rate-limit rejection, so we stop asking until the deadline they gave
	// us passes (#1934). The zero value is ready to use.
	cooldowns indexerCooldowns

	// health persists whether each indexer answered its last search, so a
	// suspended account or a revoked key is visible in Settings instead of
	// only in an interactive search panel (#1935). nil disables recording,
	// which is what every caller that has not been wired up gets.
	health *indexerHealth

	// quota enforces the per-indexer daily query cap, so one wanted sweep
	// cannot spend a tracker's whole API allowance (#2312). nil disables the
	// cap entirely, same as health.
	quota *indexerQuota

	// timeout overrides searchBookTimeout; zero uses it. Only tests set it,
	// so the outer deadline can be exercised without waiting a minute.
	timeout time.Duration
}

// NewSearcher creates a new multi-indexer searcher.
func NewSearcher() *Searcher {
	return &Searcher{}
}

// MatchCriteria describes what we're searching for. Year and ISBN are
// optional and only used for ranking — they never cause a result to be
// rejected. MediaType filters the indexer category set; "audiobook" narrows
// to the Newznab audiobook subcategory (303x, primarily 3030), anything else
// narrows to the ebook subcategory (702x, primarily 7020). The broad parent
// categories 7000 and 3000 are only sent for indexers that explicitly opt in
// (Indexer.IncludeParentCategories); they can find releases missed by an
// incomplete category mapping but are often noisy.
// AllowedLanguages is the author's metadata-profile language list; callers
// apply it to results via FilterByAllowedLanguages (releases tagged with a
// language outside the set are dropped). Recorded here so search debug
// output shows which set was in force.
type MatchCriteria struct {
	Title            string
	Author           string
	Year             int
	ISBN             string
	ASIN             string   // for audiobook ASIN anchoring
	MediaType        string   // models.MediaTypeEbook or models.MediaTypeAudiobook
	AllowedLanguages []string // from author's MetadataProfile; empty = no filter
	AuthorAliases    []string // alternate names (e.g. latin-script romanisations for non-latin authors)
	// Profile is the author's quality profile. Its stored order ranks the
	// results, top is best, per media type (see quality_order.go). nil, or a
	// profile with no entry for the media type in question, ranks by
	// models.QualityRank as before.
	Profile *models.QualityProfile
	// DurationSeconds is the book's stored runtime, applied to every candidate
	// for that book. Indexer feeds carry no per-release runtime, so this is the
	// only figure ranking has. Zero means the runtime is unknown, and the size
	// term keeps the flat, capped bonus it has always used rather than
	// dividing by zero.
	DurationSeconds int
	// SeriesPosition is the wanted book's position in its primary series as
	// stored ("3", "1-3", ""). The relevance filter uses it to tell a release
	// that names the wanted book's own number from one that names a later book
	// (#3268). Empty when the book has no series position; the filter then
	// judges a numbered release on the release name alone.
	SeriesPosition string
	// SeriesTitle is the title of the wanted book's primary series. The
	// relevance filter never reads a library title that equals, contains or is
	// contained in it as "another title", because releases name their series
	// (#3268). Empty when the book has no series.
	SeriesTitle string
	// AuthorTitles are the titles of the author's OTHER books in the library
	// (not the wanted book). The relevance filter rejects a release that goes
	// on to name one of them after the wanted title (#3268). Empty disables
	// that rule only.
	AuthorTitles []string
}

// CriteriaISBN picks the ISBN to put in MatchCriteria.ISBN for a book, given
// that book's edition rows. Callers that skip it leave MatchCriteria.ISBN
// empty and the exact-match bonus in scoreResult can never fire (#1724).
//
// The returned value is digits-only ISBN-13 — the same shape ParseRelease
// produces — because a release name can only ever yield an ISBN-13 (the
// parser's regex requires a 978/979 prefix). An edition recorded with only an
// isbn_10 is converted rather than dropped, so those books still match.
//
// A book can have many editions and MatchCriteria carries one ISBN, so the
// book's selected edition wins when it has one; otherwise the first edition
// with a usable ISBN does, in the order the caller supplied.
//
// Book.ProviderISBNs is deliberately not consulted: it is transport-only
// metadata filled in by providers during ingestion and is never read back out
// of the books table, so it is always empty on a book loaded from the database
// (#1893).
func CriteriaISBN(book *models.Book, editions []models.Edition) string {
	if book == nil {
		return ""
	}
	first := ""
	for _, e := range editions {
		isbn := editionISBN13(e)
		if isbn == "" {
			continue
		}
		if book.SelectedEditionID != nil && e.ID == *book.SelectedEditionID {
			return isbn
		}
		if first == "" {
			first = isbn
		}
	}
	return first
}

// editionISBN13 returns the edition's ISBN in ISBN-13 form, preferring the
// isbn_13 column and falling back to a converted isbn_10.
func editionISBN13(e models.Edition) string {
	if e.ISBN13 != nil {
		if v := isbnutil.ToISBN13(*e.ISBN13); v != "" {
			return v
		}
	}
	if e.ISBN10 != nil {
		if v := isbnutil.ToISBN13(*e.ISBN10); v != "" {
			return v
		}
	}
	return ""
}

// makeClient returns a (possibly cached) newznab client for the given
// indexer config. The pool is keyed on (baseURL, apiKey) so successive
// searches against the same indexer reuse the same *Client (and therefore
// the same connection-keep-alive state on the shared transport). On a
// cache miss the injected newClient factory is used, falling back to
// newznab.New (SSRF-hardened transport) when none is set.
func (s *Searcher) makeClient(baseURL, apiKey string) *newznab.Client {
	s.cacheOnce.Do(func() {
		s.cache = newClientCache(s.newClient)
	})
	return s.cache.get(baseURL, apiKey)
}

// filterCategoriesForMedia returns the subset of configured indexer
// categories relevant to the requested media type. If the indexer has no
// categories matching the needed prefix (e.g. pre-v0.5.0 indexer configs
// that only list 7000/7020 but the user is searching for an audiobook),
// we substitute the standard Newznab category for that media type rather
// than silently sending an ebook query — otherwise the search appears to
// succeed but returns the wrong kind of release.
//
// Indexers with non-standard taxonomies (category IDs > 9999, e.g. MaM's
// 100xxx subcategories) are passed through as-is when no standard-range
// match exists. So are the Newznab "Other" IDs (8xxx): an indexer whose list
// has nothing in the requested media's thousand but does name 8000 is searched
// under 8000 rather than under a standard ID it never listed. Substituting a standard fallback ID (3030, 7020) on such
// indexers returns unrelated results because the standard IDs do not cover
// the indexer's extended subcategory tree.
//
// When includeParentCategories is true (the per-indexer opt-in for trackers
// that file releases loosely under the parent), the media-specific parent is
// prepended even if it is absent from cats — existing rows may not have it
// stored because Prowlarr sync strips it. Two guards keep the opt-in from
// widening a search into a bucket the indexer does not serve:
//
//   - The parent is only added when the indexer carries at least one category
//     in that thousand. A books-only indexer opted in must not receive 3000 on
//     an audiobook search, or it returns its entire music surface.
//   - The parent is never added on the non-standard-taxonomy path. Injecting
//     7000 into a MaM-style indexer is the same mistake as substituting 7020
//     there: the standard ID does not address the extended subcategory tree.
//
// Note the interaction with the query cascade: broadening the category list
// makes an early text tier likelier to come back with junk. That used to stop
// the ladder — tiers 2-4 returned on ANY result, before filterRelevant ran —
// leaving an opted-in indexer with fewer good results than a narrow list would
// have produced. Every tier now gates on relevance before short-circuiting, so
// a junk tier falls through to the more specific ones (#1891).
func filterCategoriesForMedia(cats []int, mediaType string, includeParentCategories bool) []int {
	// Newznab category convention: 7xxx is the Books parent (7020 ebook,
	// 7030 magazines), 3xxx is Audio (3030 audiobook). The bare parents
	// (7000 / 3000) are dropped by default: Prowlarr reports them for generic
	// book trackers and sending them as-is returns the entire books or audio
	// surface, which is noise. The per-indexer opt-in adds the relevant parent
	// back after filtering, subject to the guards above.
	//
	// Beyond that, every non-parent subcategory in the matching bucket is
	// trusted: the user explicitly added it to the indexer's category list
	// in Settings → Indexers. Previously the filter narrowly matched
	// 702x / 303x and silently dropped foreign-language IDs like 7120
	// (German ebooks), 7150, 7180, and any 31xx audiobook variants (#851),
	// leaving non-English users searching only the English bucket. Now any
	// 7xxx (except 7000) flows through for ebook search, and any 3xxx
	// (except 3000) flows through for audiobook search. Standard 7020 /
	// 3030 remain the fallback for empty input or zero matches.
	wantThousand := 7
	parent := 7000
	fallback := []int{7020}
	if mediaType == "audiobook" {
		wantThousand = 3
		parent = 3000
		fallback = []int{3030}
	}
	if len(cats) == 0 {
		return fallback
	}
	var out []int
	var other []int
	hasNonStandard := false
	// carriesMedia records whether the indexer lists anything at all in the
	// requested media's thousand — the parent itself counts. It gates the
	// opt-in so the parent is only ever sent to an indexer that serves it.
	carriesMedia := false
	for _, c := range cats {
		if c/1000 == wantThousand {
			carriesMedia = true
			if c != parent {
				out = append(out, c)
			}
		}
		if c > 9999 {
			hasNonStandard = true
		}
		// Newznab files "Other" under 8xxx, and trackers that do not map their
		// books file them there. A user who typed 8000 into an indexer's
		// categories chose it, so it is not ours to replace with 7020 (#3169).
		if c/1000 == 8 {
			other = append(other, c)
		}
	}
	if len(out) == 0 {
		if hasNonStandard {
			// Non-standard taxonomy: pass through untouched, opt-in included.
			return cats
		}
		if len(other) > 0 {
			// Nothing in the requested media's thousand, but the indexer
			// lists the Other bucket: search that, not a standard ID the
			// indexer was never given. The opt-in parent does not apply, since
			// the indexer does not carry the media's thousand.
			return other
		}
		out = append([]int(nil), fallback...)
	}
	if includeParentCategories && carriesMedia {
		out = append([]int{parent}, out...)
	}
	return out
}

// SearchBook queries all enabled indexers and returns deduplicated, filtered,
// ranked results.
//
// An outer context.WithTimeout of searchBookTimeout is applied to the whole
// operation so that a slow or hung indexer cannot block the caller indefinitely.
// The timeout is additional to any deadline already on ctx — whichever fires
// first wins.
func (s *Searcher) SearchBook(ctx context.Context, indexers []models.Indexer, c MatchCriteria) []newznab.SearchResult {
	timeout := searchBookTimeout
	if s.timeout > 0 {
		timeout = s.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		mu      sync.Mutex
		results []newznab.SearchResult
		wg      sync.WaitGroup
	)

	for _, idx := range indexers {
		if !idx.Enabled {
			continue
		}
		if reason, held := s.cooldownActive(idx); held {
			slog.Debug("skipping indexer in rate-limit cooldown", "indexer", idx.Name, "reason", reason)
			continue
		}
		if reason, held := s.quotaHold(ctx, idx); held {
			slog.Debug("skipping indexer at its daily query cap", "indexer", idx.Name, "reason", reason)
			continue
		}
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()
			defer s.flushQuota(ctx, idx, false)

			client := s.makeClient(idx.URL, idx.APIKey)
			cats := filterCategoriesForMedia(idx.Categories, c.MediaType, idx.IncludeParentCategories)
			qctx := newznab.WithQueryBudget(ctx, s.quotaBudget(idx))
			hits, trace, err := client.BookSearchTraced(qctx, c.Title, c.Author, cats)
			if trace.BudgetExhausted {
				s.flushQuota(ctx, idx, true)
			}
			if err != nil {
				if budgetExhausted(err) {
					s.flushQuota(ctx, idx, true)
					slog.Debug("indexer hit its daily query cap mid-search", "indexer", idx.Name)
					return
				}
				s.noteIndexerError(idx, err)
				s.health.recordFailure(ctx, idx, err)
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				return
			}
			s.noteIndexerSuccess(ctx, idx)

			protocol := protocolForType(idx.Type)
			for i := range hits {
				hits[i].IndexerID = idx.ID
				hits[i].IndexerName = idx.Name
				hits[i].Protocol = protocol
				hits[i].IndexerPriority = idx.Priority
			}

			mu.Lock()
			results = append(results, hits...)
			mu.Unlock()

			slog.Debug("indexer returned results", "indexer", idx.Name, "count", len(hits))
		}(idx)
	}

	wg.Wait()

	results = dedupe(results)
	results = filterUsenetJunk(results, junkScope{MediaType: c.MediaType, WantedTitle: c.Title})
	results = filterNonBookContent(results)
	results, _ = filterRelevantWithContext(results, c.Title, c.Author, c.AuthorAliases, c.context())
	rankResults(results, c)
	return results
}

// SearchQuery performs a generic text search across all enabled indexers.
//
// The per-indexer parent-category opt-in does not apply here: it selects a
// parent from the requested media type, and a freeform query has none.
// Configured categories are sent as-is.
//
// The content guards SearchBook applies are applied here too: some indexers
// ignore category filters on q= searches and return movies and raw per-article
// usenet postings alongside books, and this endpoint backs the /search page,
// whose results each carry a Grab button. Without them the #1591 video guard
// was reachable by hand from the UI even after it was added to both book-search
// pipelines.
//
// filterRelevant is deliberately NOT applied. It scores a result against a
// known book's title and author, and a freeform query has neither — the user
// typed the search themselves, so relevance is their call, not ours. Only the
// query-independent content filters belong here.
func (s *Searcher) SearchQuery(ctx context.Context, indexers []models.Indexer, query string) []newznab.SearchResult {
	var (
		mu      sync.Mutex
		results []newznab.SearchResult
		wg      sync.WaitGroup
	)

	for _, idx := range indexers {
		if !idx.Enabled {
			continue
		}
		if reason, held := s.cooldownActive(idx); held {
			slog.Debug("skipping indexer in rate-limit cooldown", "indexer", idx.Name, "reason", reason)
			continue
		}
		if reason, held := s.quotaHold(ctx, idx); held {
			slog.Debug("skipping indexer at its daily query cap", "indexer", idx.Name, "reason", reason)
			continue
		}
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()
			defer s.flushQuota(ctx, idx, false)

			client := s.makeClient(idx.URL, idx.APIKey)
			qctx := newznab.WithQueryBudget(ctx, s.quotaBudget(idx))
			hits, err := client.Search(qctx, query, idx.Categories)
			if err != nil {
				if budgetExhausted(err) {
					s.flushQuota(ctx, idx, true)
					slog.Debug("indexer hit its daily query cap mid-search", "indexer", idx.Name)
					return
				}
				s.noteIndexerError(idx, err)
				s.health.recordFailure(ctx, idx, err)
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				return
			}
			s.noteIndexerSuccess(ctx, idx)

			protocol := protocolForType(idx.Type)
			for i := range hits {
				hits[i].IndexerID = idx.ID
				hits[i].IndexerName = idx.Name
				hits[i].Protocol = protocol
				hits[i].IndexerPriority = idx.Priority
			}

			mu.Lock()
			results = append(results, hits...)
			mu.Unlock()
		}(idx)
	}

	wg.Wait()

	results = dedupe(results)
	results = filterUsenetJunk(results, junkScope{})
	results = filterNonBookContent(results)
	rankResults(results, MatchCriteria{Title: query})
	return results
}

// primaryTitle returns the portion of title before the first colon (used for
// subtitle handling — "Dune: Messiah" → "Dune"). If there's no colon the full
// title is returned.
func primaryTitle(title string) string {
	if i := strings.Index(title, ":"); i > 0 {
		return strings.TrimSpace(title[:i])
	}
	return title
}

// volumeNumberAgrees keeps the cross spelling volume match (keywordPattern)
// from accepting a different volume.
//
// The volume number is under three bytes for volumes 1 to 99, so SigWords
// drops it and the keyword matchers never compare it. Before volume markers
// matched across spellings, a "Vol 16" release for a "Volume 17" title was
// rejected purely because "volume" was missing from it. That was an accident,
// but it was a rejection, and the cross spelling match must not turn it into
// an accept.
//
// So: when title names a volume marker followed by a number, and the release
// does not carry that exact marker spelling (so any keyword hit on the marker
// came through the cross spelling match), the release must carry some
// spelling of the marker followed by the same number. A release that carries
// the title's own spelling is left exactly as it was judged before. title is
// the raw title string the keywords came from; normRelease is NormalizeRelease
// output.
func volumeNumberAgrees(normRelease, title string) bool {
	tt := strings.Fields(NormalizeRelease(title))
	spelling, number := "", ""
	for i, tok := range tt {
		if newznab.IsVolumeMarker(tok) {
			spelling = tok
			if i+1 < len(tt) {
				number = tt[i+1]
			}
			break
		}
	}
	if spelling == "" || number == "" {
		return true
	}
	rt := strings.Fields(normRelease)
	for _, tok := range rt {
		if tok == spelling {
			return true
		}
	}
	for i, tok := range rt {
		if newznab.IsVolumeMarker(tok) && i+1 < len(rt) && sameVolumeNumber(rt[i+1], number) {
			return true
		}
	}
	return false
}

// sameVolumeNumber compares two volume tokens, ignoring zero padding when both
// are numbers ("07" is "7"). Anything else must match exactly.
func sameVolumeNumber(a, b string) bool {
	if isAllDigits(a) && isAllDigits(b) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	}
	return a == b
}

// stripPossessivePrefix removes a leading "Author's " possessive from a book
// title when the author's name (or a portion of it) forms the possessive
// opener. For example, "Tom Clancy's Rainbow Six" with author "Tom Clancy"
// returns "Rainbow Six". This prevents "clancys" from appearing as a keyword
// and failing to match releases named "Tom Clancy - Rainbow Six".
//
// The comparison is case-insensitive. Both ASCII apostrophes (') and Unicode
// right-single-quotation-marks (’) are recognised as the possessive
// marker. The function tries the full author name first, then each leading
// prefix (first name, first+second name, etc.) in descending length order,
// accepting the longest match. If no possessive prefix is found the original
// title is returned unchanged.
func stripPossessivePrefix(title, author string) string {
	if title == "" || author == "" {
		return title
	}
	// Normalise apostrophe variants so we only need to test one form.
	normTitle := strings.ReplaceAll(title, "’", "'")
	lowerTitle := strings.ToLower(normTitle)

	authorFields := strings.Fields(author)
	// Try longest prefix down to a single word (must be ≥ 2 chars to avoid
	// matching short words that happen to be possessive).
	for n := len(authorFields); n >= 1; n-- {
		prefix := strings.ToLower(strings.Join(authorFields[:n], " ")) + "'s "
		if strings.HasPrefix(lowerTitle, prefix) {
			// Slice normTitle (not title): both use ASCII apostrophe, so
			// len(prefix) is a valid byte offset into normTitle. Slicing the
			// original title mis-aligns when it contains a Unicode
			// right-single-quotation-mark (3 bytes vs ASCII 1 byte).
			stripped := strings.TrimSpace(normTitle[len(prefix):])
			if stripped != "" {
				return stripped
			}
		}
	}
	return title
}

// authorTokens splits an author name into a (significant, all-lowercased)
// token list suitable for word-boundary matching. The name is passed through
// NormalizeRelease first so the tokens live in the same alphabet as the
// normalized release strings they are matched against: interior punctuation
// becomes a token boundary exactly as it does on the release side. Previously
// only edge punctuation was trimmed, so "J.R.R. Tolkien" produced the token
// "j.r.r" — which can never occur in a NormalizeRelease haystack (dots
// collapse to spaces there), making every all-tokens author match fail and
// zeroing out single-keyword-title searches for such authors (#1608).
// Hyphenated first names ("Mary-Kate") had the same defect.
//
// Significant means >=3 chars of letters/digits; shorter tokens (typically
// initials like "R." or "R") are treated as optional and dropped, so the
// compact dotted form decomposes exactly like the spaced "J. R. R. Tolkien".
// Umlauts are KEPT (textutil.FoldForKeyword, not NormalizeRelease): "Müller"
// yields "müller", which keywordPattern matches as mueller, muller or müller,
// while the ASCII "Mueller" yields "mueller" and means that spelling only. A
// token is a keyword, not a release string; see conflictingTitleAuthor for the
// one place that compares one to a release word. Returns nil for
// empty / all-initials input — the caller should fall back to surname-only
// behaviour.
func authorTokens(author string) []string {
	if author == "" {
		return nil
	}
	var out []string
	for _, w := range strings.Fields(textutil.FoldForKeyword(author)) {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// authorMatchesRelease reports whether the normalized release plausibly
// belongs to the requested author. The check is:
//   - Empty author tokens: caller-defined; this function returns false.
//   - 1 significant token (single-name pseudonym, e.g. "Plato"): word-boundary
//     match on that token.
//   - 2+ significant tokens: accept a contiguous "first ... last" phrase
//     match (preferred), or — as a fallback — every significant token at a
//     word boundary anywhere in the release.
//
// Initials (tokens <3 chars like "R." in "George R. R. Martin") have already
// been stripped by authorTokens, so they are effectively optional: a release
// named "George Martin ..." matches "George R. R. Martin".
func authorMatchesRelease(normResult string, tokens []string) bool {
	switch len(tokens) {
	case 0:
		return false
	case 1:
		return WordBoundaryRegex(tokens[0]).MatchString(normResult)
	default:
		// Prefer contiguous "first ... last" phrase.
		if ContainsPhrase(normResult, tokens) {
			return true
		}
		// Fallback: every significant token must appear at a word boundary.
		for _, tok := range tokens {
			if !WordBoundaryRegex(tok).MatchString(normResult) {
				return false
			}
		}
		return true
	}
}

// seriesMarkerTokens are words that legitimately sit before the title in a
// release name as part of a series/sequence label ("Book 1 - Title",
// "Vol. 2 Title", German "Band 3 Titel"). They carry no evidence that the
// phrase belongs to a different work, so matchAnchored treats them as benign
// prefix filler alongside numbers and stop words.
var seriesMarkerTokens = map[string]bool{
	"book": true, "vol": true, "volume": true, "part": true, "tome": true, "band": true,
}

// isAllDigits reports whether s is non-empty and consists only of ASCII digits.
func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// benignPrefixToken reports whether tok, appearing before the matched title in
// a release name, is compatible with the release actually BEING that title:
// the requested author's tokens, bare numbers (series indices, years), tokens
// SigWords itself would discard (stop words, initials, "01"), and generic
// series markers ("book", "vol", …). Anything else is a real foreign word —
// most likely part of a different work's longer title.
func benignPrefixToken(tok string, authorToks []string) bool {
	if isAllDigits(tok) {
		return true
	}
	if len(newznab.SigWords(tok)) == 0 {
		return true
	}
	if seriesMarkerTokens[tok] {
		return true
	}
	for _, a := range authorToks {
		if WordBoundaryRegex(a).MatchString(tok) {
			return true
		}
	}
	return false
}

// matchAnchored reports whether re's leftmost match in normResult is preceded
// only by benign tokens (see benignPrefixToken). Legitimate release naming
// puts the author, a series label, or nothing before the title; a title
// embedded mid-way through a longer different title ("Reborn as an Assassin's
// Apprentice" for "Assassin's Apprentice") has that other work's words in
// front. Checking the leftmost match is sufficient: prefixes only grow, so a
// foreign word before the first match precedes every later match too.
func matchAnchored(normResult string, re *regexp.Regexp, authorToks []string) bool {
	loc := re.FindStringIndex(normResult)
	if loc == nil {
		return false
	}
	for _, tok := range strings.Fields(normResult[:loc[0]]) {
		if !benignPrefixToken(tok, authorToks) {
			return false
		}
	}
	return true
}

// titleExtensionConnectives are the words that, sitting immediately AFTER a
// single-word title inside a release name, carry it on into a longer, different
// title: "Treasure" -> "The Treasure of Khan" (#1731).
//
// Articles ("a", "an", "the") are deliberately excluded, and so is "by". An
// article that belongs to a longer title sits BEFORE the requested word ("The
// Treasure of Khan"), where the prefix check already looks; AFTER the word it
// almost always opens a trailing descriptor that a release appends to the real
// title -- "Outliers: The Story of Success", "Treasure - A Dirk Pitt Novel".
// Treating those as extensions would reject the correct release for a large
// class of one-word titles. "by" introduces the author, for the same reason.
// An "or" that opens the book's alternate title does not extend it either
// (see opensAlternateTitle).
var titleExtensionConnectives = map[string]bool{
	"of": true, "and": true, "or": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "from": true,
}

// releaseMetaTokens are release-name words that describe the FILE rather than
// the work: bitrate/encoding markers, edition and narration qualifiers. Like
// years, formats and series markers they carry no evidence that the release is
// a different book, so they are benign wherever they trail a title.
var releaseMetaTokens = map[string]bool{
	"kbps": true, "kbit": true, "vbr": true, "abr": true,
	"khz": true, "mono": true, "stereo": true, "bitrate": true,
	"retail": true, "unabridged": true, "abridged": true, "complete": true,
	"audiobook": true, "audio": true, "ebook": true, "edition": true,
	"read": true, "narrated": true, "narrator": true, "novel": true,
}

// benignTrailingToken reports whether tok, appearing after a connective that
// follows the matched title, is compatible with the release still BEING that
// title. It extends benignPrefixToken (author tokens, bare numbers, stop words,
// series markers) with the vocabulary that only ever trails a title: file
// formats, the bitrate/edition markers above, and release language tags. A word
// outside all of those is a real foreign word -- the rest of a longer title.
func benignTrailingToken(tok string, authorToks []string) bool {
	if benignPrefixToken(tok, authorToks) {
		return true
	}
	if releaseMetaTokens[tok] {
		return true
	}
	// Digit-led tokens are sizes, bitrates and disc counts ("64kbps", "128k",
	// "3cd"); benignPrefixToken already covers the bare-number form. No title
	// word starts with a digit.
	if tok[0] >= '0' && tok[0] <= '9' {
		return true
	}
	if _, ok := releaseLanguageTags[tok]; ok {
		return true
	}
	for _, f := range formatTokens {
		if tok == f {
			return true
		}
	}
	return false
}

// keywordExtendsIntoLongerTitle reports whether every occurrence of kw in
// normResult is immediately followed by a connective plus a further significant
// word, i.e. the release title does not stop at kw but runs on into a longer,
// different work: "the treasure of khan" for the book "Treasure" (#1731).
//
// This is the right-hand counterpart to matchAnchored, and it exists because
// author corroboration structurally cannot separate these two: "The Treasure of
// Khan" is by the same Clive Cussler as "Treasure", so the author check added
// by #1539 and #1063 passes for both. A single-word title carries no phrase
// context either, so without this it matches every longer title in its author's
// catalogue that happens to contain that word.
//
// The scan stops at the FIRST significant word after the connective and asks
// only whether that word is benign (see benignTrailingToken); it deliberately
// does not read on to the end of the release. Reading further would find the
// narrator, release group or scene tag of a perfectly correct release and
// reject it.
//
// Only a release where EVERY occurrence extends is rejected: one clean
// occurrence of the word is enough to believe the release names the book.
//
// raw is the release name normResult was folded from. It is read only to tell
// an alternate title ("Frankenstein; or, The Modern Prometheus") from a longer
// title (see opensAlternateTitle). otherTitles are the author's other library
// books: an "or" followed by one of their names is a different book, however
// it is punctuated (see namesOtherTitle).
func keywordExtendsIntoLongerTitle(raw, normResult, kw string, authorToks, otherTitles []string) bool {
	re := WordBoundaryRegex(kw)
	toks := strings.Fields(normResult)
	found := false
	for i, tok := range toks {
		if !re.MatchString(tok) {
			continue
		}
		found = true
		if !extendsAfter(toks, i, authorToks) {
			return false
		}
		// Only an occurrence that would otherwise be rejected needs the raw
		// punctuation, so the release is tokenised here and not before.
		if rawToks, rawOK := tokenizeRelease(raw); rawOK && opensAlternateTitle(rawToks, i+1) &&
			!namesOtherTitle(toks, i, kw, otherTitles) {
			return false
		}
	}
	return found
}

// opensAlternateTitle reports whether the token at index i is an "or" that
// opens the book's alternate title rather than a longer, different title:
// "Frankenstein; or, The Modern Prometheus", "The Hobbit, or There and Back
// Again", "Frankenstein or The Modern Prometheus". The "or" counts when a comma
// or semicolon sits right before it, or an article right after it. A bare
// "or" between two words ("Truth or Dare") still extends the title.
func opensAlternateTitle(toks []relTok, i int) bool {
	if i >= len(toks) || toks[i].text != "or" {
		return false
	}
	if strings.ContainsAny(toks[i].sep, ",;") {
		return true
	}
	if i+1 < len(toks) {
		switch toks[i+1].text {
		case "the", "a", "an":
			return true
		}
	}
	return false
}

// namesOtherTitle reports whether the words after the "or" at toks[i+1] spell
// out another of the author's library books that begins with the wanted
// keyword: "Truth or The Consequences" and "Truth, or Dare" for Truth, "Dune
// or the Messiah" for a library holding "Dune Messiah". It compares what the
// other title carries after the keyword, with "or" and articles dropped, to
// what the release carries after its "or", articles dropped, as a prefix. It
// vetoes the alternate title reading, so such a release stays rejected.
func namesOtherTitle(toks []string, i int, kw string, otherTitles []string) bool {
	if len(otherTitles) == 0 || i+2 >= len(toks) {
		return false
	}
	after := dropOrArticles(toks[i+2:], false)
	if len(after) == 0 {
		return false
	}
	re := WordBoundaryRegex(kw)
	for _, o := range otherTitles {
		ot := strings.Fields(NormalizeRelease(o))
		if len(ot) < 2 || !re.MatchString(ot[0]) {
			continue
		}
		rest := dropOrArticles(ot[1:], true)
		if len(rest) > 0 && len(after) >= len(rest) && slices.Equal(after[:len(rest)], rest) {
			return true
		}
	}
	return false
}

// dropOrArticles removes articles, and the word "or" when dropOr is set, so
// "or the consequences" and "consequences" compare equal.
func dropOrArticles(toks []string, dropOr bool) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if t == "the" || t == "a" || t == "an" || (dropOr && t == "or") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// extendsAfter reports whether the token at index i is followed by a
// title-extending connective and then a significant, non-benign word.
// Insignificant words between the two ("of the khan") are skipped.
func extendsAfter(toks []string, i int, authorToks []string) bool {
	if i+1 >= len(toks) || !titleExtensionConnectives[toks[i+1]] {
		return false
	}
	for _, tok := range toks[i+2:] {
		if len(newznab.SigWords(tok)) == 0 {
			continue
		}
		return !benignTrailingToken(tok, authorToks)
	}
	// The connective ends the release name; there is nothing to extend into.
	return false
}

// titleMatchesResult returns true if the normalized result contains the
// significant words of the title either as a contiguous phrase or (for
// multi-word titles as a fallback) with every significant word appearing at
// a word boundary. A single-significant-word title additionally requires the
// author to be present (first+last for multi-token authors, surname-only for
// single-token authors; see authorMatchesRelease) and requires the release
// title not to run PAST that word into a longer, different title (see
// keywordExtendsIntoLongerTitle).
//
// A phrase or in-order hit alone is only trusted when it is ANCHORED (see
// matchAnchored): a release whose title merely embeds the requested title
// inside a longer, different one ("Reborn as an Assassin's Apprentice, Vol. 1
// by okiuta" for Robin Hobb's "Assassin's Apprentice", #1539) must name the
// requested author somewhere to be accepted. Requiring the author on EVERY
// phrase hit was considered and rejected — releases titled with just the book
// title and no author are a large legitimate class — so the author demand
// kicks in only when foreign words precede the phrase. Known tradeoff: a
// "SeriesName 01 - Title" release that names neither the author nor a bare
// series marker is now rejected; a wrong grab imports the wrong book, a missed
// grab retries on another release.
func titleMatchesResult(raw, normResult string, titleKws []string, authorToks, otherTitles []string, allowKwFallback bool) bool {
	switch len(titleKws) {
	case 0:
		return authorMatchesRelease(normResult, authorToks)
	case 1:
		if !WordBoundaryRegex(titleKws[0]).MatchString(normResult) {
			return false
		}
		// The word is present, but does the release title stop there? If it
		// runs on into a longer title the release is a different book by (very
		// often) the same author, which no amount of author checking can catch
		// (#1731). See keywordExtendsIntoLongerTitle.
		if keywordExtendsIntoLongerTitle(raw, normResult, titleKws[0], authorToks, otherTitles) {
			return false
		}
		if len(authorToks) == 0 {
			// No author tokens to anchor on — accept (can't do better).
			return true
		}
		return authorMatchesRelease(normResult, authorToks)
	default:
		if ContainsPhrase(normResult, titleKws) {
			if len(authorToks) == 0 {
				// No author tokens to corroborate with — accept (can't do better).
				return true
			}
			return matchAnchored(normResult, phraseRegex(titleKws), authorToks) ||
				authorMatchesRelease(normResult, authorToks)
		}
		if !allowKwFallback {
			return false
		}
		for _, kw := range titleKws {
			if !WordBoundaryRegex(kw).MatchString(normResult) {
				return false
			}
		}
		// All title words are present but not contiguous. Weak evidence: a
		// different work can contain the same words REORDERED ("Keep Your Doors
		// Locked" vs "Locked Doors"; "Secrets of the Human Body" vs "Body of
		// Secrets"). Accept only if the author anchors it, or the words appear in
		// title order (legit stop-word-separated titles like "The Lord of the
		// Rings") — and, mirroring the phrase branch, the in-order hit itself
		// must be anchored so an embedded title can't sneak back in through
		// this weaker path.
		if len(authorToks) > 0 && authorMatchesRelease(normResult, authorToks) {
			return true
		}
		if !containsInOrder(normResult, titleKws) {
			return false
		}
		return len(authorToks) == 0 || matchAnchored(normResult, inOrderRegex(titleKws), authorToks)
	}
}

// filterRelevant removes results that don't plausibly match the requested book.
// Strategy:
//   - Multi-significant-word titles: try a contiguous phrase match first; if
//     the phrase fails, accept the result if every significant keyword appears
//     at a word boundary (handles titles like "The Name of the Wind" where stop
//     words between sigWords prevent a direct phrase hit on the release title).
//   - Single-significant-word titles: require the word AND the author surname
//     at word boundaries (prevents "sparrow" alone from matching noise).
//   - Titles with no significant words: fall back to the author surname alone.
//   - Subtitle handling: if the title has "primary: subtitle", results matching
//     either the primary-only or the full title form are accepted.
//
// Each result is evaluated independently. The previous batch-level
// anyPhraseMatch gate (which disabled keyword fallback for the whole batch if
// any result happened to phrase-match) caused correctly-titled releases to be
// dropped when an abbreviated result set the gate — e.g. "Name.Wind.epub"
// enabling strict mode that then rejected "Name.of.the.Wind.epub".
func filterRelevant(results []newznab.SearchResult, title, author string, aliases []string) []newznab.SearchResult {
	kept, _ := filterRelevantDetailed(results, title, author, aliases)
	return kept
}

// filterRelevantDetailed is the one implementation of the relevance filter,
// returning the kept results and a record of each result it dropped and why.
// filterRelevant and filterRelevantDebug are both thin wrappers over it.
//
// There used to be two copies, and they drifted. The guards added for #2502
// (a release naming a different author for the same title, and title words
// that match only when scattered through a longer title) went into
// filterRelevant alone. But every production search runs the other copy:
// the scheduler reaches SearchBookWithOutcomes, which projects
// SearchBookWithDebug, and the interactive search calls SearchBookWithDebug
// directly. filterRelevant was reached only by the scheduler's fallback for
// searchers that cannot report outcomes, which in practice means test stubs.
// So #2502's fix was pinned by tests that never ran the production path, and
// automatic search kept grabbing the exact releases it was written to reject.
// The author guard was later copied across by hand (#2863); the title
// identity gate never was, so a same spelling "Volume 16" release was still
// kept for a "Volume 17" title (#2921). One implementation means a guard
// cannot land in one path and miss the other.
func filterRelevantDetailed(results []newznab.SearchResult, title, author string, aliases []string) ([]newznab.SearchResult, []FilterDebug) {
	return filterRelevantWithContext(results, title, author, aliases, bookContext{})
}

// filterRelevantWithContext is filterRelevantDetailed given what the library
// knows about the wanted book (see bookContext). A zero bookContext is
// filterRelevantDetailed exactly.
func filterRelevantWithContext(results []newznab.SearchResult, title, author string, aliases []string, bc bookContext) ([]newznab.SearchResult, []FilterDebug) {
	kept, dropped := filterRelevantKeepDropped(results, title, author, aliases, bc)
	return kept, droppedFilterDebug(dropped)
}

// filterRelevantKeepDropped is the relevance filter itself. It returns the
// dropped releases whole (not just a FilterDebug summary) so the interactive
// search can offer them for a deliberate grab (#2334). Every other form of the
// filter is a projection of this one, so the kept set cannot differ between
// the scheduler and the interactive path.
func filterRelevantKeepDropped(results []newznab.SearchResult, title, author string, aliases []string, bc bookContext) ([]newznab.SearchResult, []DroppedRelease) {
	// "Book One" and "Book 1" are the same position (#3476). Both the wanted
	// title and every release name are read with a spelled out number after a
	// volume word as digits, so the two spellings compare equal everywhere
	// below while "Book 10" and "Book 2" still differ. The judgement runs on
	// the folded copy; what is returned is always the release as the indexer
	// named it.
	orig := results
	results = foldResultTitles(results)
	title = newznab.FoldNumberWords(title)
	// The library titles the trailing rules compare against are read the same
	// way, or a spelled out series title would never equal the folded release.
	// Author aliases are names, not titles, and are deliberately not folded.
	bc.seriesTitle = newznab.FoldNumberWords(bc.seriesTitle)
	if len(bc.otherTitles) > 0 {
		folded := make([]string, len(bc.otherTitles))
		for i, t := range bc.otherTitles {
			folded[i] = newznab.FoldNumberWords(t)
		}
		bc.otherTitles = folded
	}
	// Strip edition qualifiers ("(German Edition)" etc.) and normalize
	// smart quotes before tokenizing, so they don't become spurious keywords.
	// Format qualifiers mid title ("(Light Novel)") go too, matching what the
	// query sent to the indexer (see newznab.StripFormatQualifiers).
	title = newznab.StripFormatQualifiers(newznab.NormalizeQueryTitle(title))
	// Strip possessive author prefix before keyword extraction.
	// "Tom Clancy's Rainbow Six" → "Rainbow Six" when author is "Tom Clancy",
	// preventing "clancys" from becoming a keyword that fails to match releases
	// like "Tom Clancy - Rainbow Six". See issue #409.
	title = stripPossessivePrefix(title, author)
	fullKws := newznab.SigWords(title)
	primaryKws := newznab.SigWords(primaryTitle(title))
	// Elision fallback (see newznab.SigWordsElided): an elided title folds to
	// a single token no release name contains, because release names keep the
	// separator ("L'Outsider" vs "L.Outsider"). Tried only after the strict
	// set fails, so possessive matching ("Ender's Game" vs "Enders.Game") is
	// untouched.
	fullElided := newznab.SigWordsElided(title)
	primaryElided := newznab.SigWordsElided(primaryTitle(title))
	fullIdentity := titleIdentityKeywords(title)
	primaryIdentity := titleIdentityKeywords(primaryTitle(title))
	fullIdentityElided := titleIdentityKeywords(newznab.ElideApostrophes(title))
	primaryIdentityElided := titleIdentityKeywords(newznab.ElideApostrophes(primaryTitle(title)))
	authorKws := newznab.SigWords(author)
	// A "Series N: Title" name also gets the book's own title as a reading,
	// because releases name the book, not its series position ("The Mining
	// Company Contract" for "Feral Mage 4: The Mining Company Contract"). It
	// needs two significant words so a one word title cannot turn the author's
	// whole catalogue into matches, and it is tried only after the full and
	// primary readings both fail. See newznab.SeriesPositionTitle.
	seriesTitle := newznab.SeriesPositionTitle(title)
	seriesKws := newznab.SigWords(seriesTitle)
	if len(seriesKws) < 2 || sameKws(seriesKws, fullKws) || sameKws(seriesKws, primaryKws) {
		seriesTitle, seriesKws = "", nil
	}
	seriesIdentity := titleIdentityKeywords(seriesTitle)

	authorTokenSets := latinAliasTokenSets(author, aliases)

	tryMatch := func(raw, n string, kws []string) bool {
		for _, toks := range authorTokenSets {
			if titleMatchesResult(raw, n, kws, toks, bc.otherTitles, true) {
				return true
			}
		}
		return false
	}

	// tryMatchElided retries a FAILED strict match with the apostrophe-
	// separated reading of the same title. Never tried first, and skipped
	// when the two readings are identical, so every result that matches today
	// still matches for the same reason.
	tryMatchElided := func(raw, n string, elided, kws []string) bool {
		if len(elided) == 0 || sameKws(elided, kws) {
			return false
		}
		return tryMatch(raw, n, elided)
	}

	// identityOK applies the title-identity gate, accepting the elided reading
	// as well. "Sac d'os" folds to the identity words [sac dos], which
	// "Sac.d.os.2015.FR" ([sac 2015]) cannot satisfy; the separated reading
	// yields [sac], which it can.
	identityOK := func(haystack string, ident, elided []string) bool {
		if len(ident) < 2 {
			return true
		}
		if ContainsPhrase(haystack, ident) {
			return true
		}
		return len(elided) > 0 && ContainsPhrase(haystack, elided)
	}

	if len(fullKws) == 0 && len(primaryKws) == 0 && len(authorKws) == 0 {
		return orig, nil
	}

	// Pre-normalize all result titles once.
	normTitles := make([]string, len(results))
	for i, r := range results {
		normTitles[i] = NormalizeRelease(r.Title)
	}

	drop := func(r newznab.SearchResult, reason string) DroppedRelease {
		return DroppedRelease{Result: r, Stage: StageRelevance, Reason: reason}
	}
	// originalOf returns the release as the indexer named it, for the kept and
	// dropped lists; results holds the folded copy the judgement reads.
	originalOf := func(i int) newznab.SearchResult { return orig[i] }

	rules := newTrailingRules(title, bc, fullIdentity, fullIdentityElided, primaryIdentity, primaryIdentityElided, seriesIdentity)
	subtitle := newSubtitleInfo(title)

	authorConflict := func(r newznab.SearchResult) bool {
		return conflictingTitleAuthor(r.Title, title, authorTokenSets) ||
			conflictingTitleAuthor(r.Title, primaryTitle(title), authorTokenSets) ||
			(seriesTitle != "" && conflictingTitleAuthor(r.Title, seriesTitle, authorTokenSets))
	}

	// evalFull reads one result against the whole title: whether its keywords
	// and identity match, whether it is approved on them, and why not when
	// the trailing rules (#3268) reject it. The title is whole in the release
	// but the release may go on to name another book; a release that carries
	// the full title and is rejected there is not retried under the shorter
	// readings, which would only match the same words again.
	evalFull := func(i int) (keywords, ok bool, trailing string) {
		r, n := results[i], normTitles[i]
		identity := strings.Join(titleIdentityWords(r.Title), " ")
		keywords = tryMatch(r.Title, n, fullKws) || tryMatchElided(r.Title, n, fullElided, fullKws)
		ok = keywords &&
			identityOK(identity, fullIdentity, fullIdentityElided) &&
			volumeNumberAgrees(n, title)
		if ok {
			if trailing = rules.check(r.Title, n, fullIdentity, fullIdentityElided); trailing != "" {
				ok = false
			}
		}
		return keywords, ok, trailing
	}

	// fullTitleTypes is the set of media types for which some result in the
	// batch carries the whole title, is approved on it, is the same book (no
	// summary or study aid wording, nothing but release furniture after the
	// title) and has a determinable media type. The main title reading is
	// judged against it (see subtitleInfo.mainTitleOnly): a release that names
	// only the main title is a sibling of one that names the book, but only a
	// sibling of the same media type. Computed on first use, so a batch that
	// never reaches the main title reading pays nothing.
	var fullTitleTypes map[string]bool
	anyFullInBatch := func(mediaType string) bool {
		if fullTitleTypes == nil {
			fullTitleTypes = map[string]bool{}
			for i, r := range results {
				if authorConflict(r) {
					continue
				}
				if _, ok, _ := evalFull(i); !ok {
					continue
				}
				if mt := releaseMediaType(r.Title); mt != "" &&
					fullTitleCounts(r.Title, normTitles[i], authorTokenSets, fullIdentity, fullIdentityElided) {
					fullTitleTypes[mt] = true
				}
			}
		}
		return fullTitleTypes[mediaType]
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	var dropped []DroppedRelease
	for i, r := range results {
		n := normTitles[i]
		if authorConflict(r) {
			dropped = append(dropped, drop(originalOf(i), "release names a different author for this title"))
			continue
		}
		// Only insignificant connecting words may separate title keywords.
		// Author corroboration cannot make inserted title words disappear.
		identity := strings.Join(titleIdentityWords(r.Title), " ")

		// allowFallback=true: each result gets phrase match first, then keyword
		// fallback if the phrase fails.
		fullKeywords, fullOK, trailing := evalFull(i)
		primaryKeywords, primaryOK := false, false
		if !fullOK && trailing == "" && len(primaryKws) > 0 && !sameKws(primaryKws, fullKws) {
			primaryKeywords = tryMatch(r.Title, n, primaryKws) || tryMatchElided(r.Title, n, primaryElided, primaryKws)
			primaryOK = primaryKeywords &&
				identityOK(identity, primaryIdentity, primaryIdentityElided) &&
				volumeNumberAgrees(n, primaryTitle(title))
			if primaryOK {
				if trailing = rules.check(r.Title, n, primaryIdentity, primaryIdentityElided); trailing == "" {
					// The release carries the main title and not the whole
					// one; is it still this book (#3268, part 2)?
					trailing = subtitle.mainTitleOnly(rules, r.Title, n, identity, anyFullInBatch, primaryIdentity, primaryIdentityElided)
				}
				if trailing != "" {
					primaryOK = false
				}
			}
		}
		seriesKeywords, seriesOK := false, false
		if !fullOK && !primaryOK && trailing == "" && len(seriesKws) > 0 {
			seriesKeywords = tryMatch(r.Title, n, seriesKws)
			seriesOK = seriesKeywords &&
				(len(seriesIdentity) < 2 || ContainsPhrase(identity, seriesIdentity)) &&
				volumeNumberAgrees(n, seriesTitle)
			if seriesOK {
				if trailing = rules.check(r.Title, n, seriesIdentity); trailing != "" {
					seriesOK = false
				}
			}
		}
		if fullOK || primaryOK || seriesOK {
			filtered = append(filtered, originalOf(i))
			continue
		}
		reason := "title/author keywords did not match release name"
		if trailing != "" {
			reason = trailing
		} else if fullKeywords || primaryKeywords || seriesKeywords {
			// The keywords are all there but the release's title is not this
			// one: other words sit between them, as "12 Rules for Life" inside
			// "12 More Rules for Life", or a number differs, as "Volume 16"
			// for "Volume 17".
			reason = "title words appear, but with other words or numbers among them"
		}
		dropped = append(dropped, drop(originalOf(i), reason))
	}
	return filtered, dropped
}

// foldResultTitles returns results with each Title run through
// newznab.FoldNumberWords. The slice is copied only when a title changes, so the
// common search allocates nothing.
func foldResultTitles(results []newznab.SearchResult) []newznab.SearchResult {
	var out []newznab.SearchResult
	for i, r := range results {
		folded := newznab.FoldNumberWords(r.Title)
		if folded == r.Title {
			continue
		}
		if out == nil {
			out = make([]newznab.SearchResult, len(results))
			copy(out, results)
		}
		out[i].Title = folded
	}
	if out == nil {
		return results
	}
	return out
}

// latinAliasTokenSets builds the candidate author token sets used to anchor a
// release against its author. The primary set is always `author` itself. When
// the author's name is written in a non-latin script (e.g. "村上春樹"), the
// token sets of any latin-script aliases (e.g. "Haruki Murakami") are added so
// release names romanised by indexers are not filtered out. Each set is used
// independently: a release matching any one alias' tokens is accepted.
//
// The decision of which aliases qualify is textutil.LatinAliasBinds, shared
// with the alias write path and the Calibre import path. It used to be a
// surname-only test here, which disagreed with the other two for any author
// whose given name and surname are in different scripts (#2419).
func latinAliasTokenSets(author string, aliases []string) [][]string {
	sets := [][]string{authorTokens(author)}
	for _, alias := range aliases {
		if !textutil.LatinAliasBinds(author, alias) {
			continue
		}
		if toks := authorTokens(alias); len(toks) > 0 {
			sets = append(sets, toks)
		}
	}
	return sets
}

func sameKws(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DedupeResults removes duplicate search results (by GUID, falling back to
// title+URL when the GUID is empty). Callers fanning out multiple SearchBook
// calls (e.g. dual-format books) use this to merge the per-format result sets.
func DedupeResults(results []newznab.SearchResult) []newznab.SearchResult {
	return dedupe(results)
}

func dedupe(results []newznab.SearchResult) []newznab.SearchResult {
	seen := make(map[string]bool)
	deduped := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		key := r.GUID
		if key == "" {
			key = r.Title + r.NZBURL
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, r)
	}
	return deduped
}

// rankResults sorts results in place by a composite score combining format
// quality, edition markers (RETAIL/UNABRIDGED/ABRIDGED), year match against
// the book's release year, grabs, size, and an ISBN exact-match boost. The
// format term follows c.Profile's stored order when the profile has an
// opinion on the media type in question (quality_order.go); the profile is
// compiled once here, not per result.
func rankResults(results []newznab.SearchResult, c MatchCriteria) {
	type scored struct {
		r     newznab.SearchResult
		score float64
	}
	ranks := newProfileRanks(c.Profile)
	items := make([]scored, len(results))
	for i, r := range results {
		items[i] = scored{r, scoreResult(r, c, ranks)}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})
	for i, it := range items {
		results[i] = it.r
	}
}

const (
	// flatSizeCapMiB is the total-size cap the flat size bonus has always
	// applied.
	flatSizeCapMiB = 1024.0
	// sizeReferenceMinutes is the book length that cap implies for a typical
	// audiobook: ten hours. With a known runtime the size bonus becomes the
	// release's density scored against this reference, so a ten-hour book
	// keeps exactly the bonus it gets today (#2740).
	sizeReferenceMinutes = 600.0
	// maxGrabCount caps the popularity term. At 100 grabs the bonus reaches
	// about 20 points and stops there, so a release with thousands of grabs
	// cannot outweigh the format it carries (#2740).
	maxGrabCount = 100
)

// scoreResult computes the composite ranking score for a single result.
// Higher is better. ranks is c.Profile compiled by rankResults; nil means no
// profile.
//
// Two terms are weighted by the book rather than hardcoded (#2740). The size
// bonus is normalised by the book's runtime when that runtime is known, so a
// long audiobook is not rewarded for being long, and the grabs bonus is
// capped, so a very popular release cannot outweigh the format it is. Both
// changes are deliberate edits to the existing weights, not a new scoring
// framework: no setting turns them on, and a book with no stored runtime
// scores exactly as it did before.
func scoreResult(r newznab.SearchResult, c MatchCriteria, ranks *profileRanks) float64 {
	p := ParseRelease(r.Title)

	quality := p.Format
	if quality == "" {
		quality = detectQuality(r.Title)
	}

	// Format term. When the search names a media type and the profile has an
	// opinion on it, the release scores by its best ticked token in the
	// profile's order, judged over every token in the title so "azw3 epub"
	// ranks by azw3 although ParseRelease reduces it to epub. Otherwise the
	// built in QualityRank applies exactly as it did before #2733.
	//
	// A search with no media type takes the QualityRank path even when a
	// profile is present, because a profile's ranks only compare inside one
	// list: an ebook list of 12 entries scores up to 12 while an audiobook
	// list of 5 scores up to 5. Every score in one search must be on one
	// scale, and the only criteria built without a media type today (free
	// text SearchQuery) carries no profile anyway.
	var score float64
	if c.MediaType != "" && ranks.hasOpinion(c.MediaType) {
		score = float64(ranks.formatScore(ReleaseFormats(r.Title), c.MediaType)) * 100
	} else {
		score = float64(models.QualityRank[quality]) * 100
	}

	// Media-type mismatch penalty. An ebook grab returning an audiobook
	// format (or vice-versa) is almost certainly the wrong kind of release
	// — knock it way down so correct-type results with weaker quality still
	// win. Neutral (unknown) formats aren't penalised either way.
	if c.MediaType != "" && quality != "unknown" {
		if c.MediaType == models.MediaTypeAudiobook && !IsAudiobookFormat(quality) {
			score -= 500
		}
		if c.MediaType == models.MediaTypeEbook && IsAudiobookFormat(quality) {
			score -= 500
		}
	}

	switch {
	case p.Retail:
		score += 50
	case p.Unabridged:
		score += 30
	case p.Abridged:
		score -= 50
	}

	if c.Year > 0 && p.Year > 0 {
		diff := c.Year - p.Year
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff == 0:
			score += 20
		case diff <= 2:
			score += 10
		case diff <= 5:
			score += 5
		default:
			score -= 5
		}
	}

	// The popularity bonus is capped. log10(grabs+1)*10 is unbounded, so
	// without a ceiling a release with a few thousand grabs can carry more
	// points than the format and size terms together; at 100 grabs the bonus
	// reaches about 20 points and stops there (#2740).
	if r.Grabs > 0 {
		grabs := r.Grabs
		if grabs > maxGrabCount {
			grabs = maxGrabCount
		}
		score += math.Log10(float64(grabs+1)) * 10
	}

	// Size term. Total size rewards a long book for being long, so when the
	// book's runtime is known an audio release is scored by its density in
	// MiB per minute instead, at the points-per-MiB the flat term used for a
	// ten-hour book and capped where that term's 1024 MiB cap lands. A
	// ten-hour book therefore scores exactly what it did, and a shorter or
	// longer one no longer gains or loses points for its length. An unknown
	// runtime, or a release that is not an audio container, keeps the flat,
	// capped bonus exactly as it was (#2740).
	if r.Size > 0 {
		mb := float64(r.Size) / (1024 * 1024)
		if c.DurationSeconds > 0 && IsAudiobookFormat(quality) {
			density := mb / (float64(c.DurationSeconds) / 60)
			if density > flatSizeCapMiB/sizeReferenceMinutes {
				density = flatSizeCapMiB / sizeReferenceMinutes
			}
			score += density * (sizeReferenceMinutes / 100)
		} else {
			if mb > flatSizeCapMiB {
				mb = flatSizeCapMiB
			}
			score += mb / 100
		}
	}

	if c.ISBN != "" && p.ISBN != "" && strings.EqualFold(p.ISBN, c.ISBN) {
		score += 200
	}
	// ASIN match is a near-certain identifier for audiobooks.
	if c.ASIN != "" && strings.Contains(strings.ToUpper(r.Title), strings.ToUpper(c.ASIN)) {
		score += 250
	}

	// Indexer priority: each priority point adds directly to the score so a
	// higher-priority indexer wins ties and can outweigh small quality gaps.
	// Default priority is 0, so deployments that never configure it are unaffected.
	score += float64(r.IndexerPriority)

	return score
}

// IsAudiobookFormat reports whether the format token names an audio container.
// Exported so the importer can classify a download's release format without
// keeping its own copy of the token lists (they drifted).
func IsAudiobookFormat(format string) bool {
	switch format {
	case "m4b", "m4a", "mp3", "flac", "ogg":
		return true
	}
	return false
}

// usenetJunkRe matches raw per-article Usenet posting titles that some
// indexers surface alongside (or instead of) the aggregated release:
// individual RAR parts, PAR2 recovery blocks, SFV checksums, yEnc
// markers, and "[N/M]" post-index brackets. Grabbing one of these
// produces a partial/unusable download, so they're filtered upstream
// rather than ranked.
var usenetJunkRe = regexp.MustCompile(
	`(?i)` +
		`\.part\d+\.rar\b` + `|` + // File.part03.rar
		`\.vol\d+\+\d+\.par2\b` + `|` + // File.vol003+004.par2
		`\.sfv\b` + `|` + // File.sfv
		`\byEnc\b` + `|` + // trailing yEnc marker
		`\[\d+/\d+\]`, // [12/22] post-index bracket
)

// filterUsenetJunk drops results whose titles look like raw per-article
// postings rather than coherent releases.
func filterUsenetJunk(results []newznab.SearchResult, sc junkScope) []newznab.SearchResult {
	out := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		if usenetJunkReason(r.Title, sc) == "" {
			out = append(out, r)
		}
	}
	return out
}

// videoMarkerRe matches release-name tokens that only appear on movie/TV
// video content: resolutions, video codecs, rip/source tags, and television
// season-episode numbering. No legitimate ebook or audiobook release carries
// any of these, so a single match disqualifies the result outright (#1591:
// a movie sharing a few title words with the requested book was auto-grabbed
// and imported as its audiobook).
var videoMarkerRe = regexp.MustCompile(
	`(?i)\b(?:` +
		`(?:480|576|720|1080|2160)[pi]` + `|` + // video resolutions
		`[xh]\.?26[45]|hevc|xvid|divx` + `|` + // video codecs
		`web-?rip|web-?dl|hdtv|bd-?rip|br-?rip|dvd-?rip|blu-?ray|bluray|remux` + `|` + // video rip sources
		`s\d{1,2}e\d{1,3}` + // S01E02 television numbering
		`)\b`,
)

// isNonBookCategory reports whether a result's self-reported Newznab category
// sits under a top-level category that can never contain book content:
// 1000 Console, 2000 Movies, 4000 PC, 5000 TV, 6000 XXX. Books are 7000s,
// audio(books) 3000s, 0/8000s are misc/other — those are left alone, as is
// anything empty or unparseable: only positive evidence drops a result.
func isNonBookCategory(cat string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(cat))
	if err != nil {
		return false
	}
	return (n >= 1000 && n < 3000) || (n >= 4000 && n < 7000)
}

// filterNonBookContent drops results that are demonstrably not book content:
// releases whose names carry video-only markers, and results the indexer
// itself filed under a movie/TV/console/PC category. Book searches already
// request book/audiobook categories, but some indexers ignore category
// filters on q= searches and return anything matching the title words
// (#1591) — this is the response-side backstop.
func filterNonBookContent(results []newznab.SearchResult) []newznab.SearchResult {
	kept, _ := filterNonBookContentDetailed(results)
	return kept
}

// filterNonBookContentDetailed is filterNonBookContent that also reports each
// drop and why. It is the one implementation, so the scheduler's SearchBook and
// the interactive SearchBookWithDebug cannot disagree about what is not a book.
// The drops are for the Filter decisions panel only: a video release or a movie
// category result is not a book, so it is never offered for a grab (#2334).
func filterNonBookContentDetailed(results []newznab.SearchResult) ([]newznab.SearchResult, []DroppedRelease) {
	out := make([]newznab.SearchResult, 0, len(results))
	var dropped []DroppedRelease
	for _, r := range results {
		if videoMarkerRe.MatchString(r.Title) {
			slog.Debug("dropping video release from book search", "title", r.Title, "indexer", r.IndexerName)
			dropped = append(dropped, DroppedRelease{Result: r, Stage: StageNonBook, Reason: "release name carries video markers (resolution, codec or rip tag)"})
			continue
		}
		if isNonBookCategory(r.Category) {
			slog.Debug("dropping non-book category result", "title", r.Title, "category", r.Category, "indexer", r.IndexerName)
			dropped = append(dropped, DroppedRelease{Result: r, Stage: StageNonBook, Reason: "indexer filed it under a non-book category (" + r.Category + ")"})
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// detectQuality scans a result title for known quality keywords and returns
// the best (highest-ranked) match found. Retained as a fallback for
// scoreResult when ParseRelease's word-boundary format detection misses
// (e.g. if a format token is jammed against other text without separators).
func detectQuality(title string) string {
	lower := strings.ToLower(title)
	best := "unknown"
	bestRank := 0
	for q, rank := range models.QualityRank {
		if q == "unknown" {
			continue
		}
		if strings.Contains(lower, q) {
			if rank > bestRank {
				bestRank = rank
				best = q
			}
		}
	}
	return best
}

// protocolForType maps an indexer type string to its protocol name.
func protocolForType(t string) string {
	if t == "torznab" {
		return "torrent"
	}
	return "usenet"
}

// releaseLanguageTags maps release-name language markers to the ISO 639-2/B
// code of the language they indicate. Matched at word boundaries against the
// normalized title — so "RUSSE" (French for "Russian") no longer falsely
// matches "RUSSELL". The codes use the same vocabulary the metadata-profile
// editor writes into allowed_languages, so a profile's language set can be
// checked against a release directly.
//
// Two kinds of marker live here. The first is the *name* of a language, in
// English or in another language ("german", "deutsch"). The second is a word
// that only appears in releases OF that language, typically the local word for
// "audiobook", "radio play" or "unabridged" (#2273): scene audiobook releases
// are named in the target language and frequently never spell out the English
// name of it, so "Luisterboek ... (NL Audiobook)" and "... (Ungekürzt)" carried
// no recognisable tag at all and passed an eng-only filter untouched.
//
// Adding a marker here is a widening of what counts as *tagged*, which makes
// the filter drop more, so a marker earns its place only when it is unambiguous
// across the languages this map already knows. Deliberately absent:
//   - "audiolibro" is both Spanish and Italian, and a tag maps to one code, so
//     including it would drop Italian releases from an ita-allowed profile.
//   - "audiokniha" is Czech and Slovak; Slovak has no entry here to lose to.
//   - Finnish, Estonian and Hungarian markers were suggested on #2273 but the
//     forms given were not right, and guessing at a marker is how false drops
//     get shipped. They can be added by someone who reads the language.
//
// Umlauts are listed here in their ASCII-expanded form, which is what
// NormalizeRelease produces, and releaseLanguageTagKeywords restores the real
// umlaut for the regex: only a keyword with an ä/ö/ü is allowed to match the
// compact spelling (#3123), so "ungekürzt" is what matches "Ungekürzt",
// "Ungekuerzt" and "Ungekurzt".
//
// Every code produced here must also be selectable in the profile editor, or
// the filter can drop a language no operator is able to allow.
// TestMetadataEditorLanguageVocabulary enforces that.
var releaseLanguageTags = map[string]string{
	"english": "eng",
	"french":  "fre", "francais": "fre",
	"vf": "fre", "vostfr": "fre", "vff": "fre",
	"livre audio": "fre",
	"german":      "ger", "deutsch": "ger",
	"hoerbuch": "ger", "ungekuerzt": "ger", "gekuerzt": "ger",
	// The same two words with the umlaut stripped outright by an indexer,
	// "Thron aus Asche und Gold 01 - King of Scars (ungekrzt)" (#3268). They
	// are not words in any other language, and the umlaut-flex cannot reach
	// them because there is no letter left to flex.
	"ungekrzt": "ger", "gekrzt": "ger",
	"spanish": "spa", "espanol": "spa", "español": "spa",
	"dutch": "dut", "netherlands": "dut", "nederlands": "dut",
	"luisterboek": "dut", "hoorspel": "dut",
	"italian": "ita", "italiano": "ita",
	"portuguese": "por", "portugues": "por", "audiolivro": "por",
	"russian": "rus", "russe": "rus",
	"japanese": "jpn", "japonais": "jpn",
	"chinese": "chi", "mandarin": "chi",
	"korean": "kor",
	"arabic": "ara", "arabe": "ara",
	"swedish": "swe", "svenska": "swe", "svensk": "swe", "ljudbok": "swe",
	"norwegian": "nor", "norsk": "nor", "lydbok": "nor",
	"danish": "dan", "dansk": "dan", "lydbog": "dan",
	"polish": "pol", "polski": "pol",
	"czech":   "cze",
	"turkish": "tur",
	"hindi":   "hin",
}

// releaseLanguageTagKeywords gives the markers whose ASCII form in
// releaseLanguageTags began as a German umlaut, in the keyword alphabet that
// lets the compact spelling match. The map keys stay ASCII because
// benignTrailingToken looks tags up by release-alphabet token.
var releaseLanguageTagKeywords = map[string]string{
	"hoerbuch": "hörbuch", "ungekuerzt": "ungekürzt", "gekuerzt": "gekürzt",
}

// releaseLanguageCodes returns the distinct language codes indicated by
// markers in the normalized release title. Empty means the release carries no
// recognisable language tag.
func releaseLanguageCodes(norm string) []string {
	var out []string
	seen := map[string]bool{}
	for tag, code := range releaseLanguageTags {
		if seen[code] {
			continue
		}
		kw := tag
		if k, ok := releaseLanguageTagKeywords[tag]; ok {
			kw = k
		}
		if WordBoundaryRegex(kw).MatchString(norm) {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

// sceneMarkerTokens are the words that mark the metadata suffix of a scene
// audiobook release name ("Author-Title-AUDIOBOOK-WEB-MP3-DE-2023-GRP").
// Everything after the last of them is release metadata, not title (#3271).
var sceneMarkerTokens = map[string]bool{
	"audiobook": true, "abook": true, "mp3": true, "m4b": true, "flac": true, "aac": true,
}

// sceneDiscRe matches the disc tokens scene audiobook groups put where a
// format marker would go: "4CD", "2MP3CD", "MP3CD". They never occur as title
// words, so they mark a scene suffix like a marker does (#3268).
var sceneDiscRe = regexp.MustCompile(`(?i)^(\d+cd|\d*mp3cd)$`)

// isSceneMarker reports whether tok starts or ends the marker part of a scene
// name: a format or audiobook word, or a disc token.
func isSceneMarker(tok string) bool {
	return sceneMarkerTokens[strings.ToLower(tok)] || sceneDiscRe.MatchString(tok)
}

// releaseLanguageCodeSet is every code releaseLanguageTags can produce. A
// scene code only counts when it normalizes into this set, so it stays inside
// the vocabulary the profile editor offers, and a token such as "web" or a
// group name that NormalizeLanguageCode passes through unchanged is ignored.
var releaseLanguageCodeSet = func() map[string]bool {
	set := make(map[string]bool, len(releaseLanguageTags))
	for _, code := range releaseLanguageTags {
		set[code] = true
	}
	return set
}()

var (
	// sceneTokenRe splits a raw release name into the same letter and digit
	// runs NormalizeRelease keeps as tokens.
	sceneTokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)
	// sceneTrailerRe matches what can follow the release group at the end of
	// a name: a usenet part counter ("(1/12)"), quotes and spaces, or a file
	// extension. They are stripped first so the last token is the group.
	sceneTrailerRe = regexp.MustCompile(`(?i)(\s*[(\[]\d+/\d+[)\]]|["'\s]+|\.(nzb|torrent|nfo|par2|rar|zip|epub|mobi|azw3?|pdf|mp3|m4b|m4a|flac))$`)
	sceneYearRe    = regexp.MustCompile(`^(19|20)\d\d$`)
)

// sceneLanguageCodes returns the language codes in the scene suffix of a raw
// release name: tokens after the last scene marker that are a language code
// ("DE", "POR", "NO"), read through models.NormalizeReleaseLanguageCode so
// the country codes groups use ("SE", "DK") count too. As bare words most of
// these are common ("de", "no", "it"), which is why #2274 left them out; they
// are only read after a scene marker, never in the title.
//
// It takes the raw name, not NormalizeRelease's, because the shape matters. A
// scene suffix is joined by separators ("-WEB-MP3-DE-2023-GRP"). One with
// spaces in it is usually prose ("(Audiobook) read by Ola De Vries", "MP3 -
// Hi Quality - 2010"), so there a token only counts when it is uppercase and
// sits right before a year or the group ("-DE-2005-GRP iNT").
func sceneLanguageCodes(title string) []string {
	for {
		trimmed := sceneTrailerRe.ReplaceAllString(title, "")
		if trimmed == title {
			break
		}
		title = trimmed
	}
	locs := sceneTokenRe.FindAllStringIndex(title, -1)
	toks := make([]string, len(locs))
	for i, loc := range locs {
		toks[i] = title[loc[0]:loc[1]]
	}
	out := sceneLeadingCodes(title, locs, toks)
	// The suffix starts after the last marker. A marker in first position is
	// a leading tag ("[MP3] Author - Title"), not a suffix, so it doesn't
	// count. The last token is the release group, and a group can be named
	// like a language code ("-2026-TR").
	start := -1
	for i, tok := range toks {
		if i > 0 && isSceneMarker(tok) {
			start = i + 1
		}
	}
	end := len(toks) - 1
	if start < 0 || start >= end {
		return out
	}
	spaced := strings.ContainsFunc(title[locs[start-1][1]:], unicode.IsSpace)
	for i := start; i < end; i++ {
		tok := toks[i]
		if spaced && !spacedSceneCode(toks, i, end) {
			continue
		}
		if code := models.NormalizeReleaseLanguageCode(strings.ToLower(tok)); releaseLanguageCodeSet[code] && !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	return out
}

// sceneLeadingCodes reads the other half of the scene shape (#3268): a code
// placed BEFORE the marker, "Nick Hornby-A Long Way Down-DE-AUDIOBOOK-4CD-FLAC-
// 2005-GRP" or "Title-3MP3CD-DE-2005-GRP" (the disc token is the marker there).
// The bare code is as common a word as it is after a marker, so every one of
// these has to hold, and a miss is cheaper than an English release dropped:
//
//   - the token is 2 or 3 ASCII capitals, and directly joined by one separator
//     on each side, to the token before it and to a marker or disc token after;
//   - nothing before the code is a title word in the same breath: the text
//     before it holds a lowercase letter, so an all-caps name, where capitals
//     say nothing ("STEPHEN_KING-IT-AUDIOBOOK-..."), is never read;
//   - from the code to a year there is no whitespace, the year is joined to
//     what follows, and a group follows the year, so the spaced prose of
//     human-written names never qualifies;
//   - "IT" and "NO" are never read here. They are English words and titles
//     ("Stephen_King-IT-AUDIOBOOK-..."), and capitals cannot tell them apart
//     from the codes. They still count AFTER a marker, where a title cannot be.
func sceneLeadingCodes(title string, locs [][]int, toks []string) []string {
	var out []string
	for i := 1; i+1 < len(toks); i++ {
		tok := toks[i]
		if len(tok) < 2 || len(tok) > 3 || strings.ToUpper(tok) != tok || strings.IndexFunc(tok, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			continue
		}
		if tok == "IT" || tok == "NO" || !isSceneMarker(toks[i+1]) || !sceneJoint(title[locs[i-1][1]:locs[i][0]]) || !sceneJoint(title[locs[i][1]:locs[i+1][0]]) {
			continue
		}
		if !strings.ContainsFunc(title[:locs[i][0]], unicode.IsLower) {
			continue
		}
		year := -1
		for j := i + 2; j+1 < len(toks); j++ {
			if sceneYearRe.MatchString(toks[j]) {
				year = j
				break
			}
		}
		if year < 0 || strings.ContainsFunc(title[locs[i][0]:locs[year][1]], unicode.IsSpace) || !sceneJoint(title[locs[year][1]:locs[year+1][0]]) {
			continue
		}
		if code := models.NormalizeReleaseLanguageCode(strings.ToLower(tok)); releaseLanguageCodeSet[code] && !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	return out
}

// spacedSceneCode decides whether the all-caps token at i counts as a language
// code inside a suffix that has spaces in it (end is the group, the last
// token). Two shapes qualify:
//
//   - right before a year: "-DE-2005-GRP iNT", "Audiobook - FR 2019". When
//     that year is the last token, so no group follows it, IT, NO and HI are
//     refused, being words ("Audiobook - IT 2017", "Hi 2010", "Dr. NO");
//   - right before the group, with a marker or a year directly before the
//     code: "Audiobook DE GRP", "ABOOK 2020 DK GRP". A group that is a bare
//     number ("MP3 PT 2") is a part counter, not a group, and IT, NO and HI
//     are refused here too ("3CD IT GRP").
//
// Anything else is prose ("read by Robert DE NIRO", "DE LUXE").
func spacedSceneCode(toks []string, i, end int) bool {
	tok := toks[i]
	if tok != strings.ToUpper(tok) {
		return false
	}
	if i+1 <= end && sceneYearRe.MatchString(toks[i+1]) {
		return i+1 != end || (tok != "IT" && tok != "NO" && tok != "HI")
	}
	if i+1 != end || i == 0 || strings.Trim(toks[end], "0123456789") == "" || tok == "IT" || tok == "NO" || tok == "HI" {
		return false
	}
	return isSceneMarker(toks[i-1]) || sceneYearRe.MatchString(toks[i-1])
}

// sceneJoint reports whether sep is exactly one scene separator, with no space
// either side of it.
func sceneJoint(sep string) bool {
	return sep == "-" || sep == "_" || sep == "."
}

// FilterByAllowedLanguages drops results whose release name is tagged with a
// language outside the metadata profile's allowed set. Untagged releases
// always pass — most releases carry no language marker, and dropping them
// would empty nearly every search; the tag check can only ever be a negative
// signal. An empty allowed list disables the filter.
//
// Profile codes are reduced through models.NormalizeLanguageCode, the same
// canonicaliser models.IsLanguageAllowed runs the book-level filter through,
// so both halves of one setting read it with one vocabulary: "en", "en-US",
// "deu" and "German" all mean "eng"/"ger" on both sides. This filter kept its
// own two-letter table until #2463, which knew neither region subtags nor
// ISO 639-2/T, so a profile written "pt-BR" accepted the book and then dropped
// every Portuguese release of it.
func FilterByAllowedLanguages(results []newznab.SearchResult, allowed []string) []newznab.SearchResult {
	kept, _ := SplitByAllowedLanguages(results, allowed)
	return kept
}

// SplitByAllowedLanguages is FilterByAllowedLanguages that also returns the
// results it removed, in their original order, so the interactive search can
// offer them for a deliberate grab (#2334). It is the one implementation.
func SplitByAllowedLanguages(results []newznab.SearchResult, allowed []string) (kept, dropped []newznab.SearchResult) {
	if len(allowed) == 0 {
		return results, nil
	}
	set := make(map[string]bool, len(allowed))
	for _, code := range allowed {
		if strings.ToLower(strings.TrimSpace(code)) == "any" {
			return results, nil
		}
		code = models.NormalizeLanguageCode(code)
		if code == "" {
			continue
		}
		set[code] = true
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		norm := NormalizeRelease(r.Title)
		ok := true
		for _, code := range append(releaseLanguageCodes(norm), sceneLanguageCodes(r.Title)...) {
			if !set[code] {
				ok = false
				break
			}
		}
		if ok {
			filtered = append(filtered, r)
		} else {
			dropped = append(dropped, r)
		}
	}
	return filtered, dropped
}

// FilterByLanguage removes results whose titles contain known foreign-language
// markers when lang is "en". When lang is "any" (or empty), all results pass.
// This is the global search.preferredLanguage setting's filter; profile-aware
// callers use FilterByAllowedLanguages directly.
func FilterByLanguage(results []newznab.SearchResult, lang string) []newznab.SearchResult {
	kept, _ := SplitByLanguage(results, lang)
	return kept
}

// SplitByLanguage is FilterByLanguage that also returns what it removed.
func SplitByLanguage(results []newznab.SearchResult, lang string) (kept, dropped []newznab.SearchResult) {
	if lang != "en" {
		return results, nil
	}
	return SplitByAllowedLanguages(results, []string{"eng"})
}
