package importer

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestAuthorMatchDottedInitials pins the #1646 half of the #1608 bug class:
// significantAuthorTokens used to trim punctuation only at the ENDS of a
// whitespace-split token, so "J.R.R." survived as the 5-character token
// "j.r.r" — long enough to clear the >=3 gate that exists to discard initials,
// and impossible for any author name to contain.
//
// It was directional. The failing direction is the one that actually occurs:
// the PARSED side (an author folder, or an ID3 Artist tag, where audiobook
// releases overwhelmingly write "J.R.R. Tolkien") has the run-together form
// while the catalogue has the spaced form OpenLibrary and DNB return.
func TestAuthorMatchDottedInitials(t *testing.T) {
	cases := []struct {
		name      string
		catalogue string
		parsed    string
	}{
		{"run-together parsed vs spaced catalogue", "J. R. R. Tolkien", "J.R.R. Tolkien"},
		{"spaced parsed vs run-together catalogue", "J.R.R. Tolkien", "J. R. R. Tolkien"},
		{"middle initials run together", "George R. R. Martin", "George R.R. Martin"},
		{"trailing dot only", "P. G. Wodehouse", "P.G. Wodehouse"},
		{"initials dropped entirely still matches", "George R. R. Martin", "George Martin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !authorMatch(tc.catalogue, tc.parsed) {
				t.Errorf("authorMatch(%q, %q) = false, want true (tokens=%v)",
					tc.catalogue, tc.parsed, significantAuthorTokens(tc.parsed))
			}
		})
	}
}

// TestAuthorMatchUnicodeForms covers the second #1646 defect: nothing in
// internal/importer applied Unicode normalization, while every provider that
// fills the catalogue emits NFC and macOS hands filenames back decomposed. The
// two byte forms of the same name never met.
func TestAuthorMatchUnicodeForms(t *testing.T) {
	for _, name := range []string{"Björn Andersen", "José Saramago", "Åsa Larsson", "China Miéville"} {
		nfc, nfd := norm.NFC.String(name), norm.NFD.String(name)
		if nfc == nfd {
			t.Fatalf("%q has no distinct NFD form; test would be vacuous", name)
		}
		if !authorMatch(nfc, nfd) {
			t.Errorf("authorMatch(NFC %q, NFD) = false, want true", name)
		}
		if !authorMatch(nfd, nfc) {
			t.Errorf("authorMatch(NFD %q, NFC) = false, want true", name)
		}
	}
}

// TestAuthorMatchStillRejectsWrongAuthors is the guard on the other side: the
// fix normalizes both sides, which is a widening, so the discrimination the
// predicate exists for has to be re-pinned.
func TestAuthorMatchStillRejectsWrongAuthors(t *testing.T) {
	cases := []struct {
		catalogue string
		parsed    string
	}{
		{"J. R. R. Tolkien", "Terry Pratchett"},
		{"George R. R. Martin", "George Orwell"},
		{"Björn Andersen", "Björn Larsson"},
		{"Matt Dinniman", "David Wong"},
		// A shared surname is not enough on its own.
		{"Emily Brontë", "Charlotte Brontë"},
	}
	for _, tc := range cases {
		if authorMatch(tc.catalogue, tc.parsed) {
			t.Errorf("authorMatch(%q, %q) = true, want false", tc.catalogue, tc.parsed)
		}
	}
}

// TestAuthorMatchEmptyAndInitialsOnly pins the documented "can't disprove"
// contract, which matchingAuthors depends on: a nil author set there means
// "every author is a candidate", so getting this wrong silently widens or
// empties the entire title tier.
func TestAuthorMatchEmptyAndInitialsOnly(t *testing.T) {
	if !authorMatch("J. R. R. Tolkien", "") {
		t.Error("empty parsed author should not filter")
	}
	if !authorMatch("J. R. R. Tolkien", "J. R. R.") {
		t.Error("initials-only parsed author should not filter")
	}
	if got := significantAuthorTokens("J.R.R."); len(got) != 0 {
		t.Errorf("significantAuthorTokens(%q) = %v, want empty — these are initials", "J.R.R.", got)
	}
}

// TestAuthorWordIndexIsSuperset re-proves the invariant ScanLibrary's candidate
// pruning rests on: for any author authorMatch accepts, that author appears in
// the bucket of EVERY significant token of the parsed name. If it ever stops
// holding, the scan silently drops books rather than slowing down.
func TestAuthorWordIndexIsSuperset(t *testing.T) {
	catalogue := []string{
		"J. R. R. Tolkien", "George R. R. Martin", "Björn Andersen",
		"Stanisław Lem", "刘慈欣", "Mary-Kate Olsen", "Terry Pratchett",
		"Ursula K. Le Guin", "Jo Nesbø", "José Saramago",
	}
	index := make(map[string][]int)
	for i, name := range catalogue {
		for _, tok := range authorNameTokens(name) {
			index[tok] = append(index[tok], i)
		}
	}

	parsedForms := []string{
		"J.R.R. Tolkien", "J. R. R. Tolkien", "Tolkien", "George R.R. Martin",
		norm.NFD.String("Björn Andersen"), "Bjorn Andersen", "Stanislaw Lem",
		"刘慈欣", "Mary Kate Olsen", "Mary-Kate Olsen", "Ursula Le Guin",
		"Jo Nesbo", "Jose Saramago", "Nobody At All",
	}
	for _, parsed := range parsedForms {
		tokens := significantAuthorTokens(parsed)
		if len(tokens) == 0 {
			continue
		}
		for i, name := range catalogue {
			if !authorMatch(name, parsed) {
				continue
			}
			for _, tok := range tokens {
				if !slices.Contains(index[tok], i) {
					t.Errorf("index broken: authorMatch(%q, %q) is true but author is absent from bucket %q",
						name, parsed, tok)
				}
			}
		}
	}
}

// TestTitleSigTokensFoldsNonASCII covers the title half of #1646. The old
// tokenizer kept only [a-z0-9] and treated every other rune as a separator, so
// "Die Höhle" reduced to [die hle]: the ö split the word and the leftover
// single-rune "h" fell under the 2-character floor.
func TestTitleSigTokensFoldsNonASCII(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Die Höhle", []string{"die", "hoehle"}},
		{"Die Hoehle", []string{"die", "hoehle"}},
		{"Der Prozeß", []string{"der", "prozess"}},
		{"Der Prozess", []string{"der", "prozess"}},
		{"Ender's Game", []string{"enders", "game"}},
		{"Enders Game", []string{"enders", "game"}},
		{"三体", []string{"三体"}},
	}
	for _, tc := range cases {
		if got := titleSigTokens(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("titleSigTokens(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestTitleMatchAcrossSpellings is the end-to-end version: these pairs are the
// same book spelled the way a library folder and a metadata provider each spell
// it, and titleMatch has to say so.
func TestTitleMatchAcrossSpellings(t *testing.T) {
	pairs := [][2]string{
		{"Ender's Game", "Enders Game"},
		{"Die Höhle", "Die Hoehle"},
		{"Der Prozeß", "Der Prozess"},
		{"Fräulein Smillas Gespür für Schnee", "Fraeulein Smillas Gespuer fuer Schnee"},
		{norm.NFC.String("Café Society"), norm.NFD.String("Café Society")},
	}
	for _, p := range pairs {
		if !titleMatch(p[0], p[1]) {
			t.Errorf("titleMatch(%q, %q) = false, want true", p[0], p[1])
		}
		if !titleMatch(p[1], p[0]) {
			t.Errorf("titleMatch(%q, %q) = false, want true", p[1], p[0])
		}
	}
}

// TestTitleMatchStillRejectsDifferentBooks guards the widening above.
func TestTitleMatchStillRejectsDifferentBooks(t *testing.T) {
	pairs := [][2]string{
		{"The Hobbit", "The Silmarillion"},
		{"Die Höhle", "Die Wand"},
		{"1984", "2001"},
		{"Ender's Game", "Speaker for the Dead"},
	}
	for _, p := range pairs {
		if titleMatch(p[0], p[1]) {
			t.Errorf("titleMatch(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// TestLookupAuthorMatchTransliteration covers the manual/bulk-import matcher,
// which had no transliteration at all: raw Jaro-Winkler puts
// "Boell, Heinrich" against "Heinrich Böll" at 0.441, nowhere near its 0.80
// threshold, because everything after the shared prefix differs.
func TestLookupAuthorMatchTransliteration(t *testing.T) {
	for _, tc := range [][2]string{
		{"Boell, Heinrich", "Heinrich Böll"},
		{"Heinrich Boell", "Heinrich Böll"},
		{"Joerg Mueller", "Jörg Müller"},
		{"Jo Nesbo", "Jo Nesbø"},
	} {
		if !lookupAuthorMatch(tc[0], tc[1]) {
			t.Errorf("lookupAuthorMatch(%q, %q) = false, want true", tc[0], tc[1])
		}
	}
	// Still discriminating.
	if lookupAuthorMatch("Heinrich Böll", "Thomas Mann") {
		t.Error("lookupAuthorMatch matched two unrelated authors")
	}
}

// TestAuthorNameTokensCacheDoesNotAlias catches the aliasing hazard introduced
// by memoising a slice: callers must never mutate the shared return value, and
// the cache must hand back the same content every time.
func TestAuthorNameTokensCacheDoesNotAlias(t *testing.T) {
	const name = "Ursula K. Le Guin"
	want := strings.Fields("ursula k le guin")
	for range 3 {
		if got := authorNameTokens(name); !slices.Equal(got, want) {
			t.Fatalf("authorNameTokens(%q) = %v, want %v", name, got, want)
		}
	}
}
