package seriesmatch

import "testing"

// The expected values here were taken from the GPL-3.0 implementation this
// package replaced (#1988), so they double as a parity record: ratio,
// tokenSortRatio and tokenSetRatio reproduce it exactly, and partialRatio
// reproduces it on every case where the two agree.

func TestRatio(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "dune", 0},
		{"dune", "dune", 100},
		{"dune", "dune messiah", 50},
		{"way of kings", "way of kings", 100},
		{"words of radiance", "way of kings", 48},
		{"leviathan wakes", "leviathan falls", 80},
		{"abaddons gate", "abbadons gate", 92},
	} {
		if got := ratio(tc.a, tc.b); got != tc.want {
			t.Errorf("ratio(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestRatioIsSymmetric guards the property every caller assumes: TitleScore is
// called with local and remote titles in whichever order the caller happens to
// hold them.
func TestRatioIsSymmetric(t *testing.T) {
	pairs := [][2]string{
		{"dune messiah", "children of dune"},
		{"leviathan wakes", "expanse boxed set leviathan wakes calibans war"},
		{"way of kings", "stormlight archive"},
		{"", "anything at all"},
	}
	for _, p := range pairs {
		if forward, reverse := ratio(p[0], p[1]), ratio(p[1], p[0]); forward != reverse {
			t.Errorf("ratio not symmetric for %q/%q: %d vs %d", p[0], p[1], forward, reverse)
		}
		if forward, reverse := partialRatio(p[0], p[1]), partialRatio(p[1], p[0]); forward != reverse {
			t.Errorf("partialRatio not symmetric for %q/%q: %d vs %d", p[0], p[1], forward, reverse)
		}
		if forward, reverse := tokenSetRatio(p[0], p[1]), tokenSetRatio(p[1], p[0]); forward != reverse {
			t.Errorf("tokenSetRatio not symmetric for %q/%q: %d vs %d", p[0], p[1], forward, reverse)
		}
		if forward, reverse := tokenSortRatio(p[0], p[1]), tokenSortRatio(p[1], p[0]); forward != reverse {
			t.Errorf("tokenSortRatio not symmetric for %q/%q: %d vs %d", p[0], p[1], forward, reverse)
		}
	}
}

// TestPartialRatioScoresContainedSubstringPerfectly is the property the
// omnibus and boxed-set matching work rests on: a volume title that appears
// verbatim inside a collection title must score 100, so that the collection is
// recognised as carrying that volume.
func TestPartialRatioScoresContainedSubstringPerfectly(t *testing.T) {
	const boxset = "expanse boxed set leviathan wakes calibans war abaddons gate"
	for _, volume := range []string{"leviathan wakes", "calibans war", "abaddons gate", "expanse"} {
		if got := partialRatio(volume, boxset); got != 100 {
			t.Errorf("partialRatio(%q, %q) = %d, want 100", volume, boxset, got)
		}
		if got := partialRatio(boxset, volume); got != 100 {
			t.Errorf("partialRatio is not order independent for %q", volume)
		}
	}
	// A volume that is NOT in the collection must not score perfectly.
	if got := partialRatio("cibola burn", boxset); got >= 85 {
		t.Errorf("partialRatio(%q, boxset) = %d, want well below the 85 reuse threshold", "cibola burn", got)
	}
}

func TestPartialRatio(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "dune", 0},
		{"dune", "dune", 100},
		{"dune", "children of dune", 100},
		{"way of kings", "way of kings a novel", 100},
		{"words of radiance", "way of kings", 50},
		{"iron gold", "burning god", 67},
	} {
		if got := partialRatio(tc.a, tc.b); got != tc.want {
			t.Errorf("partialRatio(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestTokenSortRatioIgnoresWordOrder is the whole point of the metric.
func TestTokenSortRatioIgnoresWordOrder(t *testing.T) {
	if got := tokenSortRatio("wakes leviathan", "leviathan wakes"); got != 100 {
		t.Errorf("tokenSortRatio over reordered tokens = %d, want 100", got)
	}
	if got := ratio("wakes leviathan", "leviathan wakes"); got >= 100 {
		t.Errorf("precondition: plain ratio should be order sensitive, got %d", got)
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"  ", "dune", 0},
		{"dune messiah", "messiah dune", 100},
		{"way of kings", "kings of way", 100},
	} {
		if got := tokenSortRatio(tc.a, tc.b); got != tc.want {
			t.Errorf("tokenSortRatio(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestTokenSetRatioIgnoresExtraTokens covers the intersection/difference
// behaviour: one title being the other plus extra words scores 100.
func TestTokenSetRatioIgnoresExtraTokens(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "dune", 0},
		{"dune", "", 0},
		{"dune", "children of dune", 100},
		{"leviathan wakes", "leviathan wakes unabridged audiobook edition", 100},
		{"way of kings", "kings way of the", 100},
		// Repeated tokens collapse into the set, so they cannot skew the score.
		{"dune dune dune", "dune", 100},
	} {
		if got := tokenSetRatio(tc.a, tc.b); got != tc.want {
			t.Errorf("tokenSetRatio(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	// Disjoint token sets must not score highly.
	if got := tokenSetRatio("way of kings", "red rising"); got >= 70 {
		t.Errorf("tokenSetRatio over disjoint titles = %d, want below 70", got)
	}
}

// TestSubstitutionWeightedDistance pins the weighting the ratio formula is
// defined over: a substitution costs 2, an insertion or deletion costs 1. With
// unit substitution cost the ratio would no longer mean "share of characters
// that align" and every score in the package would shift.
func TestSubstitutionWeightedDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abd", 2},  // one substitution
		{"abc", "abcd", 1}, // one insertion
		{"abcd", "abc", 1}, // one deletion
		{"kitten", "sitting", 5},
	} {
		if got := substitutionWeightedDistance([]rune(tc.a), []rune(tc.b)); got != tc.want {
			t.Errorf("substitutionWeightedDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestFuzzyHandlesMultibyte guards the rune handling. The replaced
// implementation mixed byte offsets into rune slicing, so a title carrying
// non-ASCII characters could score 0 against a string that plainly contains
// it.
func TestFuzzyHandlesMultibyte(t *testing.T) {
	const short = "awlād ḥāratinā"
	const long = "awlād ḥāratinā arabic أولاد حارتنا"
	if got := partialRatio(short, long); got != 100 {
		t.Errorf("partialRatio over multibyte containment = %d, want 100", got)
	}
	if got := ratio("café", "café"); got != 100 {
		t.Errorf("ratio over identical multibyte strings = %d, want 100", got)
	}
	if got := tokenSetRatio(short, long); got != 100 {
		t.Errorf("tokenSetRatio over multibyte containment = %d, want 100", got)
	}
}
