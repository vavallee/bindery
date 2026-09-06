package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/textutil"
)

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
			name: "smart curly apostrophe deleted",
			in:   "Ender’s Game",
			want: "enders game",
		},
		{
			name: "ascii apostrophe deleted",
			in:   "Ender's Game",
			want: "enders game",
		},
		{
			name: "em-dash becomes a separator",
			in:   "Title — Subtitle",
			want: "title subtitle",
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
			// #2042: the key no longer truncates. Truncating discarded
			// exactly the words that tell two instalments apart.
			name: "post-colon subtitle retained",
			in:   "Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2",
			want: "carls doomsday scenario dungeon crawler carl book 2",
		},
		{
			name: "title without colon unchanged",
			in:   "Carl's Doomsday Scenario",
			want: "carls doomsday scenario",
		},
		{
			name: "compact colon is a separator too",
			in:   "foo:bar",
			want: "foo bar",
		},
		{
			name: "hash and colon fold to the same spacing",
			in:   "Journey of the Pharaohs: Numa Files #17",
			want: "journey of the pharaohs numa files 17",
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
		{"Mistborn: The Final Empire", "mistborn the final empire"},
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
		{"The Eye of the World [Unabridged]", "the eye of the world"},
		{"Dune (Unabridged)", "Dune [Audiobook]"},
		{"Die Straße: Ein Roman", "Die Strasse: Ein Roman"},
		{"Poseidon's Arrow", "Poseidons Arrow"},
		{"Journey of the Pharaohs: Numa Files #17", "Journey of the Pharaohs Numa Files #17"},
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
		{"Carl's Doomsday Scenario", "Carl’s Doomsday Scenario"},
	}
	for _, pair := range pairs {
		k1 := NormalizeTitleForDedup(pair[0])
		k2 := NormalizeTitleForDedup(pair[1])
		if k1 != k2 {
			t.Errorf("asymmetric dedup key:\n  %q → %q\n  %q → %q", pair[0], k1, pair[1], k2)
		}
	}
}

// TestNormalizeTitleForDedupExpandsAmpersand pins the ampersand as a spelling
// of "and" rather than as punctuation. Providers disagree about which form to
// send for the same book, and as a separator the two spellings produced
// "foundation empire" and "foundation and empire", which could never meet.
func TestNormalizeTitleForDedupExpandsAmpersand(t *testing.T) {
	pairs := [][2]string{
		{"Foundation & Empire", "Foundation and Empire"},
		{"Sense & Sensibility", "Sense and Sensibility"},
		{"Q&A", "Q and A"},
		{"Crime & Punishment: A Novel", "Crime and Punishment: A Novel"},
	}
	for _, p := range pairs {
		got, want := NormalizeTitleForDedup(p[0]), NormalizeTitleForDedup(p[1])
		if got != want {
			t.Errorf("NormalizeTitleForDedup(%q) = %q, NormalizeTitleForDedup(%q) = %q; the two spellings must produce one key",
				p[0], got, p[1], want)
		}
	}

	// The expansion must not weld its neighbours together, which is what a bare
	// deletion would have done: "Q&A" has to become three tokens, not "qa".
	if got := NormalizeTitleForDedup("Q&A"); got != "q and a" {
		t.Errorf("NormalizeTitleForDedup(%q) = %q, want %q", "Q&A", got, "q and a")
	}

	// And a title with no ampersand is untouched, so the change cannot move a
	// key it has no business moving.
	if got := NormalizeTitleForDedup("The Hobbit"); got != "the hobbit" {
		t.Errorf("NormalizeTitleForDedup(%q) = %q, want %q", "The Hobbit", got, "the hobbit")
	}
}

// TestFoldPunctuationUsesTheSharedApostropheSet guards the delegation to
// textutil.IsApostrophe. The two sets were identical when this file stopped
// keeping its own copy, and the point of the delegation is that they cannot
// drift apart again.
func TestFoldPunctuationUsesTheSharedApostropheSet(t *testing.T) {
	for _, spelling := range []string{
		"Poseidon's Arrow", // ASCII
		"Poseidon’s Arrow", // right single quotation mark
		"Poseidon‘s Arrow", // left single quotation mark
		"Poseidon`s Arrow", // backtick
		"Poseidonʼs Arrow", // modifier letter apostrophe
	} {
		if got := NormalizeTitleForDedup(spelling); got != "poseidons arrow" {
			t.Errorf("NormalizeTitleForDedup(%q) = %q, want %q", spelling, got, "poseidons arrow")
		}
	}
}

// TestDedupAndTitleMatchAlphabetsDifferOnAmpersand pins a deliberate
// disagreement between two alphabets that otherwise look interchangeable, so
// that anyone setting out to "fix" the inconsistency finds the reason first.
//
// CanonicalDedupKey expands "&" to " and " because it answers "are these the
// same book", and providers send both spellings for one book. FoldForTitleMatch
// leaves it a separator because it feeds ContainsPhrase, which requires the
// keywords contiguous: an injected "and" token would break every phrase hit on
// a release named "Foundation.&.Empire".
//
// The two are safe only as long as no caller compares a key from one against a
// key from the other. Nothing does today.
func TestDedupAndTitleMatchAlphabetsDifferOnAmpersand(t *testing.T) {
	const title = "Foundation & Empire"

	dedup := NormalizeTitleForDedup(title)
	if dedup != "foundation and empire" {
		t.Errorf("NormalizeTitleForDedup(%q) = %q, want %q — the dedup alphabet must expand the ampersand so both provider spellings share a key",
			title, dedup, "foundation and empire")
	}

	match := textutil.FoldForTitleMatch(title)
	if match != "foundation empire" {
		t.Errorf("FoldForTitleMatch(%q) = %q, want %q — the match alphabet must leave the ampersand a separator, or ContainsPhrase stops finding \"Foundation.&.Empire\"",
			title, match, "foundation empire")
	}

	if dedup == match {
		t.Errorf("the dedup and title-match alphabets agree on %q (%q); they are meant to differ, and a caller comparing across them would now silently pass",
			title, dedup)
	}
}
