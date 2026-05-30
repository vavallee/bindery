package newznab

import "strings"

// stopWords are common English words excluded from keyword significance checks.
// Must stay in sync with the set used by filterRelevant in the indexer package.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "in": true, "to": true, "by": true, "for": true,
	"with": true, "at": true, "from": true, "is": true, "it": true,
	"as": true, "on": true, "be": true,
}

// sigWordSeparators are the same separator characters that NormalizeRelease
// (internal/indexer/release.go) treats as word boundaries when normalising
// release names. Splitting here keeps the two sides of the relevance match
// symmetric: "Slaughterhouse-Five" on the title side becomes the same
// [slaughterhouse, five] pair that "Slaughterhouse-Five.epub" yields on the
// release side after NormalizeRelease. Without this split, single-word
// hyphenated titles produced one literal-hyphen keyword that could never
// match the de-hyphenated release string (#871).
var sigWordSeparators = "._-()[]|"

// SigWords returns the meaningful (non-stop, 3+ char) words from s.
// Apostrophes are stripped so "Ender's" produces the token "enders",
// matching the apostrophe-free form used in most release names.
// Hyphens and other NZB separators are split on so a title like
// "Slaughterhouse-Five" yields [slaughterhouse, five] (#871).
// German umlauts are transliterated (ä→ae etc.) to match NormalizeRelease.
func SigWords(s string) []string {
	var out []string
	normalised := strings.Map(func(r rune) rune {
		if strings.ContainsRune(sigWordSeparators, r) {
			return ' '
		}
		return r
	}, strings.ToLower(s))
	for _, w := range strings.Fields(normalised) {
		w = strings.ReplaceAll(w, "'", "")
		w = transliterateUmlauts(w)
		if len(w) >= 3 && !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// transliterateUmlauts maps German umlaut characters to their common ASCII
// two-letter equivalents (ä→ae, ö→oe, ü→ue, ß→ss). Must be called after
// strings.ToLower so only the lowercase forms need to be handled.
func transliterateUmlauts(s string) string {
	s = strings.ReplaceAll(s, "ä", "ae")
	s = strings.ReplaceAll(s, "ö", "oe")
	s = strings.ReplaceAll(s, "ü", "ue")
	s = strings.ReplaceAll(s, "ß", "ss")
	return s
}
