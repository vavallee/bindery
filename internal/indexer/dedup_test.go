package indexer

import "testing"

func TestNormalizeTitleForDedup(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing edition suffix stripped",
			in:   "Die Stille ist ein Geräusch (German Edition)",
			want: "die stille ist ein geraeusch",
		},
		{
			name: "unabridged suffix stripped",
			in:   "Dune (Unabridged)",
			want: "dune",
		},
		{
			name: "smart curly quote folded",
			in:   "Ender’s Game",
			want: "ender's game",
		},
		{
			name: "em-dash folded to hyphen",
			in:   "Title — Subtitle",
			want: "title - subtitle",
		},
		{
			name: "leading and trailing whitespace stripped",
			in:   "  Moby Dick  ",
			want: "moby dick",
		},
		{
			name: "internal whitespace collapsed",
			in:   "Moby   Dick",
			want: "moby dick",
		},
		{
			name: "umlauts transliterated",
			in:   "Öde Wälder",
			want: "oede waelder",
		},
		{
			name: "eszett transliterated",
			in:   "Die Straße",
			want: "die strasse",
		},
		{
			name: "NFD to NFC before normalization",
			// "é" in NFD (e + combining acute U+0301) vs NFC (é U+00E9)
			in:   "élan",
			want: "élan",
		},
		{
			name: "identical titles normalise to same key",
			in:   "Die Stille ist ein Geraeusch",
			want: "die stille ist ein geraeusch",
		},
		{
			name: "post-colon subtitle stripped",
			in:   "Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2",
			want: "carl's doomsday scenario",
		},
		{
			name: "title without colon unchanged",
			in:   "Carl's Doomsday Scenario",
			want: "carl's doomsday scenario",
		},
		{
			name: "colon without trailing whitespace preserved",
			in:   "foo:bar",
			want: "foo:bar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeTitleForDedup(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeTitleForDedup(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCanonicalDedupKey covers the single cross-source key (#940): it must
// strip ABS-style bracketed qualifiers on top of all NormalizeTitleForDedup
// folding, so titles a Calibre ebook and an ABS audiobook present for the same
// work collapse to one key.
func TestCanonicalDedupKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"The Eye of the World [Unabridged]", "the eye of the world"},
		{"The Eye of the World", "the eye of the world"},
		{"Mistborn: The Final Empire", "mistborn"},
		{"Mistborn", "mistborn"},
		{"Dune (Unabridged) [Audiobook]", "dune"},
		{"Die Straße", "die strasse"},
		{"Die Strasse", "die strasse"},
		{"  spaced  out [2021] ", "spaced out"},
	}
	for _, tc := range cases {
		if got := CanonicalDedupKey(tc.in); got != tc.want {
			t.Errorf("CanonicalDedupKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCanonicalDedupKey_Symmetric is the order-independence invariant: any two
// titles for the same work, in either order, produce the same key.
func TestCanonicalDedupKey_Symmetric(t *testing.T) {
	pairs := [][2]string{
		{"Mistborn: The Final Empire", "Mistborn"},
		{"The Eye of the World [Unabridged]", "the eye of the world"},
		{"Dune (Unabridged)", "Dune [Audiobook]"},
		{"Die Straße: Ein Roman", "Die Strasse"},
	}
	for _, p := range pairs {
		if a, b := CanonicalDedupKey(p[0]), CanonicalDedupKey(p[1]); a != b {
			t.Errorf("asymmetric: %q->%q vs %q->%q", p[0], a, p[1], b)
		}
	}
}

func TestNormalizeTitleForDedup_Symmetric(t *testing.T) {
	// Both the raw provider form and the trailing-stripped form must map to
	// the same key — this is the core dedup invariant.
	pairs := [][2]string{
		{"Die Stille ist ein Geräusch (German Edition)", "Die Stille ist ein Geräusch"},
		{"Dune (Unabridged)", "Dune"},
		{"  Moby Dick  ", "Moby Dick"},
		{"Öde Wälder (German Edition)", "Öde Wälder"},
		{"Carl's Doomsday Scenario", "Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2"},
	}
	for _, pair := range pairs {
		k1 := NormalizeTitleForDedup(pair[0])
		k2 := NormalizeTitleForDedup(pair[1])
		if k1 != k2 {
			t.Errorf("asymmetric dedup key:\n  %q → %q\n  %q → %q", pair[0], k1, pair[1], k2)
		}
	}
}
