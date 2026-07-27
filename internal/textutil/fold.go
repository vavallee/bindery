package textutil

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// This file is the single home for the character-level reductions used when
// two strings have to be compared for sameness. It exists because the same bug
// kept recurring: two sides of a comparison reduced through DIFFERENT
// alphabets, so the match was silently always-false (#940, #871, #1608, #1642,
// #1643, #1646, #1647).
//
// There are FOUR distinct comparison alphabets in this codebase, and they
// legitimately differ. What was wrong before was that the difference was
// accidental and undocumented, and that each fold was copy-pasted at its point
// of use. They are named here so the next person can tell which one they want.
//
//  1. TITLE / RELEASE MATCHING — FoldForTitleMatch.
//     German umlauts EXPAND (ö→oe) because that is the convention German NZB
//     indexers use in release names. Apostrophes are deleted rather than split
//     so "Ender's" meets "Enders". Every other non-alphanumeric run becomes a
//     separator. Unicode letters survive so CJK/Cyrillic titles still tokenise.
//     Used by indexer.NormalizeRelease, newznab.SigWords, importer title
//     matching. Producers and consumers of a title comparison MUST share it.
//
//  2. AUTHOR IDENTITY — NormalizeAuthorName (author.go).
//     Diacritics are STRIPPED (ö→o) rather than expanded, because author names
//     arrive from providers in every romanisation and the goal is one identity
//     per human. Punctuation collapses to spaces so "J.R.R." and "J. R. R."
//     agree. NormalizeAuthorNameWithVariants layers initial-compaction,
//     last-first inversion and umlaut expansion on top, so a name folded either
//     way still meets its counterpart.
//
//  3. SORT KEYS — db.authorSortKey.
//     Like (2) but additionally folds the non-decomposable Latin letters
//     (FoldNonDecomposableLatin) so a Scandinavian or Polish name sorts in the
//     expected A–Z place instead of after "Z". Lossy and ORDERING-ONLY; never
//     compare identities with it.
//
//  4. SLUGS / FOREIGN IDs — e.g. openlibrary.seriesSlug.
//     Must be stable and collision-free, not lenient. Different scripts must
//     produce different slugs (#1645), so this one deliberately does NOT
//     transliterate to ASCII.
//
// If you are about to write a `strings.ToLower` next to a comparison, one of
// the four above is what you actually want.

// TransliterateUmlauts maps German umlaut characters to their common ASCII
// two-letter equivalents (ä→ae, ö→oe, ü→ue, ß→ss). German NZB indexers almost
// universally use this convention in release names, so normalising both sides
// of a comparison to ASCII prevents false-negative title matches for
// German-language books. Must be called after strings.ToLower so only the
// lowercase forms need to be handled.
//
// This used to be copied verbatim into both internal/indexer and
// internal/indexer/newznab (the latter cannot import the former without an
// import cycle), which is the duplication that let #1643 happen.
func TransliterateUmlauts(s string) string {
	return umlautExpander.Replace(s)
}

var umlautExpander = strings.NewReplacer(
	"ä", "ae",
	"ö", "oe",
	"ü", "ue",
	"ß", "ss",
)

// FoldForTitleMatch reduces s to the alphabet used for every title/release
// comparison: NFC-composed, lowercased, apostrophes deleted, German umlauts
// expanded, and every run of non-letter/non-number characters collapsed to a
// single space. The result is trimmed and single-space separated.
//
// Both sides of a title comparison must pass through this, or they live in
// different alphabets and the comparison silently fails. Tokenisation POLICY
// (stop-words, minimum length) deliberately stays with the caller — only the
// character rewriting is shared.
//
// The NFC step is load-bearing and was missing from every open-coded copy of
// this fold. A combining mark is category Mn, not a letter, so in decomposed
// input it hits the separator branch and TRUNCATES the word at the accent:
// NFD "Café Society" folded to "cafe society" while the NFC spelling of the
// same title folded to "café society". Two tokens, never equal — the #1642
// failure mode arriving by a different route. macOS hands filenames back
// decomposed while every metadata provider returns composed, so the two forms
// meet constantly.
func FoldForTitleMatch(s string) string {
	s = norm.NFC.String(s)
	s = strings.ToLower(s)
	s = apostropheStripper.Replace(s)
	s = TransliterateUmlauts(s)
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(mapped), " ")
}

// apostropheStripper removes both the ASCII apostrophe and the Unicode right
// single quote. They are DELETED rather than turned into separators so a
// possessive reduces to one token ("ender's" → "enders"), which is the form
// most release names use.
var apostropheStripper = strings.NewReplacer("'", "", "’", "")

// FoldNonDecomposableLatin maps the Latin letters that NFD leaves intact —
// they are atomic code points, not base+combining-mark compositions — to an
// ASCII approximation. Applied to already-lowercased input; uppercase variants
// are unnecessary because every caller lowercases first.
//
// NFD + strip-Mn handles é→e, ö→o, ñ→n. It does nothing for ø, ł, æ, ß, þ, ð,
// đ, so a name like "Nesbø" or "Łukasz" keeps a non-ASCII leading letter
// unless this runs too.
func FoldNonDecomposableLatin(s string) string {
	return nonDecomposableFolder.Replace(s)
}

var nonDecomposableFolder = strings.NewReplacer(
	"ø", "o",
	"ł", "l",
	"æ", "ae",
	"œ", "oe",
	"ß", "ss",
	"þ", "th",
	"ð", "d",
	"đ", "d",
	"ħ", "h",
	"ı", "i",
	"ŀ", "l",
)
