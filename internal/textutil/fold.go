package textutil

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
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
//  6. LIBRARY SEARCH — FoldForSearch.
//     The recall alphabet for the search box, and the only one that is STORED
//     and matched with SQL LIKE (books.search_key, authors.search_key,
//     author_aliases.search_key). SQLite folds ASCII A–Z and nothing else, in
//     LIKE and in COLLATE NOCASE alike, so "muller" never found "Müller" and a
//     decomposed query never found a composed row (#1660). This fold is
//     deliberately the most aggressive one here: NFKC (so full-width ＴＯＫＹＯ
//     and the ﬁ ligature reach the same key as their plain spellings), FULL
//     Unicode case folding rather than ToLower (so Straße meets STRASSE),
//     Latin and Greek diacritics stripped, "&" expanded to "and".
//     It is lossy BY DESIGN and is a RECALL key, never an identity: two
//     distinct works may share one search_key, which only means both are
//     offered to someone who typed either spelling. Never compare identities
//     with it, never store it as a foreign ID, never show it to a user.
//
// If you are about to write a `strings.ToLower` next to a comparison, one of
// the first four above is what you actually want. If you are about to write one
// next to a SQL LIKE, it is (6).
//
// docs/search-design.md carries the reasoning behind each choice and the
// primary sources it rests on, and internal/normdrift asserts the differences
// so a future edit cannot quietly erase one.

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
	// Marks first, table second, for the reason given in FoldForSearch: the
	// table keys on the bare æ ø œ ł ß, and a precomposed ǣ ǽ ǿ only becomes
	// one of them once NFD has dropped its accent. Run the other way round,
	// this function was not idempotent (ǣ went to æ, and æ to ae), which is a
	// defect in a function whose whole contract is a stable identity.
	//
	// This does change the slug for a title containing one of those six code
	// points, and seriesSlug builds a stored series ForeignID from it, so such
	// a series re-keys once. The set is Old English and Old Norse scholarly
	// orthography and the alternative was leaving an identity function that
	// gives two answers for one name.
	s = stripLatinGreekMarks(s)
	return FoldNonDecomposableLatin(s)
}

// stripLatinGreekMarks removes the combining marks that sit on a Latin or Greek
// base and leaves every other script's marks in place. Shared by FoldForSlug
// (4) and FoldForSearch (6) so the two cannot drift apart on the one decision
// that matters here: WHICH scripts have their marks dropped. See FoldForSlug
// for why the answer is Latin and Greek and nothing else.
//
// Input must already be lowercased and NFC-composed. Runes are decomposed one
// at a time rather than by normalizing the whole string, so a mark that never
// composed — and therefore belongs to whatever precedes it — is judged by its
// base's script rather than by its own, which has none.
func stripLatinGreekMarks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevFoldable := false
	for _, r := range s {
		switch {
		case foldsDiacritics(r):
			// Composed accented letter: decompose and keep only the base. NFD
			// is a FULL decomposition, so a Vietnamese letter carrying two
			// stacked marks (ộ) loses both — the case SQLite's own
			// remove_diacritics=1 documents as a bug it cannot fix without
			// breaking existing indexes.
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

// FoldForSearch reduces s to alphabet (6): the key stored in books.search_key,
// authors.search_key and author_aliases.search_key, and matched against a
// query folded by this same function.
//
// Each step earns its place:
//
//	NFKC                     ＴＯＫＹＯ→tokyo, ﬁre→fire, Ⅷ→viii, halfwidth
//	                         ﾊﾘｰ･ﾎﾟｯﾀｰ→ハリー・ポッター. Compatibility folding is
//	                         lossy and UAX #15 §1.2 warns against applying it to
//	                         arbitrary text, which is exactly why it is confined
//	                         to a key nobody ever displays.
//	cases.Fold               FULL case folding, not ToLower: ß→ss, ẞ→ss, ς→σ,
//	                         İ→i+U+0307 (the mark goes below). Unicode §3.13 is
//	                         explicit that lowercasing is not caseless matching —
//	                         ToLower leaves ß alone, so "Straße" and "STRASSE"
//	                         would never meet.
//	FoldNonDecomposableLatin ø ł æ œ ß þ ð đ ħ ı ŀ have NO decomposition, so NFD
//	                         alone leaves "Nesbø" unreachable by "nesbo". This is
//	                         the withdrawn UTR #30 distinction between accent
//	                         removal and diacritic removal, and it is also the
//	                         gap in SQLite's own remove_diacritics.
//	stripLatinGreekMarks     Latin and Greek only, for the reason spelled out on
//	                         FoldForSlug: a kana dakuten or a Devanagari nukta is
//	                         category Mn but part of the letter, and dropping it
//	                         merges unrelated words.
//	ё→е                      Cyrillic does not fold as a script here, but Russian
//	                         is routinely typed with е for ё, so this ONE pair
//	                         folds for recall. й stays distinct from и, because
//	                         those are two letters of the alphabet.
//	NFC                      Recompose. NFD leaves Hangul as conjoining jamo,
//	                         which would never meet a composed syllable stored
//	                         from a provider.
//	apostrophes, U+30FB      DELETED, not turned into separators, so "Poseidon's"
//	                         meets "Poseidons" (#2042) and "ハリー・ポッター" meets
//	                         "ハリーポッター" (#1645).
//	& → " and "              So "Foundation & Empire" meets "Foundation and
//	                         Empire". Deliberately NOT done in FoldForTitleMatch:
//	                         that alphabet feeds ContainsPhrase, which needs the
//	                         keywords contiguous, and an injected "and" token
//	                         would break every phrase hit on a release named
//	                         "Foundation.&.Empire".
//
// The result is folded, single-space separated and trimmed. Callers own
// tokenisation policy, as with FoldForTitleMatch — only the character rewriting
// is shared.
func FoldForSearch(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if isASCII(s) {
		return foldASCIIForSearch(s)
	}
	s = norm.NFKC.String(s)
	s = foldCase(s)
	// Marks first, table second. The table keys on the bare letters (æ ø œ ł
	// ß), and a precomposed form like ǣ (U+01E3, ae with macron) only becomes
	// one of them after NFD decomposition drops the macron. Running the table
	// first meant ǣ, ǽ and ǿ never reached it: "Ǣlfric" folded to "ælfric"
	// while the commoner spelling "Ælfric" folded to "aelfric", so the two
	// were mutually unreachable, and FoldForSearch was not idempotent on those
	// six code points. The table's own outputs are ASCII, so it has nothing
	// left to strip afterwards and the order costs nothing.
	s = stripLatinGreekMarks(s)
	s = FoldNonDecomposableLatin(s)
	s = cyrillicYoFolder.Replace(s)
	s = norm.NFC.String(s)
	s = strings.Map(func(r rune) rune {
		if IsApostrophe(r) || r == katakanaMiddleDot {
			return -1
		}
		return r
	}, s)
	s = ampersandExpander.Replace(s)
	mapped := strings.Map(func(r rune) rune {
		// Marks are word characters here, unlike in FoldForTitleMatch. Any mark
		// still standing has already survived stripLatinGreekMarks, so it is one
		// of the meaning-bearing kind, and in several scripts it is not even
		// combining: a Devanagari vowel sign (ा, category Mc) is a SPACING mark,
		// so treating it as a separator silently deleted it and folded "कमला"
		// onto "कमल" — the #1645 collapse arriving through the separator branch
		// instead of the diacritic one. openlibrary.seriesSlug makes the same
		// call for the same reason.
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(mapped), " ")
}

// isASCII reports whether s is entirely 7-bit ASCII.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// foldASCIIForSearch is FoldForSearch for input that is already 7-bit ASCII,
// which most of an English-language catalogue is. Every Unicode step in the
// general path is provably the identity on ASCII — NFKC and NFC leave it alone,
// case folding agrees with ToLower over A-Z, there are no marks to strip, and
// no ASCII character appears in the non-decomposable table — so this shortcut
// changes nothing except the cost, which matters because the general path runs
// on every book and author write and again on every keystroke in the search box.
//
// TestFoldForSearchASCIIFastPathAgrees proves the equivalence over the fixture
// corpus and an exhaustive sweep of ASCII, rather than asking a reader to take
// the paragraph above on trust.
func foldASCIIForSearch(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	space := false
	write := func(r byte) {
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteByte(r)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '`':
			// Apostrophes are deleted, not separated, so "Poseidon's" meets
			// "Poseidons". Deliberately not a separator and not a word char.
		case c == '&':
			// Expands to a word of its own, spaces on both sides.
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("and")
			space = true
		case c >= 'A' && c <= 'Z':
			write(c + ('a' - 'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			write(c)
		default:
			space = true
		}
	}
	return b.String()
}

// foldCasers hands out Unicode case folders one goroutine at a time.
//
// A cases.Caser is stateful, so sharing one across goroutines corrupts its
// buffers — the trap #1374 hit with a shared accent stripper. Building one per
// call is the safe answer and was the first one here, but it is also the
// expensive part of this function: it made FoldForSearch four times the cost of
// FoldForTitleMatch, on a path that runs for every book and author write and
// again for every keystroke in the search box. A pool keeps the safety property
// (a Caser is only ever held by one goroutine) without paying to rebuild the
// tables. Caser.String resets before it transforms, so a pooled instance needs
// no cleanup on the way in or out.
var foldCasers = sync.Pool{New: func() any { c := cases.Fold(); return &c }}

func foldCase(s string) string {
	c := foldCasers.Get().(*cases.Caser)
	defer foldCasers.Put(c)
	return c.String(s)
}

// FoldForSearchRev is the revision of FoldForSearch. db.backfillSearchKeys
// records it after rewriting the search_key columns and skips its table scans
// while the stored value still matches, so a scan runs on the first boot after
// this function's output can change and never otherwise (#2346's pattern).
//
// BUMP THIS whenever a change to FoldForSearch, stripLatinGreekMarks or
// FoldNonDecomposableLatin can produce a different key for the same input.
// Missing a bump leaves existing rows folded by the old rules while queries are
// folded by the new ones — the two-alphabet failure this whole file exists to
// prevent. Bumping when nothing changed costs three table scans once, so when
// in doubt, bump.
const FoldForSearchRev = 1

// IsApostrophe reports whether r is one of the apostrophe forms that appear in
// book titles: ASCII, backtick (a common mojibake for the curly form), both
// curly quotes, and MODIFIER LETTER APOSTROPHE, which is what some providers
// emit for Irish and Hawaiian names.
//
// The set is shared so the folds cannot disagree about what an apostrophe is.
// Note that FoldForTitleMatch still uses the narrower two-form apostropheStripper
// above; widening it changes release matching and is tracked separately (#1660).
func IsApostrophe(r rune) bool {
	switch r {
	case '\'', '`', '‘', '’', 'ʼ':
		return true
	}
	return false
}

// katakanaMiddleDot separates the parts of a transliterated foreign name in
// Japanese ("ハリー・ポッター"). Catalogues are inconsistent about writing it, so
// for search it is deleted rather than treated as a separator: deleting makes
// the two spellings converge, whereas separating leaves them two tokens apart.
const katakanaMiddleDot = '・'

// cyrillicYoFolder folds ё onto е. Applied after case folding, so only the
// lowercase pair needs handling. This is the single deliberate exception to
// "Cyrillic marks are letters": Russian keyboards and Russian publishing both
// treat ё as optional, so a catalogue and a searcher routinely disagree about it.
var cyrillicYoFolder = strings.NewReplacer("ё", "е")

// ampersandExpander spells out "&" so it survives the non-alphanumeric pass as
// a word rather than a separator. Spaces on both sides: "Q&A" must become
// "q and a", not "qanda".
var ampersandExpander = strings.NewReplacer("&", " and ")
