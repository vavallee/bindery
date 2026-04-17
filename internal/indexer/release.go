package indexer

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ParsedRelease holds metadata extracted from a release/NZB title.
type ParsedRelease struct {
	Normalized   string
	Year         int
	Format       string
	Retail       bool
	Unabridged   bool
	Abridged     bool
	ReleaseGroup string
	ISBN         string
	ASIN         string // Audible ASIN when embedded in the release title
}

var (
	separatorRe  = regexp.MustCompile(`[._\-()\[\]|]+`)
	multiSpaceRe = regexp.MustCompile(`\s{2,}`)
	articleRe    = regexp.MustCompile(`\b(a|an|the|and|or|of)\b`)

	releaseYearRe  = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	releaseIsbnRe  = regexp.MustCompile(`\b97[89][\-\s]?\d[\-\s]?\d{3}[\-\s]?\d{5}[\-\s]?\d\b|\b97[89]\d{10}\b`)
	releaseAsinRe  = regexp.MustCompile(`\bB[0-9A-Z]{9}\b`)
	releaseGroupRe = regexp.MustCompile(`-([A-Za-z0-9]+)\s*$`)

	formatTokens = []string{"epub", "azw3", "azw", "mobi", "pdf", "djvu", "cbr", "cbz", "fb2", "lit", "rtf", "txt", "m4b", "m4a", "flac", "mp3", "ogg"}

	regexCache = sync.Map{} // map[string]*regexp.Regexp
	articleSet = map[string]bool{"a": true, "an": true, "the": true, "and": true, "or": true, "of": true}
)

// transliterateUmlauts maps German umlaut characters to their common ASCII
// two-letter equivalents (ä→ae, ö→oe, ü→ue, ß→ss). German NZB indexers
// almost universally use this convention in release names, so normalising both
// sides of a comparison to ASCII prevents false-negative title matches for
// German-language books. Must be called after strings.ToLower so only the
// lowercase forms need to be handled.
func transliterateUmlauts(s string) string {
	s = strings.ReplaceAll(s, "ä", "ae")
	s = strings.ReplaceAll(s, "ö", "oe")
	s = strings.ReplaceAll(s, "ü", "ue")
	s = strings.ReplaceAll(s, "ß", "ss")
	return s
}

// NormalizeRelease lowercases s and replaces NZB separators with single spaces.
// Parentheses, brackets, pipes and repeated separators are all collapsed.
// Apostrophes are stripped so possessive forms in book titles ("Ender's") match
// the corresponding release names which typically omit them ("Enders").
// German umlauts are transliterated to their ASCII equivalents (ä→ae etc.) to
// match the convention used by German-language NZB indexers like Scenenzbs.
func NormalizeRelease(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'", "") // "ender's" → "enders", "hitchhiker's" → "hitchhikers"
	s = transliterateUmlauts(s)
	s = separatorRe.ReplaceAllString(s, " ")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// StripArticles removes common English articles used as connectives from an
// already-normalized string. "the sparrow" → "sparrow".
func StripArticles(s string) string {
	s = articleRe.ReplaceAllString(s, "")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// umlautFlexRegex makes "ae", "oe", "ue" in an already-QuoteMeta'd keyword
// flexible by appending "?" to the second letter: "ae"→"ae?", "oe"→"oe?",
// "ue"→"ue?". This allows a single regex to match both the German
// umlaut-expanded form (ä→ae, as produced by transliterateUmlauts) and the
// compact form (ä→a) used by some NZB uploaders. Must be called AFTER
// regexp.QuoteMeta so the inserted "?" acts as a regex quantifier.
func umlautFlexRegex(kw string) string {
	kw = strings.ReplaceAll(kw, "ae", "ae?")
	kw = strings.ReplaceAll(kw, "oe", "oe?")
	kw = strings.ReplaceAll(kw, "ue", "ue?")
	return kw
}

// WordBoundaryRegex returns a cached case-insensitive \bkw\b regex for the
// given keyword. Safe for concurrent use. German umlaut expansions (ae/oe/ue)
// produced by transliterateUmlauts are treated as flexible so the regex
// matches both the expanded (ae) and compact (a) NZB-name conventions.
func WordBoundaryRegex(kw string) *regexp.Regexp {
	if v, ok := regexCache.Load(kw); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)\b` + umlautFlexRegex(regexp.QuoteMeta(kw)) + `\b`)
	regexCache.Store(kw, re)
	return re
}

// ContainsPhrase returns true if all words in phrase appear in haystack in the
// given order, separated only by non-word characters. haystack must already be
// normalized (lowercased, separators→space). German umlaut expansions in phrase
// words are matched flexibly (ae/oe/ue optionally contracts to a/o/u).
func ContainsPhrase(haystack string, phrase []string) bool {
	if len(phrase) == 0 {
		return true
	}
	parts := make([]string, len(phrase))
	for i, w := range phrase {
		parts[i] = umlautFlexRegex(regexp.QuoteMeta(strings.ToLower(w)))
	}
	pattern := `(?i)\b` + strings.Join(parts, `\W+`) + `\b`
	re, _ := regexCache.LoadOrStore(pattern, regexp.MustCompile(pattern))
	return re.(*regexp.Regexp).MatchString(haystack)
}

// ParseRelease extracts structured metadata from an indexer result title.
func ParseRelease(title string) ParsedRelease {
	p := ParsedRelease{}
	p.Normalized = NormalizeRelease(title)

	// Group: trailing "-SOMEGROUP" at end of original title
	if m := releaseGroupRe.FindStringSubmatch(title); len(m) == 2 {
		p.ReleaseGroup = m[1]
	}

	// Year: prefer the first valid year
	if y := releaseYearRe.FindString(title); y != "" {
		if n, err := strconv.Atoi(y); err == nil {
			p.Year = n
		}
	}

	// ISBN: normalize to digits-only
	if isbn := releaseIsbnRe.FindString(title); isbn != "" {
		p.ISBN = strings.NewReplacer("-", "", " ", "").Replace(isbn)
	}

	// ASIN (Audible identifier). Uppercase BXXXXXXXXX pattern, 10 chars.
	if asin := releaseAsinRe.FindString(title); asin != "" {
		p.ASIN = asin
	}

	// Format: first recognised format token in the normalized title
	for _, f := range formatTokens {
		if WordBoundaryRegex(f).MatchString(p.Normalized) {
			p.Format = f
			break
		}
	}

	upper := strings.ToUpper(title)
	p.Retail = strings.Contains(upper, "RETAIL")
	p.Unabridged = strings.Contains(upper, "UNABRIDGED")
	p.Abridged = !p.Unabridged && strings.Contains(upper, "ABRIDGED")

	return p
}

// AuthorSurname returns the last whitespace-separated token of author,
// lowercased. Returns "" for empty input.
func AuthorSurname(author string) string {
	fields := strings.Fields(author)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[len(fields)-1])
}

// IsArticle reports whether w is an English article/connective. Exported for
// tests; consumers generally call sigWords which already filters these.
func IsArticle(w string) bool { return articleSet[strings.ToLower(w)] }
