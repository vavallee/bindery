package openlibrary

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestSeriesSlugUnicodeFormInvariant is the unexported-normalizer half of the
// drift suite in internal/normdrift, which can only reach exported folds. The
// slug is a FOREIGN ID: a slug that depends on Unicode form would create two
// series rows for one series, the inverse of #1645's collapse.
func TestSeriesSlugUnicodeFormInvariant(t *testing.T) {
	for _, in := range []string{
		"Die Höhle", "Les Misérables", "Ångström Chronicles", "Dvořák Cycle",
	} {
		nfc, nfd := norm.NFC.String(in), norm.NFD.String(in)
		if nfc == nfd {
			t.Fatalf("%q has no distinct NFD form; the case would be vacuous", in)
		}
		if got, want := seriesSlug(nfd), seriesSlug(nfc); got != want {
			t.Errorf("seriesSlug is not Unicode-form invariant for %q: NFD %q vs NFC %q", in, got, want)
		}
	}
}

// TestSeriesSlugKeepsScriptsDistinct re-pins #1645 alongside it.
func TestSeriesSlugKeepsScriptsDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, in := range []string{"三体", "黑暗森林", "Преступление", "Наказание", "The Expanse"} {
		slug := seriesSlug(in)
		if slug == "" {
			t.Errorf("seriesSlug(%q) = \"\" — every series in that script would share one row", in)
			continue
		}
		if prev, dup := seen[slug]; dup {
			t.Errorf("seriesSlug collapsed %q and %q onto %q", prev, in, slug)
		}
		seen[slug] = in
	}
}
