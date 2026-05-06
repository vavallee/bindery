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
