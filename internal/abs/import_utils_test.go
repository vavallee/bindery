package abs

import (
	"strings"
	"testing"
)

func TestNormalizeLibraryIDsPrependsLegacyPrimary(t *testing.T) {
	t.Parallel()

	got := normalizeLibraryIDs(" lib-legacy ", []string{" lib-books ", "lib-audio", "lib-books", ""})
	if got, want := strings.Join(got, ","), "lib-legacy,lib-books,lib-audio"; got != want {
		t.Fatalf("library ids = %q, want %q", got, want)
	}
}

// TestNormalizeTitle_StripsABSEditionNoise verifies that the ABS title
// normalization collapses the bracket/paren/series/edition noise that ABS
// titles routinely carry, so an item title and the corresponding clean
// metadata-provider work title produce the same dedup key (#762).
func TestNormalizeTitle_StripsABSEditionNoise(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain title unchanged", "Leviathan Wakes", "leviathan wakes"},
		{"trailing series parenthetical", "The Eye of the World (The Wheel of Time, Book 1)", "the eye of the world"},
		{"trailing hash series parenthetical", "Leviathan Wakes (The Expanse #1)", "leviathan wakes"},
		{"square-bracket unabridged tag", "The Eye of the World [Unabridged]", "the eye of the world"},
		{"square-bracket dramatized tag", "The Way of Kings [Dramatized Adaptation]", "the way of kings"},
		{"colon novel subtitle and bracket tag", "Pandora's Star: A Novel [Unabridged]", "pandora's star"},
		{"stacked bracket tags", "Dune [Unabridged] [2021]", "dune"},
		{"paren edition then bracket tag", "Mistborn (Unabridged) [Audiobook]", "mistborn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeTitle(tc.in); got != tc.want {
				t.Fatalf("normalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeTitle_MatchesNoisyABSTitleToCleanProviderTitle is the core
// matching invariant: a noisy ABS item title and the clean provider work
// title for the same book must produce the same key, so lookupUpstreamBook
// and findBookByNormalizedTitle link them instead of queuing for review.
func TestNormalizeTitle_MatchesNoisyABSTitleToCleanProviderTitle(t *testing.T) {
	t.Parallel()

	pairs := [][2]string{
		{"The Eye of the World (The Wheel of Time, Book 1)", "The Eye of the World"},
		{"Pandora's Star: A Novel [Unabridged]", "Pandora's Star"},
		{"Leviathan Wakes (The Expanse #1)", "Leviathan Wakes"},
		{"The Way of Kings [Unabridged]", "The Way of Kings"},
		{"Project Hail Mary [Unabridged]", "Project Hail Mary"},
	}
	for _, pair := range pairs {
		absKey := normalizeTitle(pair[0])
		providerKey := normalizeTitle(pair[1])
		if absKey != providerKey {
			t.Errorf("titles must share a dedup key:\n  ABS      %q -> %q\n  provider %q -> %q",
				pair[0], absKey, pair[1], providerKey)
		}
	}
}

// TestNormalizeTitle_DistinctBooksStayDistinct guards against the bracket
// stripping over-collapsing genuinely different books to the same key, which
// would create false ambiguity and send correct items to review.
func TestNormalizeTitle_DistinctBooksStayDistinct(t *testing.T) {
	t.Parallel()

	distinct := [][2]string{
		{"Leviathan Wakes (The Expanse #1)", "Caliban's War (The Expanse #2)"},
		{"The Eye of the World [Unabridged]", "The Great Hunt [Unabridged]"},
		{"Pandora's Star [Unabridged]", "Judas Unchained [Unabridged]"},
	}
	for _, pair := range distinct {
		if a, b := normalizeTitle(pair[0]), normalizeTitle(pair[1]); a == b {
			t.Errorf("distinct books collapsed to same key %q:\n  %q\n  %q", a, pair[0], pair[1])
		}
	}
}
