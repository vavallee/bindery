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

// TestMatchAuthorNameTitleCaseInitialsStayExact: initials run together and
// written in title case ("Jrr", "Ra", "Tj") are common in folder names and
// loose metadata. They must stay Exact against the dotted form, because alias
// binding, the bulk import skip, the Calibre alias rule and the sort name
// dedupe all accept Exact only.
func TestMatchAuthorNameTitleCaseInitialsStayExact(t *testing.T) {
	for _, tc := range [][2]string{
		{"Jrr Tolkien", "J.R.R. Tolkien"},
		{"Tolkien, Jrr", "J. R. R. Tolkien"},
		{"Ra Salvatore", "R.A. Salvatore"},
		{"Tj Klune", "T. J. Klune"},
		{"Rr Haywood", "R.R. Haywood"},
	} {
		for _, pair := range [][2]string{tc, {tc[1], tc[0]}} {
			if got := MatchAuthorName(pair[0], pair[1]); got.Kind != AuthorMatchExact {
				t.Errorf("MatchAuthorName(%q, %q) = %s, want Exact", pair[0], pair[1], kindNames[got.Kind])
			}
		}
	}
	// Spelled out, the vowelless run still reads as initials.
	if got := MatchAuthorName("Jrr Tolkien", "John Ronald Reuel Tolkien"); got.Kind != AuthorMatchFuzzyAuto {
		t.Errorf("MatchAuthorName(Jrr Tolkien, spelled out) = %s, want FuzzyAuto", kindNames[got.Kind])
	}
	// And it still cannot stand in for a conflicting middle initial.
	if got := MatchAuthorName("Tj Klune", "T. K. Klune"); got.Kind == AuthorMatchExact || got.Kind == AuthorMatchFuzzyAuto {
		t.Errorf("MatchAuthorName(Tj Klune, T. K. Klune) = %s, want no auto match", kindNames[got.Kind])
	}
}

// TestMatchAuthorNameDroppedInitialConfirmedByTitle: #2881 asked that a
// dropped initial stay acceptable when a title backs it. Callers that have
// matched the title use ConfirmedByTitle, which accepts the ambiguous band
// only when the difference is initials left out, never initials that disagree.
func TestMatchAuthorNameDroppedInitialConfirmedByTitle(t *testing.T) {
	for _, tc := range [][2]string{
		{"J. Rowling", "J.K. Rowling"},
		{"A. Smith", "A. B. Smith"},
		{"George Martin", "George R. R. Martin"},
		{"Iain Banks", "Iain M. Banks"},
	} {
		for _, pair := range [][2]string{tc, {tc[1], tc[0]}} {
			got := MatchAuthorName(pair[0], pair[1])
			if got.Kind != AuthorMatchFuzzyAmbiguous || !got.DroppedInitial || !got.ConfirmedByTitle() {
				t.Errorf("MatchAuthorName(%q, %q) = %s dropped=%v, want ambiguous and confirmed by a title",
					pair[0], pair[1], kindNames[got.Kind], got.DroppedInitial)
			}
		}
	}
	for _, tc := range [][2]string{
		{"J. R. Smith", "J. T. Smith"},
		{"Philip K. Dick", "Philip J. Dick"},
		{"Alice Jones", "Alice James"},
		{"Tolkien", "J.R.R. Tolkien"},
		{"Stanley Paul", "Paul Stanley"},
	} {
		for _, pair := range [][2]string{tc, {tc[1], tc[0]}} {
			if got := MatchAuthorName(pair[0], pair[1]); got.ConfirmedByTitle() {
				t.Errorf("MatchAuthorName(%q, %q) = %s dropped=%v, a title must not confirm it",
					pair[0], pair[1], kindNames[got.Kind], got.DroppedInitial)
			}
		}
	}
}
