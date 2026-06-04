package textutil

import (
	"math"
	"testing"
)

func TestJaroWinkler(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		t     string
		want  float64
		delta float64
	}{
		{name: "identical", s: "abcdef", t: "abcdef", want: 1, delta: 0},
		{name: "empty", s: "", t: "abc", want: 0, delta: 0},
		// Classic textbook value: dwayne/duane ≈ 0.84
		{name: "dwayne_duane", s: "dwayne", t: "duane", want: 0.84, delta: 0.02},
		// martha/marhta is the canonical 0.961 Jaro-Winkler example.
		{name: "martha_marhta", s: "martha", t: "marhta", want: 0.961, delta: 0.01},
		// Prefix boost pulls this above plain Jaro.
		{name: "prefix_boost", s: "dixon", t: "dicksonx", want: 0.813, delta: 0.02},
		// Different strings should score well below 0.5.
		{name: "disjoint", s: "abc", t: "xyz", want: 0, delta: 0.1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JaroWinkler(tc.s, tc.t)
			if math.Abs(got-tc.want) > tc.delta {
				t.Fatalf("JaroWinkler(%q,%q) = %.4f, want %.4f ±%.4f", tc.s, tc.t, got, tc.want, tc.delta)
			}
		})
	}
}

func TestJaroWinklerSymmetric(t *testing.T) {
	a, b := "riverside", "riverton"
	if math.Abs(JaroWinkler(a, b)-JaroWinkler(b, a)) > 1e-9 {
		t.Fatalf("JaroWinkler should be symmetric for %q and %q", a, b)
	}
}

// TestJaroWinklerUnicode verifies rune-based (not byte-based) comparison:
// multibyte UTF-8 must score sensibly. Byte-indexing produced meaningless
// scores for CJK and accented Latin.
func TestJaroWinklerUnicode(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		t         string
		want      float64
		delta     float64
		wantAbove float64 // when >0, assert score strictly exceeds this instead of want±delta
	}{
		// Identical multibyte strings must score exactly 1.0.
		{name: "cjk_identical", s: "村上春樹", t: "村上春樹", want: 1, delta: 0},
		{name: "accented_identical", s: "gabriel garcía márquez", t: "gabriel garcía márquez", want: 1, delta: 0},
		// Near-identical accented names must score high (single accent change).
		{name: "accented_near", s: "garcía", t: "garcia", wantAbove: 0.9},
		// Near-identical CJK (one differing trailing character) must score high.
		{name: "cjk_near", s: "村上春樹", t: "村上春木", wantAbove: 0.8},
		// A multibyte string with a single accent swap scores high, not 0.
		{name: "accented_swap", s: "café", t: "cafë", wantAbove: 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JaroWinkler(tc.s, tc.t)
			if tc.wantAbove > 0 {
				if got <= tc.wantAbove {
					t.Fatalf("JaroWinkler(%q,%q) = %.4f, want > %.4f", tc.s, tc.t, got, tc.wantAbove)
				}
				return
			}
			if math.Abs(got-tc.want) > tc.delta {
				t.Fatalf("JaroWinkler(%q,%q) = %.4f, want %.4f ±%.4f", tc.s, tc.t, got, tc.want, tc.delta)
			}
		})
	}
}
