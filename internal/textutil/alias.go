package textutil

import "unicode"

// LatinAliasBinds reports whether an alias is strong enough, on the strength of
// the two names alone, to stand in for a canonical author name.
//
// It is true on either of two independent grounds:
//
//  1. ROMANISATION. The canonical name is written in a script the alias is not:
//     the canonical name contains at least one letter outside unicode.Latin,
//     and the alias contains letters, all of them Latin. "村上春樹" + "Haruki
//     Murakami" binds this way.
//
//  2. SAME NAME. MatchAuthorName judges the two names to be the same name
//     spelled differently. "Jo Nesbø" + "Jo Nesbo" binds this way.
//
// Anything else does not bind. "Jo Nesbø" + "Karin Fossum" is two Latin names
// (so ground 1 is out) that are not the same name (so ground 2 is out).
//
// # Why an alias needs a ground at all
//
// An alias table mixes rows that assert an identity (an author merge, a
// provider record) with rows that are merely a second spelling nobody
// attributed. For the unattributed rows the names themselves are the only
// evidence available, and two unrelated Latin names carry none: "Karin Fossum"
// sitting beside "Jo Nesbø" is as likely to be a co-author credit or a
// different real person as it is to be the same human.
//
// # Why ground 2 exists
//
// Ground 1 alone is not enough, and a script test can never be. The release
// alphabet transliterates German umlauts and nothing else, so "Jo Nesbø"
// tokenises to ["nesbø"] and never meets a release named
// "Jo.Nesbo.-.The.Snowman.2007.RETAIL.EPUB-GRP". For a Scandinavian, Polish,
// Icelandic or Turkish author the ASCII-transliterated alias is the ONLY route
// from a release name to the author, and both names are Latin, so only ground 2
// can carry it. Dropping that route costs missed grabs for every such author.
//
// # Why ground 2 is AuthorMatchExact and not the fuzzy-auto band
//
// Measured, not assumed. Every accented-Latin transliteration pair tried lands
// on AuthorMatchExact with score 1.0000, because NormalizeAuthorNameWithVariants
// already emits the ASCII-transliterated variant: Nesbø/Nesbo, Östergaard/
// Ostergaard, Östergaard/Oestergaard, Łukasz/Lukasz, Halldór/Halldor. The fuzzy
// band therefore buys the transliteration case nothing, and it costs something
// real: it admits "Jo Nesbø"/"Jon Nesbø" at 0.9778 and "Brandon Sanderson"/
// "Brendon Sanderson" at 0.9765, which is exactly the two-different-people shape
// this function exists to refuse. A genuine romanisation pair does land in the
// band ("Fyodor Dostoevsky"/"Fyodor Dostoyevsky", 0.9654), but it needs no help
// here: when the canonical row is Cyrillic, ground 1 already covers it, and when
// the canonical row is itself Latin no site ever bound it, so admitting it would
// widen the rule rather than restore anything.
//
// Note that ground 2 is not restricted to Latin aliases. The ground is "these
// are the same name", which is script-independent; restricting it would only
// re-create in a new place the category error described below. In practice it
// stays narrow: "村上春樹"/"村上 春樹" scores 0.9467, in the fuzzy band, so Exact
// excludes it.
//
// # Full name, not surname
//
// The test is on the whole name on both sides. An earlier copy of this rule in
// the search path tested only the surname, which disagreed with the other copies
// for any author whose given name and surname are in different scripts:
// "村上 Haruki" has a Latin surname but a non-Latin name, so the write path saved
// aliases the search path then ignored (#2419). Surname-only was a convenience
// of the token matcher it sat in, not a decision on the merits.
//
// # Latin, not ASCII
//
// Ground 1 asks unicode.Latin, not 7-bit ASCII. The earlier copies asked ASCII,
// which made every accented Latin name "non-Latin": "Jo Nesbø" and "Bodil
// Östergaard" counted as another script, so any unattributed alias bound to them
// on ground 1. Scandinavian, German, Polish, Turkish, Vietnamese and Czech names
// are Latin script and reach ground 2 like "John Smith" does.
//
// # Letters only
//
// Digits, spaces and punctuation are neither Latin nor non-Latin, so they never
// decide ground 1 on their own: "J.R.R. Tolkien" and "50 Cent" are Latin names.
// A string with no letters at all — "", "1234", "???" — is not a name in any
// script and is never evidence, so it neither qualifies as a Latin alias nor
// makes a canonical name non-Latin. Ground 1 therefore returns false in both
// directions; ground 2 is left to judge such a string on its own terms.
func LatinAliasBinds(canonical, alias string) bool {
	if latinRomanisationBinds(canonical, alias) {
		return true
	}
	// Ground 2: the alias is the same name, spelled differently. Exact only —
	// see the threshold note above.
	return MatchAuthorName(alias, canonical).Kind == AuthorMatchExact
}

// latinRomanisationBinds is ground 1: a Latin alias on a canonical name written
// in another script.
func latinRomanisationBinds(canonical, alias string) bool {
	hasLetter, allLatin := scanLatin(canonical)
	if !hasLetter || allLatin {
		// Canonical is letterless, or already Latin script — a Latin alias
		// carries no script information about identity.
		return false
	}
	hasLetter, allLatin = scanLatin(alias)
	return hasLetter && allLatin
}

// scanLatin reports whether s contains any letter, and whether every letter it
// contains is in unicode.Latin. Non-letters are ignored entirely.
func scanLatin(s string) (hasLetter, allLatin bool) {
	allLatin = true
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		hasLetter = true
		if !unicode.Is(unicode.Latin, r) {
			allLatin = false
			return hasLetter, allLatin
		}
	}
	return hasLetter, allLatin
}
