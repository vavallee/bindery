package textutil

import "testing"

// kindNames makes the failure messages below readable; the Kind constants are
// otherwise printed as bare integers.
var kindNames = map[AuthorMatchKind]string{
	AuthorMatchNone:           "None",
	AuthorMatchExact:          "Exact",
	AuthorMatchFuzzyAuto:      "FuzzyAuto",
	AuthorMatchFuzzyAmbiguous: "FuzzyAmbiguous",
}

// TestMatchAuthorNameShortSurnames is the reason the single Jaro-Winkler gate
// went away. On a short surname the Winkler prefix bonus rewards a shared
// first letter far more than the disagreement after it costs, so the score
// separates nothing: JW("jones","johnson") = 0.8324 and JW("michelle",
// "michael") = 0.9214 are two different people, while "Christopher Ross"
// against "Christopher Rose" reaches 0.9750 on the strength of a forename the
// pair shares — comfortably over the old 0.94 auto-accept, and two different
// people again.
//
// None of these may auto-match. A surname of five runes or fewer has to be
// equal, not similar, before anything merges on it.
func TestMatchAuthorNameShortSurnames(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Jones", "Johnson"},
		{"Michelle", "Michael"},
		{"Christopher Ross", "Christopher Rose"},
		{"Jonathan Kelly", "Jonathan Kelsy"},
		{"Anne Rice", "Anne Rich"},
		{"Alexander Jones", "Alexander James"},
		{"Michelle Smith", "Michael Smith"},
	}
	for _, tc := range cases {
		got := MatchAuthorName(tc.a, tc.b)
		if got.Kind == AuthorMatchExact || got.Kind == AuthorMatchFuzzyAuto {
			t.Errorf("MatchAuthorName(%q, %q) = %s (jw %.4f), want no auto-match",
				tc.a, tc.b, kindNames[got.Kind], got.Score)
		}
	}
}

// TestMatchAuthorNameInitialsCompatible covers the evidence a whole-name score
// cannot see: an abbreviated forename beside the name it abbreviates. The
// strings differ in most of their characters — "j r r tolkien" against "john
// ronald reuel tolkien" scores 0.9040 — but the surnames are equal and every
// initial lines up, which is as much as any catalogue can offer.
func TestMatchAuthorNameInitialsCompatible(t *testing.T) {
	cases := []struct{ a, b string }{
		{"J.R.R. Tolkien", "John Ronald Reuel Tolkien"},
		{"N. Lane", "Nick Lane"},
		{"J. K. Rowling", "Joanne Rowling"},
	}
	for _, tc := range cases {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind != AuthorMatchFuzzyAuto {
			t.Errorf("MatchAuthorName(%q, %q) = %s (jw %.4f), want FuzzyAuto",
				tc.a, tc.b, kindNames[got.Kind], got.Score)
		}
	}
	// An initial that disagrees is not compatible, and an equal surname must
	// not carry the pairing on its own.
	for _, tc := range []struct{ a, b string }{
		{"M. Lane", "Nick Lane"},
		{"J. R. Tolkien", "John Smith Tolkien"},
	} {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind == AuthorMatchExact || got.Kind == AuthorMatchFuzzyAuto {
			t.Errorf("MatchAuthorName(%q, %q) = %s, want no auto-match", tc.a, tc.b, kindNames[got.Kind])
		}
	}
}

// TestMatchAuthorNameOrderSwap pins which reorderings are the same name.
//
// "Stanley Paul" and "Paul Stanley" are equal as bags of words and nothing
// else: either one person filed two ways or two people, and the names alone
// cannot say, so it caps at ambiguous. A comma says which token is the
// surname, and so does an initials group, because an initial is never a
// surname — those stay exact, and the alias, dedupe and DNB-upgrade paths that
// compare a sort name against a display name depend on it.
func TestMatchAuthorNameOrderSwap(t *testing.T) {
	if got := MatchAuthorName("Stanley Paul", "Paul Stanley"); got.Kind != AuthorMatchFuzzyAmbiguous {
		t.Errorf("MatchAuthorName(%q, %q) = %s, want FuzzyAmbiguous",
			"Stanley Paul", "Paul Stanley", kindNames[got.Kind])
	}
	for _, tc := range []struct{ a, b string }{
		{"Haywood, R.R.", "R.R. Haywood"},
		{"Weir, Andy", "Andy Weir"},
		{"Haywood R R", "R.R. Haywood"},
		{"Tolkien, J. R. R.", "J.R.R. Tolkien"},
	} {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind != AuthorMatchExact {
			t.Errorf("MatchAuthorName(%q, %q) = %s, want Exact", tc.a, tc.b, kindNames[got.Kind])
		}
	}
}

// TestMatchAuthorNameRomanisationIsNotAFuzzyMatch keeps a romanisation an
// alias question. Somebody has to assert that 刘慈欣 publishes as Liu Cixin;
// the character sequences say nothing either way, so they must not be scored
// as evidence in either direction. LatinAliasBinds is where that assertion
// gets made — its ground 1 covers exactly this pair.
func TestMatchAuthorNameRomanisationIsNotAFuzzyMatch(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"刘慈欣", "Liu Cixin"},
		{"村上春樹", "Haruki Murakami"},
	} {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind != AuthorMatchNone {
			t.Errorf("MatchAuthorName(%q, %q) = %s, want None", tc.a, tc.b, kindNames[got.Kind])
		}
	}
	// The same name in one script, spaced two ways, is still a name comparison
	// and stays in the fuzzy band — LatinAliasBinds documents that this pair
	// must not reach Exact.
	if got := MatchAuthorName("村上春樹", "村上 春樹"); got.Kind != AuthorMatchFuzzyAmbiguous {
		t.Errorf("MatchAuthorName(%q, %q) = %s, want FuzzyAmbiguous", "村上春樹", "村上 春樹", kindNames[got.Kind])
	}
}

// TestLatinAliasBindsUnaffected guards the dependency internal/textutil/alias.go
// documents: ground 2 of alias binding is MatchAuthorName(...).Kind == Exact,
// so a change to the band a transliteration pair lands in silently stops
// aliases binding.
func TestLatinAliasBindsUnaffected(t *testing.T) {
	for _, tc := range [][2]string{
		{"Jo Nesbø", "Jo Nesbo"},
		{"Bodil Östergaard", "Bodil Ostergaard"},
		{"Łukasz Orbitowski", "Lukasz Orbitowski"},
		{"村上春樹", "Haruki Murakami"},
	} {
		if !LatinAliasBinds(tc[0], tc[1]) {
			t.Errorf("LatinAliasBinds(%q, %q) = false, want true", tc[0], tc[1])
		}
	}
	for _, tc := range [][2]string{
		{"Jo Nesbø", "Karin Fossum"},
		{"Jo Nesbø", "Jon Nesbø"},
		{"Brandon Sanderson", "Brendon Sanderson"},
	} {
		if LatinAliasBinds(tc[0], tc[1]) {
			t.Errorf("LatinAliasBinds(%q, %q) = true, want false", tc[0], tc[1])
		}
	}
}
