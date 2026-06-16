package newznab

import (
	"strings"
	"unicode"
)

// stopWords are common English words excluded from keyword significance checks.
// Must stay in sync with the set used by filterRelevant in the indexer package.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "in": true, "to": true, "by": true, "for": true,
	"with": true, "at": true, "from": true, "is": true, "it": true,
	"as": true, "on": true, "be": true,
}

// SigWords returns the meaningful (non-stop, 3+ char) words from s.
//
// Tokenisation is kept symmetric with NormalizeRelease
// (internal/indexer/release.go): both strip apostrophes, transliterate German
// umlauts, and treat every run of non-alphanumeric characters as a word
// boundary. Keeping the two alphabets identical is what lets a title keyword
// line up with the release-side string. Any punctuation the metadata title
// carries but a release name omits — a trailing "!"/"?", a stray ","/":", a
// "%"/"#"/"$" glued to a word — would otherwise survive as a keyword that no
// release could ever contain: e.g. "Eat That Frog!" yielding "frog!" against a
// release named "Eat That Frog" (the hyphen variant of this was #871).
//
// Apostrophes (both ASCII ' and the Unicode ’) are stripped rather than split
// so "Ender's" produces the token "enders", matching the apostrophe-free form
// used in most release names. Unicode letters (CJK, accented Latin) count as
// word characters so non-Latin titles still tokenise.
func SigWords(s string) []string {
	var out []string
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	normalised := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return ' '
	}, s)
	for _, w := range strings.Fields(normalised) {
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
