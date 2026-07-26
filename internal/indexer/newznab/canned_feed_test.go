package newznab

import "testing"

// Regression tests for #1643. titleHasRelevantResult compared SigWords output
// (which strips apostrophes and transliterates umlauts) against a haystack that
// was only lowercased, so the two sides lived in different alphabets. A
// byte-identical result was then misclassified as a "canned feed" (the
// Jackett/AudioBookBay pattern from #699) and thrown away, costing three extra
// indexer queries per search — deterministically, forever, for every affected
// title.
func TestTitleHasRelevantResultAlphabetSymmetry(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		release string
		want    bool
	}{
		// Previously false: SigWords emits "enders", absent from "ender's game".
		{"english possessive", "Ender's Game", "Ender's Game.epub", true},
		{"english possessive 2", "The Handmaid's Tale", "The Handmaid's Tale (Retail).epub", true},
		{"unicode apostrophe", "Ender’s Game", "Ender’s Game.epub", true},
		// Previously false when BOTH sides carried the umlaut, which is the case
		// the transliteration exists for.
		{"umlaut both sides", "Der Prozeß", "Der Prozeß (German Edition).epub", true},
		{"umlaut haystack only", "Der Prozess", "Der Prozeß.epub", true},
		{"umlaut needle only", "Der Prozeß", "Der.Prozess.German.epub", true},
		{"eszett", "Die Häschen", "Die Häschen - Muster.epub", true},
		// Controls that always worked.
		{"plain ascii", "Dune", "Dune.epub", true},
		{"punctuation separated", "Dune Messiah", "Dune.Messiah.Retail.epub", true},

		// Must still REJECT a genuine canned feed that ignores the query.
		{"canned feed rejected", "Ender's Game", "Some Unrelated Audiobook.m4b", false},
		{"partial coverage rejected", "Dune Messiah", "Dune.epub", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := titleHasRelevantResult(tc.query, []SearchResult{{Title: tc.release}})
			if got != tc.want {
				t.Errorf("titleHasRelevantResult(%q, [%q]) = %v, want %v",
					tc.query, tc.release, got, tc.want)
			}
		})
	}
}

// The BookTitle attribute is folded too, since it is concatenated into the same
// haystack and an indexer may carry the apostrophe in one field but not both.
func TestTitleHasRelevantResultFoldsBookTitleAttribute(t *testing.T) {
	res := []SearchResult{{Title: "unrelated release name", BookTitle: "Ender's Game"}}
	if !titleHasRelevantResult("Ender's Game", res) {
		t.Error("BookTitle attribute should be folded into the haystack like Title")
	}
}

func TestFoldForSigWordMatch(t *testing.T) {
	cases := map[string]string{
		"Ender's Game":  "enders game",
		"Ender’s Game":  "enders game",
		"Der Prozeß":    "der prozess",
		"Die HÄSCHEN":   "die haeschen",
		"Über Alles":    "ueber alles",
		"Plain ASCII 1": "plain ascii 1",
	}
	for in, want := range cases {
		if got := foldForSigWordMatch(in); got != want {
			t.Errorf("foldForSigWordMatch(%q) = %q, want %q", in, got, want)
		}
	}
}
