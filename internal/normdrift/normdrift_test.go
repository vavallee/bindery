package normdrift

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

// adversarialTitles is the input corpus. Every entry is here because some
// normalizer in this codebase has, at some point, silently destroyed it.
var adversarialTitles = []string{
	// Possessives — #1643, #871.
	"Ender's Game",
	"Ender’s Game",
	"The Hitchhiker's Guide to the Galaxy",
	// Punctuation that a release name drops.
	"Guards! Guards!",
	"Eat That Frog!",
	"Foundation & Empire",
	"Who Goes There?",
	"Star Wars: A New Hope",
	"Nineteen Eighty-Four",
	// German — the one script that accidentally worked before #1642.
	"Die Höhle",
	"Die Hoehle",
	"Der Prozeß",
	"Der Prozess",
	"Fräulein Smillas Gespür für Schnee",
	"Geräusch",
	// Edge-position diacritics: the exact shape #1642 could never match.
	"Åsa Larsson",
	"José Saramago",
	"Jo Nesbø",
	"Łukasz Orbitowski",
	"Ångström",
	// Interior diacritics, which always worked — kept so a fix that breaks
	// them is loud.
	"Perdido Street Station",
	"China Miéville",
	"Stanisław Lem",
	"Dvořák",
	// Non-Latin scripts — #1642, #1645.
	"三体",
	"刘慈欣",
	"村上春樹",
	"Преступление и наказание",
	"Ο Άρχοντας των Δαχτυλιδιών",
	"עם הרוח",
	// Numerals and single tokens.
	"1984",
	"2001",
	"Circle",
	// Bracketed and parenthesised qualifiers.
	"The Eye of the World [Unabridged]",
	"The Hobbit (German Edition)",
}

// adversarialAuthors is the same idea for the author-identity alphabet.
var adversarialAuthors = []string{
	"J. R. R. Tolkien",
	"J.R.R. Tolkien",
	"Tolkien, J.R.R.",
	"George R. R. Martin",
	"George R.R. Martin",
	"Mary-Kate Olsen",
	"Ursula K. Le Guin",
	"John Smith Jr.",
	"Heinrich Böll",
	"Heinrich Boell",
	"Jörg Müller",
	"Joerg Mueller",
	"Björn Andersen",
	"Jo Nesbø",
	"Østergaard, Anne",
	"刘慈欣",
	"村上春樹",
	"Фёдор Достоевский",
	"Plato",
}

// stringFolds is every exported normalizer that maps a string to a comparison
// key. Adding one here is how you opt it into the properties below.
var stringFolds = []struct {
	name string
	fn   func(string) string
}{
	{"textutil.FoldForTitleMatch", textutil.FoldForTitleMatch},
	{"textutil.NormalizeAuthorName", textutil.NormalizeAuthorName},
	{"indexer.NormalizeRelease", indexer.NormalizeRelease},
	{"indexer.NormalizeTitleForDedup", indexer.NormalizeTitleForDedup},
	{"indexer.CanonicalDedupKey", indexer.CanonicalDedupKey},
	{"seriesmatch.NormalizeSeriesName", seriesmatch.NormalizeSeriesName},
	{"newznab.NormalizeQueryTitle", newznab.NormalizeQueryTitle},
	{"newznab.TransliterateQuery", newznab.TransliterateQuery},
}

// TestNormalizersAgreeAcrossUnicodeForm is the single most valuable property
// here. macOS hands filenames back DECOMPOSED while every metadata provider
// returns COMPOSED, so the two spellings of an accented name meet constantly.
// A normalizer that treats them differently produces two keys for one thing,
// and the comparison that uses it silently never matches.
//
// This is what broke the importer (#1646), Calibre author resolution and
// metadata enrichment (#1647), and — via a combining mark hitting the
// separator branch and truncating the word at the accent — the shared title
// fold itself.
func TestNormalizersAgreeAcrossUnicodeForm(t *testing.T) {
	inputs := append(append([]string{}, adversarialTitles...), adversarialAuthors...)
	for _, fold := range stringFolds {
		for _, in := range inputs {
			nfc, nfd := norm.NFC.String(in), norm.NFD.String(in)
			if nfc == nfd {
				continue // no decomposed form; nothing to disagree about
			}
			gotC, gotD := fold.fn(nfc), fold.fn(nfd)
			if gotC != gotD {
				t.Errorf("%s is not Unicode-form invariant for %q:\n  NFC -> %q\n  NFD -> %q",
					fold.name, in, gotC, gotD)
			}
		}
	}
}

// TestNormalizersAreIdempotent catches folds that are not fixed points. Keys
// routinely pass through a second normalizer (a dedup key built from an
// already-normalized release, a series name built from a dedup key), and a
// fold that keeps changing its own output makes those chains order-dependent.
func TestNormalizersAreIdempotent(t *testing.T) {
	inputs := append(append([]string{}, adversarialTitles...), adversarialAuthors...)
	for _, fold := range stringFolds {
		for _, in := range inputs {
			once := fold.fn(in)
			if twice := fold.fn(once); twice != once {
				t.Errorf("%s is not idempotent for %q:\n  once  -> %q\n  twice -> %q",
					fold.name, in, once, twice)
			}
		}
	}
}

// TestEveryKeywordIsFindableInItsOwnHaystack is the property that would have
// caught #1642 AND #1643 on the day each was written, without anyone knowing
// to look for them.
//
// SigWords produces the keywords; NormalizeRelease produces the haystack they
// are matched against; WordBoundaryRegex does the matching. Feeding one title
// through BOTH sides must therefore always match — a keyword that cannot be
// found in the normalization of the very string it came from is, by
// definition, a keyword no release can ever satisfy.
//
// #1642: keywords with a non-ASCII edge rune compiled to a regex matching
// nothing, because RE2's `\b` is ASCII-only while NormalizeRelease preserves
// \p{L}. #1643: the haystack was merely lowercased, so the apostrophe-stripped
// keyword "enders" was absent from "ender's game".
func TestEveryKeywordIsFindableInItsOwnHaystack(t *testing.T) {
	for _, title := range adversarialTitles {
		haystack := indexer.NormalizeRelease(title)
		for _, kw := range newznab.SigWords(title) {
			if !indexer.WordBoundaryRegex(kw).MatchString(haystack) {
				t.Errorf("keyword %q from %q is unfindable in its own haystack %q",
					kw, title, haystack)
			}
		}
	}
}

// TestAuthorTokensAreFindableInTheirOwnRelease is the author-side twin, and is
// the property #1608/#1609 violated: "J.R.R. Tolkien" produced the token
// "j.r.r", which cannot occur in a NormalizeRelease haystack because dots
// collapse to spaces there.
func TestAuthorTokensAreFindableInTheirOwnRelease(t *testing.T) {
	for _, author := range adversarialAuthors {
		haystack := indexer.NormalizeRelease(author)
		for _, tok := range strings.Fields(haystack) {
			if len(tok) < 3 {
				continue // initials are dropped as optional
			}
			if !indexer.WordBoundaryRegex(tok).MatchString(haystack) {
				t.Errorf("author token %q from %q is unfindable in its own release form %q",
					tok, author, haystack)
			}
		}
	}
}

// TestSeriesNameFoldSubsumesDedupKey pins the subset relation ABS depends on:
// series are LOOKED UP by CanonicalDedupKey and PROMOTED by
// NormalizeSeriesName. If promotion is not at least as lenient as lookup, a
// series can match on lookup and then be refused the promotion check that
// exists for it — which is what "The Expanse [Audiobook]" did (#1648).
func TestSeriesNameFoldSubsumesDedupKey(t *testing.T) {
	names := []string{
		"The Expanse", "The Expanse Series", "The Expanse [Audiobook]",
		"The Expanse [Unabridged]", "Discworld", "Discworld Series",
		"Die Höhle", "Die Hoehle", "三体", "Преступление",
	}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if indexer.CanonicalDedupKey(a) != indexer.CanonicalDedupKey(b) {
				continue
			}
			if seriesmatch.NormalizeSeriesName(a) != seriesmatch.NormalizeSeriesName(b) {
				t.Errorf("%q and %q share a dedup key (%q) but not a series key (%q vs %q) — "+
					"they would match on lookup and then be refused promotion",
					a, b, indexer.CanonicalDedupKey(a),
					seriesmatch.NormalizeSeriesName(a), seriesmatch.NormalizeSeriesName(b))
			}
		}
	}
}

// TestAuthorMatchIsReflexiveAndSymmetric guards the tiered matcher's basic
// algebra. A name that does not match itself would make every caller create
// duplicates; asymmetry would make the result depend on argument order, which
// differs between call sites.
func TestAuthorMatchIsReflexiveAndSymmetric(t *testing.T) {
	for _, a := range adversarialAuthors {
		if got := textutil.MatchAuthorName(a, a); got.Kind != textutil.AuthorMatchExact {
			t.Errorf("MatchAuthorName(%q, %q) = %v, want Exact", a, a, got.Kind)
		}
		if got := textutil.MatchAuthorName(a, norm.NFD.String(a)); got.Kind != textutil.AuthorMatchExact {
			t.Errorf("MatchAuthorName(%q, NFD of itself) = %v, want Exact", a, got.Kind)
		}
		for _, b := range adversarialAuthors {
			ab := textutil.MatchAuthorName(a, b)
			ba := textutil.MatchAuthorName(b, a)
			if ab.Kind != ba.Kind {
				t.Errorf("MatchAuthorName is asymmetric for %q / %q: %v vs %v", a, b, ab.Kind, ba.Kind)
			}
		}
	}
}

// TestDiacriticSchemesAreTheDocumentedOnes makes the deliberate divergence
// LOUD. The four alphabets differ on purpose (see internal/textutil/fold.go) —
// the bug was always that the difference was undocumented and unnoticed. If
// you are changing one of these, you are changing what "the same book" or "the
// same person" means, and you should have to say so here.
func TestDiacriticSchemesAreTheDocumentedOnes(t *testing.T) {
	cases := []struct {
		fold string
		fn   func(string) string
		in   string
		want string
	}{
		// Title/release matching EXPANDS German umlauts (the NZB convention).
		{"FoldForTitleMatch", textutil.FoldForTitleMatch, "Höhle", "hoehle"},
		{"NormalizeRelease", indexer.NormalizeRelease, "Höhle", "hoehle"},
		{"CanonicalDedupKey", indexer.CanonicalDedupKey, "Höhle", "hoehle"},
		// ...but leaves other diacritics alone, so scripts stay distinct.
		{"FoldForTitleMatch", textutil.FoldForTitleMatch, "Miéville", "miéville"},
		{"NormalizeRelease", indexer.NormalizeRelease, "Nesbø", "nesbø"},
		// Author identity STRIPS diacritics instead.
		{"NormalizeAuthorName", textutil.NormalizeAuthorName, "Höhle", "hohle"},
		{"NormalizeAuthorName", textutil.NormalizeAuthorName, "Miéville", "mieville"},
		// Author identity also collapses punctuation to token boundaries,
		// which is what makes dotted initials work.
		{"NormalizeAuthorName", textutil.NormalizeAuthorName, "J.R.R. Tolkien", "j r r tolkien"},
	}
	for _, tc := range cases {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("%s(%q) = %q, want %q — if this change is intended, update "+
				"internal/textutil/fold.go and say which comparisons it moves",
				tc.fold, tc.in, got, tc.want)
		}
	}
}

// adversarialLanguageCodes is the corpus for the language alphabet: every
// vocabulary a provider actually hands us. Google Books returns ISO 639-1
// with optional region ("en", "en-US"), EPUBs carry whatever dc:language
// says, DNB emits 639-2 natively, and profiles are written in 639-2/B —
// plus empty (work-level OpenLibrary data) and plain garbage.
var adversarialLanguageCodes = []string{
	"en", "en-US", "EN", "eng", "de", "ger", "deu", "pt-BR", "pt_BR",
	"zh-Hans", "fr", "fre", " Eng ", "", "xx", "not a language",
}

// TestLanguageFilterIsInvariantUnderItsOwnCanonicaliser is the language
// member of the same family: IsLanguageAllowed compares a provider-supplied
// code against a profile's allowed list, and NormalizeLanguageCode is the
// canonicaliser both sides are supposed to be reduced through. #1729 was the
// filter trusting its inputs to already be canonical — a profile allowing
// "eng" silently rejected every book arriving as "en" or "en-US". The
// property: pre-normalizing either side must never change the verdict.
func TestLanguageFilterIsInvariantUnderItsOwnCanonicaliser(t *testing.T) {
	allowedLists := [][]string{
		nil,
		{"eng"},
		{"en"},
		{"eng", "fre"},
		{"de", "ger", "deu"},
		{"por"},
		{"xx"},
	}
	for _, code := range adversarialLanguageCodes {
		for _, allowed := range allowedLists {
			normAllowed := make([]string, len(allowed))
			for i, a := range allowed {
				normAllowed[i] = models.NormalizeLanguageCode(a)
			}
			for _, unknownFail := range []bool{false, true} {
				raw := models.IsLanguageAllowed(code, allowed, unknownFail)
				canon := models.IsLanguageAllowed(models.NormalizeLanguageCode(code), normAllowed, unknownFail)
				if raw != canon {
					t.Errorf("IsLanguageAllowed(%q, %v, unknownFail=%v) = %v, but %v on the canonicalised "+
						"forms (%q, %v) — the filter's verdict depends on which vocabulary the caller used",
						code, allowed, unknownFail, raw, canon,
						models.NormalizeLanguageCode(code), normAllowed)
				}
			}
		}
	}
}

// TestNonLatinScriptsKeepDistinctKeys pins #1645 from the other side: a fold
// used for identity must not collapse different works or people into one key
// just because it cannot romanise them.
func TestNonLatinScriptsKeepDistinctKeys(t *testing.T) {
	groups := [][]string{
		{"三体", "黑暗森林", "死神永生"},
		{"刘慈欣", "村上春樹", "遠藤周作"},
		{"Преступление", "Наказание", "Идиот"},
	}
	for _, fold := range stringFolds {
		for _, group := range groups {
			seen := make(map[string]string, len(group))
			for _, in := range group {
				key := fold.fn(in)
				if key == "" {
					t.Errorf("%s(%q) = \"\" — a non-Latin string reduced to nothing, "+
						"so every string in its script now shares one key", fold.name, in)
					continue
				}
				if prev, dup := seen[key]; dup {
					t.Errorf("%s collapsed %q and %q onto the same key %q", fold.name, prev, in, key)
				}
				seen[key] = in
			}
		}
	}
}
