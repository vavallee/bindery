package dnb

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestSlugKeepsExistingASCIIIdentifiersStable is the compatibility half. slug's
// output is stored as a ForeignID, so any change to what it returns for a name
// it already handled would orphan the authors created under the old value.
func TestSlugKeepsExistingASCIIIdentifiersStable(t *testing.T) {
	cases := map[string]string{
		"Müller, Thomas": "muller-thomas",
		"Nesbø, Jo":      "nesb-jo",
		"Łukasz":         "ukasz",
		"Zola, Émile":    "zola-emile",
		"O'Brien, Tim":   "o-brien-tim",
		"  Spaced  Out ": "spaced-out",
	}
	for name, want := range cases {
		if got := slug(name); got != want {
			t.Errorf("slug(%q) = %q, want %q — this value is a stored ForeignID and must not move", name, got, want)
		}
	}
}

// TestSlugGivesNonLatinNamesDistinctIdentifiers is the bug. Every name written
// in a non-Latin script reduced to "", so all of them shared the single
// ForeignID "dnb:author:" — one row for every Chinese, Japanese, Cyrillic,
// Greek and Hebrew author the DNB provider ever created.
func TestSlugGivesNonLatinNamesDistinctIdentifiers(t *testing.T) {
	names := []string{
		"刘慈欣",
		"村上春樹",
		"Достоевский, Фёдор",
		"Толстой, Лев",
		"Νίκος",
		"עגנון",
	}
	seen := map[string]string{}
	for _, n := range names {
		got := slug(n)
		if got == "" {
			t.Errorf("slug(%q) = \"\": every author in this script would share one ForeignID", n)
			continue
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("slug(%q) and slug(%q) both = %q: distinct people must not collide", prev, n, got)
		}
		seen[got] = n
	}
}

// TestSlugIsStableAcrossUnicodeForms guards the property the rest of the
// codebase's folds are held to in internal/normdrift. A ForeignID that depends
// on whether the provider sent composed or decomposed text mints a second
// author for the same person.
func TestSlugIsStableAcrossUnicodeForms(t *testing.T) {
	for _, n := range []string{"Müller, Thomas", "Zola, Émile", "Достоевский, Фёдор", "Dvořák, Antonín"} {
		nfc, nfd := norm.NFC.String(n), norm.NFD.String(n)
		if nfc == nfd {
			continue
		}
		if a, b := slug(nfc), slug(nfd); a != b {
			t.Errorf("slug(%q): composed = %q, decomposed = %q", n, a, b)
		}
	}
}
