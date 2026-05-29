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
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
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
	// newClient is the factory used to create per-indexer newznab clients.
	// nil uses newznab.New, which builds a client with the SSRF-hardened
	// transport. Tests that run against httptest servers can inject a factory
	// that bypasses the dialer.
	newClient func(baseURL, apiKey string) *newznab.Client
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
// categories 7000 and 3000 are never sent — they cause indexers to return
// noisier, less-targeted result sets.
// AllowedLanguages is the author's metadata-profile language list; when it
// contains exactly "eng" (or "en"), foreign-tagged releases are filtered out.
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

// makeClient creates a newznab client using the injected factory, falling
// back to newznab.New (with its SSRF-hardened transport) when none is set.
func (s *Searcher) makeClient(baseURL, apiKey string) *newznab.Client {
	if s.newClient != nil {
		return s.newClient(baseURL, apiKey)
	}
	return newznab.New(baseURL, apiKey)
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
func filterCategoriesForMedia(cats []int, mediaType string) []int {
	// Newznab category convention: 7xxx is the Books parent (7020 ebook,
	// 7030 magazines), 3xxx is Audio (3030 audiobook). The bare parents
	// (7000 / 3000) are deliberately dropped: Prowlarr reports them for
	// generic book trackers and sending them as-is returns the entire
	// books or audio surface, which is noise.
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
	for _, c := range cats {
		if c/1000 == wantThousand && c != parent {
			out = append(out, c)
		}
		if c > 9999 {
			hasNonStandard = true
		}
	}
	if len(out) == 0 {
		if hasNonStandard {
			return cats
		}
		return fallback
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
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()

			client := s.makeClient(idx.URL, idx.APIKey)
			cats := filterCategoriesForMedia(idx.Categories, c.MediaType)
			hits, err := client.BookSearch(ctx, c.Title, c.Author, cats)
			if err != nil {
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
	results = filterRelevant(results, c.Title, c.Author, c.AuthorAliases)
	rankResults(results, c)
	return results
}

// SearchQuery performs a generic text search across all enabled indexers.
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
		wg.Add(1)
		go func(idx models.Indexer) {
			defer wg.Done()

			client := s.makeClient(idx.URL, idx.APIKey)
			hits, err := client.Search(ctx, query, idx.Categories)
			if err != nil {
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
// token list suitable for word-boundary matching. Significant means >=3 chars
// of letters/digits; shorter tokens (typically initials like "R." or "R")
// are treated as optional and dropped. German umlauts are transliterated to
// match NormalizeRelease. Returns nil for empty / all-initials input — the
// caller should fall back to surname-only behaviour.
func authorTokens(author string) []string {
	if author == "" {
		return nil
	}
	var out []string
	for _, w := range strings.Fields(strings.ToLower(author)) {
		w = strings.ReplaceAll(w, "'", "")
		w = strings.Trim(w, ".,;:()[]")
		w = transliterateUmlauts(w)
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

// titleMatchesResult returns true if the normalized result contains the
// significant words of the title either as a contiguous phrase or (for
// multi-word titles as a fallback) with every significant word appearing at
// a word boundary. A single-significant-word title additionally requires the
// author to be present (first+last for multi-token authors, surname-only for
// single-token authors); see authorMatchesRelease.
func titleMatchesResult(normResult string, titleKws []string, authorToks []string, allowKwFallback bool) bool {
	switch len(titleKws) {
	case 0:
		return authorMatchesRelease(normResult, authorToks)
	case 1:
		if !WordBoundaryRegex(titleKws[0]).MatchString(normResult) {
			return false
		}
		if len(authorToks) == 0 {
			// No author tokens to anchor on — accept (can't do better).
			return true
		}
		return authorMatchesRelease(normResult, authorToks)
	default:
		if ContainsPhrase(normResult, titleKws) {
			return true
		}
		if !allowKwFallback {
			return false
		}
		for _, kw := range titleKws {
			if !WordBoundaryRegex(kw).MatchString(normResult) {
				return false
			}
		}
		return true
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
		if c.MediaType == models.MediaTypeAudiobook && !isAudiobookFormat(quality) {
			score -= 500
		}
		if c.MediaType == models.MediaTypeEbook && isAudiobookFormat(quality) {
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

// isAudiobookFormat returns true if the format token is one of the
// recognised audio container types.
func isAudiobookFormat(format string) bool {
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

// knownForeignTags lists release-name markers indicating a non-English
// release. Matched at word boundaries against the normalized title — so
// "RUSSE" (French for "Russian") no longer falsely matches "RUSSELL".
var knownForeignTags = []string{
	"french", "francais",
	"vf", "vostfr", "vff",
	"german", "deutsch",
	"spanish", "espanol", "español",
	"dutch", "netherlands",
	"italian", "italiano",
	"portuguese", "portugues",
	"russian", "russe",
	"japanese", "japonais",
	"chinese", "mandarin",
	"korean",
	"arabic", "arabe",
	"swedish", "svenska", "norwegian", "danish",
	"polish", "polski",
	"czech",
	"turkish",
	"hindi",
}

// FilterByLanguage removes results whose titles contain known foreign-language
// markers when lang is "en". When lang is "any" (or empty), all results pass.
// Tag matching is word-boundary anchored to avoid false positives (e.g. the
// former "RUSSE" ⊂ "RUSSELL" bug).
func FilterByLanguage(results []newznab.SearchResult, lang string) []newznab.SearchResult {
	if lang == "" || lang == "any" {
		return results
	}
	if lang != "en" {
		return results
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		norm := NormalizeRelease(r.Title)
		foreign := false
		for _, tag := range knownForeignTags {
			if WordBoundaryRegex(tag).MatchString(norm) {
				foreign = true
				break
			}
		}
		if !foreign {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
