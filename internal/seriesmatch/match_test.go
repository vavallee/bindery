package seriesmatch

import "testing"

func TestSamePosition(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "exact", a: "1", b: "1", want: true},
		{name: "trimmed exact", a: " 1 ", b: "\t1\n", want: true},
		{name: "numeric equivalent", a: "1.0", b: "1", want: true},
		{name: "decimal tolerance", a: "1.0009", b: "1", want: true},
		{name: "empty left", a: "", b: "1", want: false},
		{name: "empty right", a: "1", b: "", want: false},
		{name: "same non numeric", a: "prelude", b: "prelude", want: true},
		{name: "different non numeric", a: "prelude", b: "1", want: false},
		{name: "different numeric", a: "1", b: "2", want: false},
		{name: "outside tolerance", a: "1.01", b: "1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SamePosition(tt.a, tt.b); got != tt.want {
				t.Fatalf("SamePosition(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestNormalizeSeriesName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "trims and lowercases", input: "  The Stormlight Archive  ", want: "the stormlight archive"},
		{name: "strips series suffix", input: "The Stormlight Archive Series", want: "the stormlight archive"},
		{name: "strips trilogy suffix", input: "Red Rising Trilogy", want: "red rising"},
		{name: "strips saga suffix", input: "The Expanse Saga", want: "the expanse"},
		{name: "strips chronicles suffix", input: "Narnia Chronicles", want: "narnia"},
		{name: "keeps single word suffix term", input: "Saga", want: "saga"},
		{name: "keeps names without configured suffix", input: "Wayward Children", want: "wayward children"},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSeriesName(tt.input); got != tt.want {
				t.Fatalf("NormalizeSeriesName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "removes articles punctuation and novel noise", input: "The Way-of-Kings: A Novel!", want: "way of kings"},
		{name: "removes book noise", input: "Book 1: Leviathan Wakes", want: "1 leviathan wakes"},
		{name: "collapses whitespace", input: "  Words   of   Radiance  ", want: "words of radiance"},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanTitle(tt.input); got != tt.want {
				t.Fatalf("CleanTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTitleScore(t *testing.T) {
	if got := TitleScore("", "Dune"); got != 0 {
		t.Fatalf("empty title score = %d, want 0", got)
	}
	if got := TitleScore("The Way of Kings: A Novel", "Way of Kings"); got < 95 {
		t.Fatalf("equivalent title score = %d, want at least 95", got)
	}
	if got := TitleScore("The Way of Kings", "Words of Radiance"); got >= 70 {
		t.Fatalf("unrelated title score = %d, want below 70", got)
	}
}

// TestVolumeNumber covers the extractor added for #1682.
func TestVolumeNumber(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  string
		ok    bool
	}{
		{"The Mimosa Confessions Vol. 1", "1", true},
		{"The Mimosa Confessions, Vol. 12", "12", true},
		{"Trapped in a Dating Sim Volume 3", "3", true},
		{"Overlord vol 9", "9", true},
		{"Some Series Book 2", "2", true},
		{"Some Series Part 4", "4", true},
		{"Some Series #7", "7", true},
		{"Half-Step Volume 2.5", "2.5", true},
		// No explicit marker: a bare number in a title is a title, not a
		// volume. Getting this wrong would make every numeric title collide.
		{"Fahrenheit 451", "", false},
		{"Catch 22", "", false},
		{"1984", "", false},
		{"The Hobbit", "", false},
	} {
		got, ok := VolumeNumber(tc.title)
		if ok != tc.ok || got != tc.want {
			t.Errorf("VolumeNumber(%q) = (%q, %v), want (%q, %v)", tc.title, got, ok, tc.want, tc.ok)
		}
	}
}

// TestDifferentVolumes is the #1682 regression guard.
//
// Every pair in the "different" group scores 93-100 on TitleScore, well above
// the >=92 threshold internal/api/series.go uses to decide "this is the same
// book". Without this veto, an entire light novel series collapses onto its
// first volume: volume 1 is created, then every later volume matches it and is
// linked to volume 1's row at its own position instead of creating a row.
func TestDifferentVolumes(t *testing.T) {
	// Real titles from the report. The precondition assertion is the point:
	// if a future TitleScore change drops these below 92 the veto is no longer
	// load-bearing for them, and this test should say so rather than pass
	// vacuously.
	scoringAbove := [][2]string{
		{"The Mimosa Confessions Vol. 1", "The Mimosa Confessions Vol. 2"},
		{"Trapped in a Dating Sim Vol. 1", "Trapped in a Dating Sim Vol. 13"},
		{"Overlord, Vol. 1", "Overlord, Vol. 9"},
	}
	for _, p := range scoringAbove {
		if score := TitleScore(p[0], p[1]); score < 92 {
			t.Errorf("precondition: %q vs %q scores %d — below the threshold, so this pair no longer demonstrates the bug", p[0], p[1], score)
		}
		if !DifferentVolumes(p[0], p[1]) {
			t.Errorf("DifferentVolumes(%q, %q) = false, want true", p[0], p[1])
		}
	}

	// Extraction must survive the marker being spelled differently on each
	// side. This pair happens to score below the threshold anyway, so it is
	// not asserted against it.
	if !DifferentVolumes("Some Series Volume 1", "Some Series Vol. 2") {
		t.Error("DifferentVolumes should see through differing volume-marker spellings")
	}

	same := [][2]string{
		{"The Mimosa Confessions Vol. 2", "The Mimosa Confessions, Vol. 2"},
		{"Overlord Volume 9", "Overlord, Vol. 9"},
		// Only one side carries a number: says nothing, so fall through to the
		// similarity score exactly as before. An omnibus or a re-issue with
		// sloppy metadata must still be able to match.
		{"The Mimosa Confessions", "The Mimosa Confessions Vol. 1"},
		{"The Hobbit", "The Hobbit"},
		{"Fahrenheit 451", "Fahrenheit 451"},
	}
	for _, p := range same {
		if DifferentVolumes(p[0], p[1]) {
			t.Errorf("DifferentVolumes(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// TestTitleScoreWeightsLengthDifference pins the WRatio weighting. Every row
// is a rule about what a containment is worth, not an example, so moving one
// is a change to how confidently two titles are called the same book.
//
// The bug it replaces: TitleScore took a flat maximum of four ratios, and
// partialRatio returns 100 whenever the shorter title appears verbatim in the
// longer one. So "Dune" scored a perfect 100 against "Dune Messiah".
func TestTitleScoreWeightsLengthDifference(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		rule string
	}{
		{"Dune", "Dune", 100, "identical"},
		{"The Hobbit", "Hobbit, The", 100, "article inversion is the same title"},
		{"The Lord of the Rings", "Lord of the Rings, The", 100, "same"},
		{"Dune", "Dune Messiah", 90, "containment, 3x length: was 100, a different book scoring perfect"},
		{"It", "It: A Novel by Stephen King, Complete and Unabridged", 60, "containment, extreme length gap gets the harsher discount"},
		{"The Way of Kings", "Kings", 90, "sharing one word is not a match"},
		{"The Way of Kings", "Words of Radiance", 48, "unrelated"},
		{"Mistborn: The Final Empire", "Mistborn: The Well of Ascension", 57, "same series, different books"},
	}
	for _, c := range cases {
		if got := TitleScore(c.a, c.b); got != c.want {
			t.Errorf("TitleScore(%q, %q) = %d, want %d (%s)", c.a, c.b, got, c.want, c.rule)
		}
	}
}

// TestTitleScoreQualifierFloor pins the other half: a title differing only by
// a cataloguing note is the same book, and the length weighting alone would
// punish it exactly as hard as it punishes "Dune" against "Dune Messiah".
//
// The floor is 97 rather than 100 because beets down-weights a parenthetical
// instead of deleting it: "(Abridged)" really can be a different product, so
// an exact title still has to win.
func TestTitleScoreQualifierFloor(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		rule string
	}{
		{"The Hobbit (Illustrated Edition)", "The Hobbit", 97, "parenthesised qualifier"},
		{"The Hobbit [Unabridged]", "The Hobbit", 97, "bracketed qualifier"},
		{"The Hobbit (Illustrated Edition) [Unabridged]", "The Hobbit", 97, "both, stripped repeatedly"},
		{"The Eye of the World [Dramatized Adaptation]", "The Eye of the World", 97, "the ABS shape"},
		{"The Way of Kings", "The Way of Kings: The Stormlight Archive, Book One", 97, "series position appended by Calibre or ABS scanning"},
		{"Mistborn: The Final Empire", "The Final Empire", 90, "a subtitle with no position marker is part of the title and is NOT stripped"},
	}
	for _, c := range cases {
		if got := TitleScore(c.a, c.b); got != c.want {
			t.Errorf("TitleScore(%q, %q) = %d, want %d (%s)", c.a, c.b, got, c.want, c.rule)
		}
	}
}

// TestQualifierFloorNeverMergesVolumes is the guard that makes the strip safe.
// "Overlord, Vol. 1" and "Overlord, Vol. 13" both reduce to "overlord" once a
// series-position suffix is removed, and handing them a near-perfect score is
// the #2343 collapse this package already fights: owning Vol. 13 showed
// catalog Vol. 1 as present, carrying Vol. 13's title.
func TestQualifierFloorNeverMergesVolumes(t *testing.T) {
	for _, c := range [][2]string{
		{"Overlord, Vol. 1", "Overlord, Vol. 13"},
		{"Overlord, Vol. 1", "Overlord, Vol. 9"},
		{"Overlord, Book 2", "Overlord, Book 12"},
	} {
		if got := qualifierOnlyScore(c[0], c[1]); got != 0 {
			t.Errorf("qualifierOnlyScore(%q, %q) = %d, want 0; stripping must never merge two volumes", c[0], c[1], got)
		}
	}
	// And it does fire when the positions agree, which is the whole point.
	if got := qualifierOnlyScore("Overlord, Vol. 1", "Overlord, Vol. 1 [Unabridged]"); got == 0 {
		t.Error("qualifierOnlyScore refused a pair at the same position; the volume guard is too wide")
	}
}
