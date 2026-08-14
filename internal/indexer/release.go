package indexer

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
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

// NormalizeRelease lowercases s and replaces every run of non-alphanumeric
// characters with a single space, so all punctuation (dots, dashes, brackets,
// pipes, and symbols like %, !, ?, #, $) collapses to word boundaries. This
// keeps the release side symmetric with SigWords (internal/indexer/newznab):
// whatever punctuation a metadata title carries, both sides reduce it to the
// same alphanumeric tokens. Unicode letters (\p{L}) and numbers (\p{N}) are
// preserved so CJK and accented-Latin release names survive.
// Apostrophes (both ASCII ' and the Unicode ’) are stripped first so possessive
// forms in book titles ("Ender's") match the corresponding release names which
// typically omit them ("Enders"). German umlauts are transliterated to their
// ASCII equivalents (ä→ae etc.) to match the convention used by German-language
// NZB indexers like Scenenzbs.
//
// The character rewriting itself lives in textutil.FoldForTitleMatch, which is
// the ONE place the title-comparison alphabet is defined — newznab.SigWords and
// the library-scan title matcher share it. It used to be copy-pasted per call
// site, which is how #1643 (and its ancestors) happened.
func NormalizeRelease(s string) string {
	return textutil.FoldForTitleMatch(s)
}

// StripArticles removes common English articles used as connectives from an
// already-normalized string. "the sparrow" → "sparrow".
func StripArticles(s string) string {
	s = articleRe.ReplaceAllString(s, "")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// wordSep is the character class separating tokens in a NormalizeRelease
// haystack: anything that is not a Unicode letter or number. It replaces `\b`
// in every matcher below.
//
// Go's regexp is RE2, where `\b` is defined against ASCII `\w` ([0-9A-Za-z_]).
// NormalizeRelease deliberately PRESERVES \p{L}/\p{N} so CJK and accented-Latin
// survive, which makes the two mutually incompatible: a position next to a
// non-ASCII letter has a non-\w rune on both sides, so it is never an ASCII
// word boundary. Any keyword whose first or last rune was non-ASCII therefore
// compiled to a regex that could not match anything, ever — "åsa", "josé",
// "nesbø", "刘慈欣" and every Cyrillic/Greek/Hebrew/Arabic token (#1642).
// German was the one script that worked, purely because transliterateUmlauts
// converts ä/ö/ü/ß to ASCII before the pattern is built, which is why this went
// unnoticed. Diacritics INTERIOR to a token ("miéville", "stanisław") always
// worked, since only the edges touch a boundary.
//
// RE2 has no lookaround, so the boundary is matched by CONSUMING the separator
// rather than asserting on it. That is safe here because every haystack is
// NormalizeRelease output (single-space-separated \p{L}/\p{N} tokens) and each
// matcher only asks whether a match exists.
const wordSep = `[^\p{L}\p{N}]`

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

// WordBoundaryRegex returns a cached case-insensitive regex matching kw as a
// whole token in a normalized haystack. Safe for concurrent use. German umlaut
// expansions (ae/oe/ue) produced by transliterateUmlauts are treated as
// flexible so the regex matches both the expanded (ae) and compact (a)
// NZB-name conventions. See wordSep for why this is not `\b` (#1642).
func WordBoundaryRegex(kw string) *regexp.Regexp {
	if v, ok := regexCache.Load(kw); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)(?:^|` + wordSep + `)` + umlautFlexRegex(regexp.QuoteMeta(kw)) + `(?:` + wordSep + `|$)`)
	regexCache.Store(kw, re)
	return re
}

// phraseRegex returns the cached contiguous-phrase regex for the given words:
// each word at a boundary, separated only by non-word characters, with German
// umlaut expansions matched flexibly (ae/oe/ue optionally contracts to a/o/u).
// Callers needing the match position (see matchAnchored in searcher.go) share
// this instead of rebuilding the pattern.
func phraseRegex(phrase []string) *regexp.Regexp {
	parts := make([]string, len(phrase))
	for i, w := range phrase {
		parts[i] = umlautFlexRegex(regexp.QuoteMeta(strings.ToLower(w)))
	}
	// wordSep rather than \b/\W so non-ASCII words match (#1642). The inner
	// separator stays a + run, matching the previous \W+ behaviour.
	pattern := `(?i)(?:^|` + wordSep + `)` + strings.Join(parts, wordSep+`+`) + `(?:` + wordSep + `|$)`
	re, _ := regexCache.LoadOrStore(pattern, regexp.MustCompile(pattern))
	return re.(*regexp.Regexp)
}

// ContainsPhrase returns true if all words in phrase appear in haystack in the
// given order, separated only by non-word characters. haystack must already be
// normalized (lowercased, separators→space). German umlaut expansions in phrase
// words are matched flexibly (ae/oe/ue optionally contracts to a/o/u).
func ContainsPhrase(haystack string, phrase []string) bool {
	if len(phrase) == 0 {
		return true
	}
	return phraseRegex(phrase).MatchString(haystack)
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

	// ISBN: normalize to digits-only. The regex accepts any \s separator
	// (tab, CR, NBSP, …), so strip every non-digit rather than just "-" and
	// " " — otherwise an exotic separator survives into p.ISBN and breaks
	// downstream exact-match comparisons. Found by FuzzParseRelease.
	if isbn := releaseIsbnRe.FindString(title); isbn != "" {
		var b strings.Builder
		for _, r := range isbn {
			if r >= '0' && r <= '9' {
				b.WriteByte(byte(r))
			}
		}
		p.ISBN = b.String()
	}

	// ASIN (Audible identifier). Uppercase BXXXXXXXXX pattern, 10 chars.
	if asin := releaseAsinRe.FindString(title); asin != "" {
		p.ASIN = asin
	}

	// Format: first recognised format token in the normalized title.
	//
	// "First" is in formatTokens order, NOT in the order the tokens appear in
	// the title, and formatTokens lists every ebook container ahead of every
	// audio one. A release carrying both — "… (Unabridged) M4B + PDF booklet" —
	// therefore parses to "pdf". Callers that must know which MEDIA TYPE a
	// release is for want ReleaseFormats, which reports all of them.
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
//
// It deliberately does NOT go through NormalizeRelease, unlike its neighbour
// authorTokens (#1609). Its only consumer is the latin-alias gate in
// filterRelevant, which asks "is this surname non-ASCII, so should I also try
// romanised aliases?". Transliteration would answer that question wrongly in
// the direction that LOSES matches: "Böll" folds to the ASCII "boell", which
// would close the gate and stop trying a German author's latin aliases
// altogether. Answering with the raw form only ever opens the gate wider, and
// the gate only ever ADDS candidate token sets.
//
// So the inconsistency with authorTokens is real and intended. Do not "fix" it
// without moving the gate's ASCII test somewhere it can widen instead of
// narrow.
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

// ReleaseFormats returns every recognised format token in title, in
// formatTokens order and without duplicates. ParsedRelease.Format keeps only
// the first, which is enough for quality ranking but wrong for deciding what
// KIND of release this is: a "… M4B + PDF" audiobook (an audio file plus a PDF
// booklet) reduces to "pdf" there, so a caller asking "ebook or audiobook?"
// gets "ebook" for an audiobook.
func ReleaseFormats(title string) []string {
	normalized := NormalizeRelease(title)
	var out []string
	for _, f := range formatTokens {
		if WordBoundaryRegex(f).MatchString(normalized) {
			out = append(out, f)
		}
	}
	return out
}

// MediaTypeForFormat maps a release format token ("epub", "m4b", …) to the
// media type whose slot it belongs to, or "" when the token is empty or not a
// format Bindery recognises.
//
// This is the single source of truth for the token → media-type mapping.
// Duplicating the token lists elsewhere let them drift: a copy in the importer
// listed "opus" and "aac", which ParseRelease can never produce, and any token
// added here would have been invisible to it.
func MediaTypeForFormat(token string) string {
	t := strings.ToLower(strings.TrimSpace(token))
	if !slices.Contains(formatTokens, t) {
		return ""
	}
	if IsAudiobookFormat(t) {
		return models.MediaTypeAudiobook
	}
	return models.MediaTypeEbook
}
