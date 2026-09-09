package textutil

import (
	"reflect"
	"testing"
)

func TestNormalizeAuthorName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"R.R. Haywood", "r r haywood"},
		{"  John   Smith  ", "john smith"},
		{"", ""},
		{"Jean-Luc Picard", "jean luc picard"},
	}
	for _, tc := range cases {
		if got := NormalizeAuthorName(tc.in); got != tc.want {
			t.Fatalf("NormalizeAuthorName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeAuthorNameWithVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string // must be subset-match (all listed strings present)
	}{
		{in: "R.R. Haywood", want: []string{"r r haywood", "rr haywood", "haywood r r"}},
		{in: "Haywood, R.R.", want: []string{"haywood r r", "r r haywood", "rr haywood"}},
		{in: "John Smith Jr.", want: []string{"john smith", "smith john"}},
		{in: "Andy Weir", want: []string{"andy weir", "weir andy"}},
	}
	for _, tc := range cases {
		got := NormalizeAuthorNameWithVariants(tc.in)
		have := make(map[string]bool, len(got))
		for _, v := range got {
			have[v] = true
		}
		for _, want := range tc.want {
			if !have[want] {
				t.Fatalf("variants(%q) = %v, missing %q", tc.in, got, want)
			}
		}
	}
}

func TestNormalizeAuthorNameWithVariants_Idempotent(t *testing.T) {
	a := NormalizeAuthorNameWithVariants("R.R. Haywood")
	b := NormalizeAuthorNameWithVariants("R.R. Haywood")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("variants should be deterministic: %v vs %v", a, b)
	}
}

func TestMatchAuthorName(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		kind AuthorMatchKind
	}{
		{"identical", "R.R. Haywood", "r r haywood", AuthorMatchExact},
		{"compact initials", "R.R. Haywood", "RR Haywood", AuthorMatchExact},
		{"spaced initials", "R.R. Haywood", "R R Haywood", AuthorMatchExact},
		{"suffix jr", "John Smith Jr.", "John Smith", AuthorMatchExact},
		{"suffix iii", "Henry VIII III", "Henry VIII", AuthorMatchExact},
		{"last first swap", "Haywood, R.R.", "R.R. Haywood", AuthorMatchExact},
		{"last first comma", "Weir, Andy", "Andy Weir", AuthorMatchExact},
		{"fuzzy auto", "Brandon Sanderson", "Brandon Sandersen", AuthorMatchFuzzyAuto},
		{"fuzzy ambiguous", "Alice Jones", "Alice James", AuthorMatchFuzzyAmbiguous},
		{"none", "Jane Doe", "Neal Stephenson", AuthorMatchNone},
		{"empty", "", "Jane Doe", AuthorMatchNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchAuthorName(tc.a, tc.b)
			if got.Kind != tc.kind {
				t.Fatalf("MatchAuthorName(%q,%q) kind=%d score=%.3f, want kind=%d", tc.a, tc.b, got.Kind, got.Score, tc.kind)
			}
			switch tc.kind {
			case AuthorMatchFuzzyAuto:
				if got.Score < AuthorMatchAutoThreshold {
					t.Fatalf("expected score >= %.2f, got %.3f", AuthorMatchAutoThreshold, got.Score)
				}
			case AuthorMatchFuzzyAmbiguous:
				if got.Score < AuthorMatchAmbiguousMinimum || got.Score >= AuthorMatchAutoThreshold {
					t.Fatalf("expected score in [%.2f,%.2f), got %.3f", AuthorMatchAmbiguousMinimum, AuthorMatchAutoThreshold, got.Score)
				}
			}
		})
	}
}

func TestMatchAuthorName_Symmetric(t *testing.T) {
	pairs := [][2]string{
		{"R.R. Haywood", "RR Haywood"},
		{"Brandon Sanderson", "Brandon Sandersen"},
		{"Weir, Andy", "Andy Weir"},
	}
	for _, pair := range pairs {
		fwd := MatchAuthorName(pair[0], pair[1])
		rev := MatchAuthorName(pair[1], pair[0])
		if fwd.Kind != rev.Kind {
			t.Fatalf("asymmetric match for %v: fwd=%d rev=%d", pair, fwd.Kind, rev.Kind)
		}
	}
}

// TestMatchAuthorNameAcrossRomanisations pins the #1647 fix: a name written
// with diacritics and the ASCII spelling a German/Scandinavian library folder
// or provider uses must resolve to the same author. Before the transliterated
// variant chain these scored 0.9347 — inside the ambiguous band, which callers
// turn into either a review-queue entry or a duplicate author row.
func TestMatchAuthorNameAcrossRomanisations(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Jörg Müller", "Joerg Mueller"},
		{"Heinrich Böll", "Heinrich Boell"},
		{"Böll, Heinrich", "Heinrich Boell"},
		{"Günter Graß", "Guenter Grass"},
		{"Jo Nesbø", "Jo Nesbo"},
		{"Łukasz Orbitowski", "Lukasz Orbitowski"},
		// The existing diacritic-stripping chain must keep working alongside it.
		{"Jörg Müller", "Jorg Muller"},
		{"José Saramago", "Jose Saramago"},
	}
	for _, tc := range cases {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind != AuthorMatchExact {
			t.Errorf("MatchAuthorName(%q, %q) = %v (score %.4f), want Exact", tc.a, tc.b, got.Kind, got.Score)
		}
	}
}

// TestMatchAuthorNameStillRejectsDistinctAuthors makes sure the extra variants
// only widen matching where the names really are the same person. Variants are
// compared for equality and fed to Jaro-Winkler, so a careless addition could
// push genuinely different authors over the auto-accept threshold.
func TestMatchAuthorNameStillRejectsDistinctAuthors(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Heinrich Böll", "Heinrich Mann"},
		{"Jo Nesbø", "Jo Walton"},
		{"Jörg Müller", "Jörg Fauser"},
		{"Günter Graß", "Günter Wallraff"},
	}
	for _, tc := range cases {
		if got := MatchAuthorName(tc.a, tc.b); got.Kind == AuthorMatchExact || got.Kind == AuthorMatchFuzzyAuto {
			t.Errorf("MatchAuthorName(%q, %q) = %v (score %.4f), want no auto-match", tc.a, tc.b, got.Kind, got.Score)
		}
	}
}

// TestNormalizeAuthorNameFoldsCompatibilityForms covers the move from NFD to
// NFKD. Providers and scraped catalogues carry full-width Latin and
// typographic ligatures, which NFD leaves standing, so the same author keyed
// two different ways depending on which source the record came from.
func TestNormalizeAuthorNameFoldsCompatibilityForms(t *testing.T) {
	cases := []struct{ full, plain string }{
		{"Ｈａｒｕｋｉ　Ｍｕｒａｋａｍｉ", "Haruki Murakami"},
		{"ﬁona ﬂeming", "fiona fleming"},
		{"Ｊｏｒｇｅ Ｌｕｉｓ Ｂｏｒｇｅｓ", "Jorge Luis Borges"},
	}
	for _, c := range cases {
		got, want := NormalizeAuthorName(c.full), NormalizeAuthorName(c.plain)
		if got != want {
			t.Errorf("NormalizeAuthorName(%q) = %q, NormalizeAuthorName(%q) = %q; compatibility forms must fold onto the plain spelling",
				c.full, got, c.plain, want)
		}
	}

	// The ordinary path is unchanged: this must stay a diacritic-stripping,
	// lower-casing, space-collapsing key.
	if got := NormalizeAuthorName("  Jörg   Müller  "); got != "jorg muller" {
		t.Errorf("NormalizeAuthorName = %q, want %q", got, "jorg muller")
	}
}

// #2452. NormalizeAuthorName is the identity alphabet, used to decide whether
// two records are the same person, so a collision merges two authors. It used
// to drop every non-spacing mark, which is right for an acute on an e and
// wrong for kana: the dakuten and handakuten change the letter.
func TestNormalizeAuthorName_KeepsMarksThatChangeTheLetter(t *testing.T) {
	distinct := []struct {
		a, b string
		why  string
	}{
		{"ズ", "ス", "katakana dakuten"},
		{"がっこう", "かっこう", "hiragana dakuten, and two real words"},
		{"ヴィクトル", "ウィクトル", "the vu kana, which is how Viktor is written"},
		{"パン", "ハン", "handakuten"},
		{"Толстой", "Толстои", "Cyrillic breve on й"},
	}
	for _, d := range distinct {
		if got, want := NormalizeAuthorName(d.a), NormalizeAuthorName(d.b); got == want {
			t.Errorf("NormalizeAuthorName(%q) == NormalizeAuthorName(%q) == %q, so two authors merge (%s)",
				d.a, d.b, got, d.why)
		}
	}
}

// The Latin and Greek half must not change: those marks decorate the letter,
// and dropping them is what lets one author be found under either spelling.
func TestNormalizeAuthorName_StillFoldsLatinAndGreek(t *testing.T) {
	cases := map[string]string{
		"Jörg Müller":       "jorg muller",
		"José Saramago":     "jose saramago",
		"Ｍｕｒａｋａｍｉ":          "murakami",
		"ﬁnnegan":           "finnegan",
		"Ursula K. Le Guin": "ursula k le guin",
	}
	for in, want := range cases {
		if got := NormalizeAuthorName(in); got != want {
			t.Errorf("NormalizeAuthorName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A mark that follows a separator has no letter to belong to. Attaching it
// anyway swallowed the boundary and glued two name tokens into one key, which
// is the opposite of what #2452 is for.
func TestNormalizeAuthorName_FloatingMarkKeepsTheWordBoundary(t *testing.T) {
	cases := map[string]string{
		"Tanaka ゙Suzuki": "tanaka suzuki",
		"abc ́def":       "abc def",
		"Ono ́ Yoko":     "ono yoko",
		"゙abc":           "abc",
	}
	for in, want := range cases {
		if got := NormalizeAuthorName(in); got != want {
			t.Errorf("NormalizeAuthorName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The mark still attaches when it sits on the letter before it, which is the
// whole point of the change: ズ must not key as ス.
func TestNormalizeAuthorName_AttachedMarkStillCounts(t *testing.T) {
	if NormalizeAuthorName("ズ") == NormalizeAuthorName("ス") {
		t.Error("ズ and ス share one identity key again")
	}
	if got, want := NormalizeAuthorName("ハード"), "ハード"; got != want {
		t.Errorf("NormalizeAuthorName(ハード) = %q, want %q", got, want)
	}
}
