package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
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
// same book", and providers send both spellings for one book.
//
// FoldForTitleMatch must not, and the reason is subtler than it first looks.
// Both the phrase and the haystack fold through it, so expanding on both sides
// would be symmetric and would cost nothing by itself. What breaks is that the
// keyword side additionally drops "and" as a stop word and the haystack side
// does not, while phraseRegex joins its parts with a separator run that no
// letter may interrupt. TestAmpersandPhraseAsymmetry measures that rather than
// arguing it.
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

// TestAmpersandPhraseAsymmetry is the evidence behind the rule that alphabet 1
// must not expand "&", and it is here because the obvious argument for that
// rule is wrong. The phrase and the haystack both fold through
// FoldForTitleMatch, so an expansion would be symmetric; symmetry is not what
// saves the match.
//
// The asymmetry is the stop word list. SigWords drops "and" from the keywords
// and nothing drops it from the haystack, so an expanded haystack gains a word
// the phrase cannot account for, and phraseRegex admits only separators
// between its parts.
//
// The second assertion records a live false negative that is nobody's
// regression: a release spelling the word out is already missed today.
func TestAmpersandPhraseAsymmetry(t *testing.T) {
	kws := newznab.SigWords("Foundation & Empire")
	if len(kws) != 2 || kws[0] != "foundation" || kws[1] != "empire" {
		t.Fatalf(`SigWords("Foundation & Empire") = %q, want ["foundation" "empire"]; "and" is a stop word on this side and that is the whole point`, kws)
	}
	if got := newznab.SigWords("Foundation and Empire"); len(got) != 2 {
		t.Errorf(`SigWords("Foundation and Empire") = %q, want two tokens; both spellings must reduce to the same phrase`, got)
	}

	// What the haystack looks like today, and what it would look like if
	// alphabet 1 expanded the ampersand.
	const today = "foundation empire 1952 retail epub grp"
	const ifExpanded = "foundation and empire 1952 retail epub grp"

	if got := NormalizeRelease("Foundation.&.Empire.1952.RETAIL.EPUB-GRP"); got != today {
		t.Fatalf("NormalizeRelease = %q, want %q", got, today)
	}
	if !ContainsPhrase(today, kws) {
		t.Error("the ampersand release stopped matching its own phrase; alphabet 1 must be leaving & as a separator")
	}
	if ContainsPhrase(ifExpanded, kws) {
		t.Error(`an expanded haystack still matched [foundation empire]; if this ever passes, the stop word asymmetry is gone and alphabet 1 could expand "&" after all`)
	}

	// Live false negative, recorded so it is not mistaken for damage done by
	// the dedup change. A release spelling the word out is missed regardless of
	// which spelling the user asked for, because "and" interrupts the phrase.
	if ContainsPhrase(ifExpanded, newznab.SigWords("Foundation and Empire")) {
		t.Error("a release named \"Foundation and Empire\" now matches; if this passes, the miss recorded here has been fixed and this assertion should become the positive one")
	}
}
