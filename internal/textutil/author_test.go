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
