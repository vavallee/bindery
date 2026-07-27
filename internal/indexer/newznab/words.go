package newznab

import (
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
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
	for _, w := range strings.Fields(textutil.FoldForTitleMatch(s)) {
		if len(w) >= 3 && !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// foldForSigWordMatch reduces a haystack through the same CHARACTER-REWRITING
// steps SigWords applies to the words it emits: lowercase, strip both
// apostrophe forms, and transliterate German umlauts.
//
// Any string that a SigWords output is tested against must go through this, or
// the two sides live in different alphabets and the comparison silently fails
// (#1643). titleHasRelevantResult compared SigWords keywords against a merely
// lowercased release name, so a byte-identical result was rejected as a "canned
// feed": "Ender's Game" emitted the keyword "enders", which is absent from
// "ender's game". Every English possessive title was affected, and — perversely
// — every German title where BOTH sides carried the umlaut, which is exactly
// what the transliteration exists for.
//
// Word SPLITTING is deliberately not applied here: callers use strings.Contains
// against the whole haystack, so collapsing punctuation to spaces would only
// remove separators the substring test already tolerates.
func foldForSigWordMatch(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	return textutil.TransliterateUmlauts(s)
}
