package newznab

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
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

// queryUmlautReplacer expands German umlauts (both cases, plus ß/ẞ) to the
// two-letter ASCII forms Usenet release names conventionally use. Applied
// before the generic diacritic fold in transliterateQuery, which would
// otherwise collapse ä to a — the release side expects ae.
var queryUmlautReplacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ẞ", "SS",
)

// transliterateQuery converts a metadata title or author name into the ASCII
// form Usenet release names conventionally use: German umlauts expand to
// their two-letter equivalents (ä→ae, ö→oe, ü→ue, ß→ss) and remaining Latin
// diacritics fold to their base letter (é→e, ú→u, ñ→n). Non-Latin scripts
// (CJK, Cyrillic, …) pass through unchanged.
//
// Only outgoing BookSearch queries use this (#1610): release names are almost
// universally ASCII-transliterated and indexers match query terms close to
// literally, so a query carrying "Phönix" misses releases named "Phoenix".
// The result-matching side (SigWords / NormalizeRelease / umlautFlexRegex)
// already speaks both forms, so results found by the transliterated query
// still match the original metadata title downstream.
func transliterateQuery(s string) string {
	return foldLatinDiacritics(queryUmlautReplacer.Replace(s))
}

// foldLatinDiacritics decomposes s (NFD), drops combining marks whose base
// letter is Latin, and recomposes (NFC), folding é→e, ú→u, ñ→n, ç→c. Marks
// on non-Latin bases are kept: a blanket Mn-strip (as the internal/db
// sort-key folder uses for ordering) would corrupt other scripts — Cyrillic
// й is и + combining breve and must stay й in a query. norm.Form values are
// stateless and safe for concurrent use, unlike a transform.Chain (#1374).
func foldLatinDiacritics(s string) string {
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	prevLatin := false
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			if prevLatin {
				continue // strip the accent from a Latin base letter
			}
			b.WriteRune(r) // keep marks on non-Latin bases (й, Greek tonos, …)
			continue
		}
		prevLatin = unicode.In(r, unicode.Latin)
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}
