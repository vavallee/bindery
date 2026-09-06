package textutil_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/textutil"
)

// searchFixture is one row of testdata/search_fixtures.json. The file is shared
// with the web build (web/src/util/foldForSearch.test.ts reads the same JSON),
// so the Go key written into search_key and the JS fold the Books page applies
// to the query cannot drift apart without one of the two suites going red.
//
// Issue is the report the row came from, so a future reader can see which real
// failure each expectation is standing in for rather than guessing whether a
// row is load bearing.
type searchFixture struct {
	Input string `json:"input"`
	Want  string `json:"want"`
	Issue string `json:"issue"`
	Note  string `json:"note"`
}

func loadSearchFixtures(t *testing.T) []searchFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/search_fixtures.json")
	if err != nil {
		t.Fatalf("read search fixtures: %v", err)
	}
	var out []searchFixture
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse search fixtures: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("search fixtures are empty")
	}
	return out
}

func TestFoldForSearchFixtures(t *testing.T) {
	for _, f := range loadSearchFixtures(t) {
		if got := textutil.FoldForSearch(f.Input); got != f.Want {
			t.Errorf("FoldForSearch(%q) = %q, want %q (#%s %s)", f.Input, got, f.Want, f.Issue, f.Note)
		}
	}
}

// TestFoldForSearchIsUnicodeFormInvariant is the property that matters most for
// a STORED key: the row is folded from whatever spelling the provider sent and
// the query is folded from whatever the user's keyboard and OS produced. macOS
// hands back decomposed filenames while every metadata provider returns
// composed ones, so the two forms meet on this column constantly (#1646).
func TestFoldForSearchIsUnicodeFormInvariant(t *testing.T) {
	for _, f := range loadSearchFixtures(t) {
		for _, form := range []struct {
			name string
			in   string
		}{
			{"NFC", norm.NFC.String(f.Input)},
			{"NFD", norm.NFD.String(f.Input)},
			{"NFKC", norm.NFKC.String(f.Input)},
			{"NFKD", norm.NFKD.String(f.Input)},
		} {
			if got := textutil.FoldForSearch(form.in); got != f.Want {
				t.Errorf("FoldForSearch(%s(%q)) = %q, want %q (#%s)", form.name, f.Input, got, f.Want, f.Issue)
			}
		}
	}
}

// TestFoldForSearchKeysAreFixedPoints guards the backfill. A stored key is read
// back and compared against a freshly folded one to decide whether a row needs
// rewriting; if folding a key produced a different key, every boot would
// rewrite every row forever.
func TestFoldForSearchKeysAreFixedPoints(t *testing.T) {
	for _, f := range loadSearchFixtures(t) {
		if got := textutil.FoldForSearch(f.Want); got != f.Want {
			t.Errorf("FoldForSearch(%q) = %q, want the key to be unchanged (#%s)", f.Want, got, f.Issue)
		}
	}
}

// TestFoldForSearchKeepsDistinctScriptsDistinct is the counterweight to every
// other test here. FoldForSearch is deliberately lossy, and the failure mode of
// an over-eager fold is worse than a missed match: #1645 collapsed every
// non-Latin series onto one shared key, which corrupted data rather than merely
// hiding it. These pairs must NOT converge.
func TestFoldForSearchKeepsDistinctScriptsDistinct(t *testing.T) {
	pairs := []struct{ a, b, why string }{
		{"ハード", "ハート", "kana dakuten is part of the letter (hard vs heart)"},
		{"ドラゴン", "トラゴン", "kana dakuten"},
		{"Толстой", "Толстои", "Cyrillic й and и are separate letters"},
		{"कमला", "कमल", "Devanagari vowel signs are spacing marks, not accents"},
		{"三体", "三休", "distinct Han characters"},
		{"Dune", "Dunes", "no stemming: a plural is a different key"},
	}
	for _, p := range pairs {
		if got, other := textutil.FoldForSearch(p.a), textutil.FoldForSearch(p.b); got == other {
			t.Errorf("FoldForSearch collapsed %q and %q onto %q: %s", p.a, p.b, got, p.why)
		}
	}
}

// TestFoldForSearchIsConcurrencySafe exercises the per-call cases.Caser. A
// shared Caser is stateful and panics under concurrent use, which is the bug
// #1374 hit with a shared accent stripper; search keys are written from
// concurrent import workers, so the same trap applies here.
func TestFoldForSearchIsConcurrencySafe(t *testing.T) {
	const workers = 16
	done := make(chan string, workers)
	for i := 0; i < workers; i++ {
		go func() {
			var last string
			for j := 0; j < 200; j++ {
				last = textutil.FoldForSearch("Straße der Träume – Jo Nesbø & Åsa Larsson")
			}
			done <- last
		}()
	}
	want := textutil.FoldForSearch("Straße der Träume – Jo Nesbø & Åsa Larsson")
	for i := 0; i < workers; i++ {
		if got := <-done; got != want {
			t.Fatalf("concurrent fold produced %q, want %q", got, want)
		}
	}
}

// TestFoldForSearchASCIIFastPathAgrees is the proof obligation for the ASCII
// shortcut in FoldForSearch. The shortcut exists because the general path runs
// on every write and every keystroke, and it is only safe if it is
// indistinguishable from the path it skips — a divergence would mean the key
// stored for an ASCII title and the key folded from an ASCII query disagree,
// which is the exact failure this whole file guards against.
//
// Rather than reason about it, this drives both paths over the same inputs:
// every fixture that is pure ASCII, an exhaustive sweep of every printable
// ASCII character in three positions, and every pair of ASCII characters.
func TestFoldForSearchASCIIFastPathAgrees(t *testing.T) {
	// textutil is imported as an external test package, so reach the general
	// path the same way any caller would: by handing it input it cannot take
	// the shortcut on, then removing the non-ASCII marker again. A leading
	// ideograph forces the slow path and folds to itself, so stripping it from
	// the result leaves exactly what the slow path made of the rest.
	const marker = "三"
	viaSlowPath := func(s string) string {
		got := textutil.FoldForSearch(marker + " " + s)
		return strings.TrimPrefix(strings.TrimPrefix(got, marker), " ")
	}

	check := func(t *testing.T, in string) {
		t.Helper()
		if fast, slow := textutil.FoldForSearch(in), viaSlowPath(in); fast != slow {
			t.Errorf("FoldForSearch(%q): fast path = %q, general path = %q", in, fast, slow)
		}
	}

	for _, f := range loadSearchFixtures(t) {
		if isPureASCII(f.Input) {
			check(t, f.Input)
		}
	}

	for c := byte(0x20); c < 0x7f; c++ {
		for _, shape := range []string{"%c", "a%cb", "%cabc", "abc%c", "a %c b"} {
			check(t, fmt.Sprintf(shape, c))
		}
	}

	for a := byte(0x20); a < 0x7f; a++ {
		for b := byte(0x20); b < 0x7f; b++ {
			check(t, string([]byte{'x', a, b, 'y'}))
		}
	}
}

func isPureASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
