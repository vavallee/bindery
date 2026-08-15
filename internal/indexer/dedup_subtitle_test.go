package indexer

import "testing"

// TestCanonicalDedupKey_NeverTruncates is the characterisation test for #2042.
// It fails loudly if anyone reintroduces subtitle truncation into the canonical
// key.
//
// The old key dropped everything after the first ": ". That single decision
// produced BOTH reported failure modes at once:
//
//   - a false positive — "Star Wars: A New Hope" and "Star Wars: The Empire
//     Strikes Back" both became "star wars", so two different books shared one
//     identity and an import bound one onto the other;
//   - a false negative — "Journey of the Pharaohs: Numa Files #17" became
//     "journey of the pharaohs" while the colon-less spelling of the same book
//     kept every word, so the two could never meet and a duplicate `wanted`
//     row landed beside an `imported` one.
//
// If you are here because this test failed, you have made the key lossy again.
// Subtitle-only divergence is handled by CompareTitles, which can return
// TitlesNeedCorroboration; the key itself must stay an identity.
func TestCanonicalDedupKey_NeverTruncates(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Star Wars: A New Hope", "star wars a new hope"},
		{"Star Wars: The Empire Strikes Back", "star wars the empire strikes back"},
		{"Journey of the Pharaohs: Numa Files #17", "journey of the pharaohs numa files 17"},
		{"The Eye of the World: Book One of The Wheel of Time", "the eye of the world book one of the wheel of time"},
		{"Mistborn: The Final Empire", "mistborn the final empire"},
		// The tail survives even when a bracketed qualifier follows it.
		{"Pandora's Star: A Novel [Unabridged]", "pandoras star a novel"},
	}
	for _, tc := range cases {
		if got := CanonicalDedupKey(tc.in); got != tc.want {
			t.Errorf("CanonicalDedupKey(%q) = %q, want %q\n"+
				"the canonical key must not truncate at \": \" — see #2042", tc.in, got, tc.want)
		}
	}
}

// TestCanonicalDedupKey_FoldsPunctuationDivergence is the false-negative half
// of #2042, at the level of the key. Both shapes observed in production must
// now produce one key in both directions.
func TestCanonicalDedupKey_FoldsPunctuationDivergence(t *testing.T) {
	pairs := [][2]string{
		{"Poseidon's Arrow", "Poseidons Arrow"},
		{"Poseidon’s Arrow", "Poseidons Arrow"},
		{"Poseidon’s Arrow", "Poseidon's Arrow"},
		{"Journey of the Pharaohs: Numa Files #17", "Journey of the Pharaohs Numa Files #17"},
		{"Journey of the Pharaohs: Numa Files #17", "Journey of the Pharaohs — Numa Files 17"},
	}
	for _, p := range pairs {
		a, b := CanonicalDedupKey(p[0]), CanonicalDedupKey(p[1])
		if a != b {
			t.Errorf("punctuation divergence not folded:\n  %q → %q\n  %q → %q", p[0], a, p[1], b)
		}
		// Symmetry: the fold cannot depend on argument order.
		if CanonicalDedupKey(p[1]) != b {
			t.Errorf("CanonicalDedupKey is not deterministic for %q", p[1])
		}
	}
}

// TestCanonicalDedupKey_ApostrophesAreDeletedNotSpaced pins the one detail of
// the punctuation policy that is easy to get backwards. An apostrophe is
// intra-word: replacing it with a space yields "poseidon s arrow", which
// matches neither spelling and would silently make the bug worse.
func TestCanonicalDedupKey_ApostrophesAreDeletedNotSpaced(t *testing.T) {
	for _, in := range []string{"Poseidon's Arrow", "Poseidon’s Arrow", "Poseidon`s Arrow", "Poseidonʼs Arrow"} {
		if got, want := CanonicalDedupKey(in), "poseidons arrow"; got != want {
			t.Errorf("CanonicalDedupKey(%q) = %q, want %q (apostrophes are deleted, never spaced)", in, got, want)
		}
	}
}

// TestCompareTitles is the decision table. Every case is asserted in both
// directions, because an asymmetric verdict would make dedup depend on import
// order — the exact class of bug #940 and #2042 are both instances of.
func TestCompareTitles(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want TitleVerdict
	}{
		{
			name: "apostrophe divergence is one work",
			a:    "Poseidon's Arrow",
			b:    "Poseidons Arrow",
			want: TitlesSame,
		},
		{
			name: "smart apostrophe divergence is one work",
			a:    "Poseidon’s Arrow",
			b:    "Poseidons Arrow",
			want: TitlesSame,
		},
		{
			name: "colon vs no colon is one work",
			a:    "Journey of the Pharaohs: Numa Files #17",
			b:    "Journey of the Pharaohs Numa Files #17",
			want: TitlesSame,
		},
		{
			name: "identical titles are one work",
			a:    "Blue Moon: A Jack Reacher Novel",
			b:    "Blue Moon: A Jack Reacher Novel",
			want: TitlesSame,
		},
		{
			// The #2042 false positive. The old key merged these.
			name: "two named instalments of one series are different",
			a:    "Star Wars: A New Hope",
			b:    "Star Wars: The Empire Strikes Back",
			want: TitlesDifferent,
		},
		{
			name: "different main titles are different",
			a:    "Cross Fire",
			b:    "Cross Justice",
			want: TitlesDifferent,
		},
		{
			name: "one-sided publisher subtitle needs corroboration",
			a:    "The Eye of the World",
			b:    "The Eye of the World: Book One of The Wheel of Time",
			want: TitlesNeedCorroboration,
		},
		{
			name: "one-sided series subtitle needs corroboration",
			a:    "The Midnight Line: A Jack Reacher Novel",
			b:    "The Midnight Line",
			want: TitlesNeedCorroboration,
		},
		{
			name: "bracketed qualifier does not create a subtitle",
			a:    "The Eye of the World [Unabridged]",
			b:    "The Eye of the World",
			want: TitlesSame,
		},
		{
			name: "subtitle plus bracket tag vs bare title",
			a:    "Pandora's Star: A Novel [Unabridged]",
			b:    "Pandora’s Star",
			want: TitlesNeedCorroboration,
		},
		{
			// Without a colon the tail is part of the title, not a
			// subtitle, so there is no evidence they are one work.
			name: "extra words with no colon are a different title",
			a:    "Mistborn",
			b:    "Mistborn The Final Empire",
			want: TitlesDifferent,
		},
		{
			name: "a prefix of a longer main title is different",
			a:    "The Eye of the World",
			b:    "The Eye of the World War: A History",
			want: TitlesDifferent,
		},
		{
			name: "blank titles never match",
			a:    "   ",
			b:    "   ",
			want: TitlesDifferent,
		},
		{
			name: "blank never matches a real title",
			a:    "",
			b:    "Dune",
			want: TitlesDifferent,
		},
		{
			name: "umlaut transliteration still folds",
			a:    "Die Straße: Ein Roman",
			b:    "Die Strasse: Ein Roman",
			want: TitlesSame,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareTitles(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareTitles(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := CompareTitles(tc.b, tc.a); got != tc.want {
				t.Errorf("CompareTitles(%q, %q) = %v, want %v (asymmetric!)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// TestMainTitleKey covers the blocking key used to gather candidates. It is
// deliberately weaker than the canonical key and must never be stored as an
// identity — this test documents exactly how weak it is.
func TestMainTitleKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Star Wars: A New Hope", "star wars"},
		{"Star Wars: The Empire Strikes Back", "star wars"},
		{"Star Wars", "star wars"},
		{"Mistborn: The Final Empire [Unabridged]", "mistborn"},
		{"Poseidon's Arrow", "poseidons arrow"},
		{"foo:bar", "foo bar"},
	}
	for _, tc := range cases {
		if got := MainTitleKey(tc.in); got != tc.want {
			t.Errorf("MainTitleKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The whole point: a shared main-title key is NOT an identity.
	if MainTitleKey("Star Wars: A New Hope") != MainTitleKey("Star Wars: The Empire Strikes Back") {
		t.Fatal("precondition: the two Star Wars titles must share a main-title key")
	}
	if CompareTitles("Star Wars: A New Hope", "Star Wars: The Empire Strikes Back") != TitlesDifferent {
		t.Error("a shared main-title key must not be treated as a match")
	}
}

func TestSplitSubtitle(t *testing.T) {
	cases := []struct{ in, main, sub string }{
		{"Mistborn: The Final Empire", "Mistborn", "The Final Empire"},
		{"Mistborn: The Final Empire [Unabridged]", "Mistborn", "The Final Empire"},
		{"Mistborn", "Mistborn", ""},
		{"foo:bar", "foo:bar", ""},
		{"A: B: C", "A", "B: C"},
		{"  Dune (Unabridged)  ", "Dune", ""},
	}
	for _, tc := range cases {
		main, sub := SplitSubtitle(tc.in)
		if main != tc.main || sub != tc.sub {
			t.Errorf("SplitSubtitle(%q) = (%q, %q), want (%q, %q)", tc.in, main, sub, tc.main, tc.sub)
		}
	}
}

// TestTitleIndex_MirrorsCompareTitles proves the in-memory index reaches the
// same verdicts as the SQL lookup, so the author-refresh path and the importer
// path cannot drift apart.
func TestTitleIndex_MirrorsCompareTitles(t *testing.T) {
	ix := NewTitleIndex[string]()
	ix.Add("Poseidon's Arrow", "poseidon")
	ix.Add("Star Wars: A New Hope", "hope")
	ix.Add("The Eye of the World: Book One of The Wheel of Time", "eotw")
	ix.Add("The Midnight Line", "midnight")
	ix.Add("   ", "blank")

	cases := []struct {
		lookup string
		want   string
		found  bool
	}{
		{"Poseidons Arrow", "poseidon", true},
		{"Poseidon’s Arrow", "poseidon", true},
		{"Star Wars: A New Hope", "hope", true},
		// The #2042 false positive must not resolve through the index either.
		{"Star Wars: The Empire Strikes Back", "", false},
		// Stored row carries the subtitle, lookup drops it.
		{"The Eye of the World", "eotw", true},
		// Stored row drops the subtitle, lookup carries it.
		{"The Midnight Line: A Jack Reacher Novel", "midnight", true},
		{"Cross Justice", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := ix.Lookup(tc.lookup)
		if ok != tc.found || got != tc.want {
			t.Errorf("Lookup(%q) = (%q, %v), want (%q, %v)", tc.lookup, got, ok, tc.want, tc.found)
		}
	}
}

// TestTitleIndex_ExactMatchWins guards the preference rule: a subtitle-divergent
// near-miss must never shadow the row that matches exactly, regardless of
// insertion order.
func TestTitleIndex_ExactMatchWins(t *testing.T) {
	for _, order := range []string{"near-first", "exact-first"} {
		t.Run(order, func(t *testing.T) {
			ix := NewTitleIndex[string]()
			if order == "near-first" {
				ix.Add("Foundation: The Empire", "near")
				ix.Add("Foundation", "exact")
			} else {
				ix.Add("Foundation", "exact")
				ix.Add("Foundation: The Empire", "near")
			}
			if got, ok := ix.Lookup("Foundation"); !ok || got != "exact" {
				t.Errorf("Lookup(Foundation) = (%q, %v), want exact", got, ok)
			}
			if got, ok := ix.Lookup("Foundation: The Empire"); !ok || got != "near" {
				t.Errorf("Lookup(Foundation: The Empire) = (%q, %v), want near", got, ok)
			}
		})
	}
}
