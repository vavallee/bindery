// Package indexer coordinates book searches across multiple Newznab/Torznab
// indexers, filters and ranks the returned releases, and exposes a release-
// name parser shared by the filter pipeline and the import path.
package indexer

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
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
// match exists. Substituting a standard fallback ID (3030, 7020) on such
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
	}
	if len(out) == 0 {
		if hasNonStandard {
			// Non-standard taxonomy: pass through untouched, opt-in included.
			return cats
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
	ctx, cancel := context.WithTimeout(ctx, searchBookTimeout)
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
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()

			client := s.makeClient(idx.URL, idx.APIKey)
			cats := filterCategoriesForMedia(idx.Categories, c.MediaType, idx.IncludeParentCategories)
			hits, err := client.BookSearch(ctx, c.Title, c.Author, cats)
			if err != nil {
				s.noteIndexerError(idx, err)
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				return
			}

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
	results = filterUsenetJunk(results)
	results = filterNonBookContent(results)
	results = filterRelevant(results, c.Title, c.Author, c.AuthorAliases)
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
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()

			client := s.makeClient(idx.URL, idx.APIKey)
			hits, err := client.Search(ctx, query, idx.Categories)
			if err != nil {
				s.noteIndexerError(idx, err)
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				return
			}

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
	results = filterUsenetJunk(results)
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
// German umlauts are transliterated by the normalization. Returns nil for
// empty / all-initials input — the caller should fall back to surname-only
// behaviour.
func authorTokens(author string) []string {
	if author == "" {
		return nil
	}
	var out []string
	for _, w := range strings.Fields(NormalizeRelease(author)) {
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
func keywordExtendsIntoLongerTitle(normResult, kw string, authorToks []string) bool {
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
	}
	return found
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
func titleMatchesResult(normResult string, titleKws []string, authorToks []string, allowKwFallback bool) bool {
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
		if keywordExtendsIntoLongerTitle(normResult, titleKws[0], authorToks) {
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
	// Strip edition qualifiers ("(German Edition)" etc.) and normalize
	// smart quotes before tokenizing, so they don't become spurious keywords.
	title = newznab.NormalizeQueryTitle(title)
	// Strip possessive author prefix before keyword extraction.
	// "Tom Clancy's Rainbow Six" → "Rainbow Six" when author is "Tom Clancy",
	// preventing "clancys" from becoming a keyword that fails to match releases
	// like "Tom Clancy - Rainbow Six". See issue #409.
	title = stripPossessivePrefix(title, author)
	fullKws := newznab.SigWords(title)
	primaryKws := newznab.SigWords(primaryTitle(title))
	authorKws := newznab.SigWords(author)
	surname := AuthorSurname(author)

	// Build candidate author token sets. The primary set is from `author`. When
	// the primary surname is non-ASCII (e.g. "春樹" for "村上春樹"), also
	// include token sets from any latin-script aliases (e.g.
	// "Haruki Murakami") so release names romanised by indexers are not
	// incorrectly filtered out. Each token set is used independently: a
	// release matching any one alias' tokens is accepted.
	authorTokenSets := [][]string{authorTokens(author)}
	if !isAllASCIILower(surname) {
		for _, alias := range aliases {
			if s := AuthorSurname(alias); s != "" && isAllASCIILower(s) {
				if toks := authorTokens(alias); len(toks) > 0 {
					authorTokenSets = append(authorTokenSets, toks)
				}
			}
		}
	}

	tryMatch := func(n string, kws []string) bool {
		for _, toks := range authorTokenSets {
			if titleMatchesResult(n, kws, toks, true) {
				return true
			}
		}
		return false
	}

	if len(fullKws) == 0 && len(primaryKws) == 0 && len(authorKws) == 0 {
		return results
	}

	// Pre-normalize all result titles once.
	normTitles := make([]string, len(results))
	for i, r := range results {
		normTitles[i] = NormalizeRelease(r.Title)
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	for i, r := range results {
		n := normTitles[i]

		// allowFallback=true: each result gets phrase match first, then keyword
		// fallback if the phrase fails. No batch-level gate.
		fullOK := tryMatch(n, fullKws)
		primaryOK := false
		if !fullOK && len(primaryKws) > 0 && !sameKws(primaryKws, fullKws) {
			primaryOK = tryMatch(n, primaryKws)
		}
		if fullOK || primaryOK {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// isAllASCIILower returns true when every byte in the lowercased s is 7-bit ASCII.
// AuthorSurname already returns lowercase, so this is equivalent to checking
// whether the surname string contains only ASCII characters.
func isAllASCIILower(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
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
// the book's release year, grabs, size, and an ISBN exact-match boost.
func rankResults(results []newznab.SearchResult, c MatchCriteria) {
	type scored struct {
		r     newznab.SearchResult
		score float64
	}
	items := make([]scored, len(results))
	for i, r := range results {
		items[i] = scored{r, scoreResult(r, c)}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})
	for i, it := range items {
		results[i] = it.r
	}
}

// scoreResult computes the composite ranking score for a single result.
// Higher is better. Weights are hardcoded (no profile UI in v0.4.0).
func scoreResult(r newznab.SearchResult, c MatchCriteria) float64 {
	p := ParseRelease(r.Title)

	quality := p.Format
	if quality == "" {
		quality = detectQuality(r.Title)
	}
	score := float64(models.QualityRank[quality]) * 100

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

	if r.Grabs > 0 {
		score += math.Log10(float64(r.Grabs+1)) * 10
	}

	if r.Size > 0 {
		mb := float64(r.Size) / (1024 * 1024)
		if mb > 1024 {
			mb = 1024
		}
		score += mb / 100
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
func filterUsenetJunk(results []newznab.SearchResult) []newznab.SearchResult {
	out := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		if !usenetJunkRe.MatchString(r.Title) {
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
	out := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		if videoMarkerRe.MatchString(r.Title) {
			slog.Debug("dropping video release from book search", "title", r.Title, "indexer", r.IndexerName)
			continue
		}
		if isNonBookCategory(r.Category) {
			slog.Debug("dropping non-book category result", "title", r.Title, "category", r.Category, "indexer", r.IndexerName)
			continue
		}
		out = append(out, r)
	}
	return out
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
var releaseLanguageTags = map[string]string{
	"english": "eng",
	"french":  "fre", "francais": "fre",
	"vf": "fre", "vostfr": "fre", "vff": "fre",
	"german": "ger", "deutsch": "ger",
	"spanish": "spa", "espanol": "spa", "español": "spa",
	"dutch": "dut", "netherlands": "dut",
	"italian": "ita", "italiano": "ita",
	"portuguese": "por", "portugues": "por",
	"russian": "rus", "russe": "rus",
	"japanese": "jpn", "japonais": "jpn",
	"chinese": "chi", "mandarin": "chi",
	"korean": "kor",
	"arabic": "ara", "arabe": "ara",
	"swedish": "swe", "svenska": "swe",
	"norwegian": "nor",
	"danish":    "dan",
	"polish":    "pol", "polski": "pol",
	"czech":   "cze",
	"turkish": "tur",
	"hindi":   "hin",
}

// iso639TwoLetterAliases maps common two-letter (ISO 639-1) codes to the
// 639-2/B codes used by releaseLanguageTags and the profile editor, so a
// hand-edited profile like "it,en" still filters correctly.
var iso639TwoLetterAliases = map[string]string{
	"en": "eng", "fr": "fre", "de": "ger", "nl": "dut", "es": "spa",
	"it": "ita", "pt": "por", "ja": "jpn", "zh": "chi", "ru": "rus",
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
		if WordBoundaryRegex(tag).MatchString(norm) {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

// FilterByAllowedLanguages drops results whose release name is tagged with a
// language outside the metadata profile's allowed set. Untagged releases
// always pass — most releases carry no language marker, and dropping them
// would empty nearly every search; the tag check can only ever be a negative
// signal. An empty allowed list disables the filter, and codes are normalized
// to the ISO 639-2/B vocabulary the profile editor writes ("en" → "eng").
func FilterByAllowedLanguages(results []newznab.SearchResult, allowed []string) []newznab.SearchResult {
	if len(allowed) == 0 {
		return results
	}
	set := make(map[string]bool, len(allowed))
	for _, code := range allowed {
		code = strings.ToLower(strings.TrimSpace(code))
		if alias, ok := iso639TwoLetterAliases[code]; ok {
			code = alias
		}
		if code == "any" {
			return results
		}
		set[code] = true
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		norm := NormalizeRelease(r.Title)
		ok := true
		for _, code := range releaseLanguageCodes(norm) {
			if !set[code] {
				ok = false
				break
			}
		}
		if ok {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// FilterByLanguage removes results whose titles contain known foreign-language
// markers when lang is "en". When lang is "any" (or empty), all results pass.
// This is the global search.preferredLanguage setting's filter; profile-aware
// callers use FilterByAllowedLanguages directly.
func FilterByLanguage(results []newznab.SearchResult, lang string) []newznab.SearchResult {
	if lang != "en" {
		return results
	}
	return FilterByAllowedLanguages(results, []string{"eng"})
}
