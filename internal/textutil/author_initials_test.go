package textutil

import "testing"

// TestMatchAuthorNameMiddleInitials is #2881. It pins both directions at once:
// names whose initials disagree at the same position must not merge on their
// own, and the many ways one person's initials get written must keep matching.
//
// "Same person" rows assert the band callers rely on today. The other rows
// assert that nothing claims the pair automatically; most of them land in the
// ambiguous band, where a person can still confirm them.
func TestMatchAuthorNameMiddleInitials(t *testing.T) {
	auto := func(k AuthorMatchKind) bool { return k == AuthorMatchExact || k == AuthorMatchFuzzyAuto }
	cases := []struct {
		name string
		a, b string
		want AuthorMatchKind
	}{
		// Initials that disagree at the same position: two people until
		// someone says otherwise.
		{"middle initial differs", "J. R. Smith", "J. T. Smith", AuthorMatchFuzzyAmbiguous},
		{"middle initial differs, issue example", "A. B. Smith", "A. C. Smith", AuthorMatchFuzzyAmbiguous},
		{"middle initial differs after a forename", "Philip K. Dick", "Philip J. Dick", AuthorMatchFuzzyAmbiguous},
		{"middle initial differs, long surname", "Robert B. Parker", "Robert A. Parker", AuthorMatchFuzzyAmbiguous},
		{"middle initial differs, run together", "JR Smith", "J. T. Smith", AuthorMatchFuzzyAmbiguous},
		{"first initial differs", "J. R. Smith", "K. R. Smith", AuthorMatchNone},
		{"middle initial differs after an abbreviated forename", "J. R. Smith", "John T. Smith", AuthorMatchNone},

		// A missing initial against a present one, with nothing but initials
		// on either side: weaker than a match, so not claimed automatically.
		{"missing initial", "A. Smith", "A. B. Smith", AuthorMatchFuzzyAmbiguous},
		{"missing initial, reversed", "J. R. Smith", "J. Smith", AuthorMatchFuzzyAmbiguous},

		// One person, written the ways catalogues and providers write him.
		{"Tolkien dotted vs spaced", "J.R.R. Tolkien", "J. R. R. Tolkien", AuthorMatchExact},
		{"Tolkien run together", "JRR Tolkien", "J.R.R. Tolkien", AuthorMatchExact},
		{"Tolkien spaced vs run together", "J. R. R. Tolkien", "JRR Tolkien", AuthorMatchExact},
		{"Tolkien undotted", "J R R Tolkien", "J.R.R. Tolkien", AuthorMatchExact},
		{"Tolkien sort name", "Tolkien, J. R. R.", "J.R.R. Tolkien", AuthorMatchExact},
		{"Tolkien sort name run together", "Tolkien, JRR", "J. R. R. Tolkien", AuthorMatchExact},
		{"Tolkien all capitals", "JRR TOLKIEN", "J.R.R. Tolkien", AuthorMatchExact},
		{"Tolkien all lower case", "jrr tolkien", "J. R. R. Tolkien", AuthorMatchExact},
		{"Tolkien spelled out", "J.R.R. Tolkien", "John Ronald Reuel Tolkien", AuthorMatchFuzzyAuto},
		{"Tolkien run together vs spelled out", "JRR Tolkien", "John Ronald Reuel Tolkien", AuthorMatchFuzzyAuto},
		{"Jemisin", "N.K. Jemisin", "NK Jemisin", AuthorMatchExact},
		{"Jemisin spelled out forename", "Nora K. Jemisin", "N. K. Jemisin", AuthorMatchFuzzyAuto},
		{"Klune", "TJ Klune", "T. J. Klune", AuthorMatchExact},
		{"Hackwith", "A.J. Hackwith", "AJ Hackwith", AuthorMatchExact},
		{"Martin", "George R. R. Martin", "George R.R. Martin", AuthorMatchExact},
		{"Lewis spelled out", "C.S. Lewis", "Clive Staples Lewis", AuthorMatchFuzzyAuto},
		{"Rowling spelled out", "J. K. Rowling", "Joanne Rowling", AuthorMatchFuzzyAuto},
		{"Le Guin without the initial", "Ursula K. Le Guin", "Ursula Le Guin", AuthorMatchFuzzyAuto},
		{"Maas without the initial", "Sarah J. Maas", "Sarah Maas", AuthorMatchFuzzyAuto},
		{"Haywood", "R.R. Haywood", "RR Haywood", AuthorMatchExact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, pair := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
				got := MatchAuthorName(pair[0], pair[1])
				if got.Kind != tc.want {
					t.Errorf("MatchAuthorName(%q, %q) = %s (weight %.1f), want %s",
						pair[0], pair[1], kindNames[got.Kind], got.Weight, kindNames[tc.want])
				}
				if !auto(tc.want) && auto(got.Kind) {
					t.Errorf("MatchAuthorName(%q, %q) auto matched a pair that must not merge on its own", pair[0], pair[1])
				}
			}
		})
	}
}

// TestMatchAuthorNameShortWordIsNotInitials is the related risk #2881 noted:
// any short forename was split into letters, so "Ann" was the same name as
// "A. N. N." and "Amy Tan" as "A. M. Y. Tan", exactly enough to bind an alias.
// When the name is written in mixed case, only a word written in capitals is
// read as initials.
func TestMatchAuthorNameShortWordIsNotInitials(t *testing.T) {
	for _, tc := range [][2]string{
		{"Ann Leckie", "A. N. N. Leckie"},
		{"Amy Tan", "A. M. Y. Tan"},
		{"Ed McBain", "E. D. McBain"},
	} {
		if got := MatchAuthorName(tc[0], tc[1]); got.Kind == AuthorMatchExact {
			t.Errorf("MatchAuthorName(%q, %q) = Exact, a forename is not a run of initials", tc[0], tc[1])
		}
		if LatinAliasBinds(tc[0], tc[1]) {
			t.Errorf("LatinAliasBinds(%q, %q) = true, want false", tc[0], tc[1])
		}
	}
}
