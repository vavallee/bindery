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
// There are FOUR distinct comparison alphabets in this codebase (plus one
// outgoing-query reduction, listed fifth), and they legitimately differ. What
// was wrong before was that the difference was accidental and undocumented, and
// that each fold was copy-pasted at its point of use. They are named here so the
// next person can tell which one they want.
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
//  4. SLUGS / FOREIGN IDs — FoldForSlug, used by openlibrary.seriesSlug.
//     Must be stable and collision-free, not lenient. Different scripts must
//     produce different slugs (#1645), so this one deliberately does NOT
//     transliterate to ASCII — and, unlike (2) and (3), it strips diacritics
//     only where they ARE diacritics. Latin and Greek fold; every other script
//     keeps its marks, because in kana, Devanagari, Hebrew and Arabic the mark
//     is part of the letter and dropping it merges distinct works.
//
//  5. OUTGOING INDEXER QUERY — newznab.TransliterateQuery
//     (indexer/newznab/words.go). Not a comparison alphabet but a one-directional
//     query reduction, listed here so it is not overlooked. It expands German
//     umlauts (ö→oe) on the title/author sent to Newznab/Torznab indexers, whose
//     release names use that convention, and — deliberately, like alphabet (1) —
//     leaves other Latin diacritics and non-Latin scripts alone, so a result the
//     ASCII query returns still matches under FoldForTitleMatch. It lives in the
//     newznab package (an import cycle keeps it out of here) but is registered in
//     internal/normdrift alongside the folds above.
//
// If you are about to write a `strings.ToLower` next to a comparison, one of
// the first four above is what you actually want.

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

// FoldForSlug reduces s to alphabet (4): a stable, collision-free identity for
// a foreign ID. It NFC-composes, lowercases, folds the non-decomposable Latin
// letters, and strips diacritics FROM LATIN AND GREEK ONLY.
//
// That script restriction is the whole point, and it is what separates this
// fold from (2) and (3). A blanket NFD-plus-strip-Mn is correct for Latin,
// where a combining mark is a diacritic, and wrong for the scripts where the
// mark is part of the letter:
//
//   - Japanese: ズ is ス + U+3099 under NFD. Dropping the mark turns "ハード"
//     (hard) into "ハート" (heart) and puts two unrelated series on one row —
//     the exact collapse #1645 was filed to remove, reintroduced for kana.
//   - Devanagari/Tamil/Thai: vowel signs are SPACING marks (Mc). They are
//     neither letters nor Mn, so they also hit the separator branch and
//     shatter one title into fragments ("गोदान" → "ग-द-न").
//   - Hebrew niqqud and Arabic harakat are Mn and meaning-bearing.
//
// Greek DOES fold: ά/ή/ώ are accented forms of the same letter, and Greek
// convention drops the tonos entirely in all-caps, so "ΝΙΚΟΣ" and "Νίκος" must
// reach one key. Word-final sigma is normalised for the same reason —
// strings.ToLower maps Σ to σ but leaves an existing ς alone, which would
// otherwise split a title by case alone.
//
// Cyrillic deliberately does NOT fold even though NFD decomposes й into и plus
// a breve: those are separate letters of the alphabet, so folding would merge
// distinct names.
//
// Marks that survive are returned as-is; callers building a slug must treat
// them as word characters rather than separators.
func FoldForSlug(s string) string {
	s = norm.NFC.String(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = greekFinalSigma.Replace(s)
	s = FoldNonDecomposableLatin(s)

	var b strings.Builder
	b.Grow(len(s))
	prevFoldable := false
	for _, r := range s {
		switch {
		case foldsDiacritics(r):
			// Composed accented letter: decompose and keep only the base.
			for _, dr := range norm.NFD.String(string(r)) {
				if !unicode.Is(unicode.Mn, dr) {
					b.WriteRune(dr)
				}
			}
			prevFoldable = true
		case unicode.Is(unicode.Mn, r) && prevFoldable:
			// A mark on a Latin/Greek base that did not compose under NFC.
			// Still a diacritic, so drop it.
			continue
		default:
			b.WriteRune(r)
			prevFoldable = false
		}
	}
	return b.String()
}

// foldsDiacritics reports whether r belongs to a script whose combining marks
// are diacritics rather than letters. See FoldForSlug for why the set is
// exactly Latin and Greek.
func foldsDiacritics(r rune) bool {
	return unicode.Is(unicode.Latin, r) || unicode.Is(unicode.Greek, r)
}

// greekFinalSigma normalises word-final sigma to the medial form. Applied after
// lowercasing, so only the lowercase pair needs handling.
var greekFinalSigma = strings.NewReplacer("ς", "σ")

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
