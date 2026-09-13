package normdrift

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/textutil"
)

// TestSearchKeyIsNotAnAuthorIdentityBlockingKey answers the question anyone
// touching db.findAliasesByIdentity will ask: author_aliases.search_key is
// already stored and indexable-ish, so why does the identity lookup scan the
// table in Go instead of narrowing on it first?
//
// Narrowing is only sound if identity-equality IMPLIES search-key equality —
// a blocking key may over-collect, never under-collect. It does not imply it.
//
// The reason changed with #2452 and the old one is worth keeping, because it
// is how this test was originally argued. NormalizeAuthorName used to drop
// every combining mark it saw while FoldForSearch was script-aware, so a kana
// dakuten, Hebrew niqqud, Arabic harakat or the Cyrillic breve on й gave one
// identity two search keys (#1645). Those pairs were the witnesses.
//
// They are no longer witnesses, because they are no longer one identity.
// NormalizeAuthorName now strips marks only on a Latin or Greek base, through
// the same helper FoldForSearch uses, so ズ and ス are two people rather than
// one. That is the fix, and it removes this test's easiest evidence: on
// non-spacing marks the two folds now agree.
//
// What still separates them is the marks that are not Mn. A Devanagari spacing
// vowel sign is Mc, so it is not a letter, and NormalizeAuthorName drops it as
// a separator while FoldForSearch keeps it as part of the word. One identity,
// two search keys, and a search_key predicate would still drop the true match.
//
// The witnesses are asserted individually and then re-derived from the shared
// corpora so the property does not rest on a hand-written list. If this test
// ever fails for want of a witness, the implication may have become true — do
// not take that as permission to use the blocking key without proving it again
// over a corpus wider than this one.
func TestSearchKeyIsNotAnAuthorIdentityBlockingKey(t *testing.T) {
	// Pairs that used to be one identity and are now two. Each is a mark that
	// changes the letter rather than decorating it, which is the whole of
	// #2452: merging these merged two people.
	distinct := []struct {
		a, b string
		why  string
	}{
		{"ハード", "ハート", "kana dakuten"},
		{"Толстой", "Толстои", "Cyrillic breve on й: a letter, not a diacritic"},
		{"Фёдор Достоевский", "Федор Достоевскии", "the same, in a name a provider really sends"},
		{"עִברִית", "עברית", "Hebrew niqqud"},
		{"كَتَبَ", "كتب", "Arabic harakat"},
	}
	for _, d := range distinct {
		if ia, ib := textutil.NormalizeAuthorName(d.a), textutil.NormalizeAuthorName(d.b); ia == ib {
			t.Errorf("%q and %q share one identity key (%q) — %s. #2452 regressed and two authors merge here",
				d.a, d.b, ia, d.why)
		}
	}

	witnesses := []struct {
		a, b string
		why  string
	}{
		{"कमला", "कमल", "Devanagari spacing vowel sign: Mc, so identity drops it as a separator and search keeps it"},
	}
	for _, w := range witnesses {
		if ia, ib := textutil.NormalizeAuthorName(w.a), textutil.NormalizeAuthorName(w.b); ia != ib {
			t.Errorf("witness %q / %q no longer shares one identity key (%q vs %q) — %s",
				w.a, w.b, ia, ib, w.why)
			continue
		}
		if sa, sb := textutil.FoldForSearch(w.a), textutil.FoldForSearch(w.b); sa == sb {
			t.Errorf("witness %q / %q now shares a search key (%q) — %s", w.a, w.b, sa, w.why)
		}
	}

	corpus := blockingKeyCorpus(t)
	found := 0
	for i, a := range corpus {
		for _, b := range corpus[i+1:] {
			if textutil.NormalizeAuthorName(a) != textutil.NormalizeAuthorName(b) {
				continue
			}
			if textutil.FoldForSearch(a) != textutil.FoldForSearch(b) {
				found++
			}
		}
	}
	if found == 0 {
		t.Errorf("no corpus pair is one author identity under two search keys (%d inputs). "+
			"Either the corpus lost its non-Latin entries or the folds converged; "+
			"the identity lookups in internal/db still must not filter on search_key "+
			"until the implication is proved, not merely unfalsified here", len(corpus))
	} else {
		t.Logf("%d corpus pairs share an identity key but not a search key (of %d inputs)", found, len(corpus))
	}
}

// blockingKeyCorpus is every adversarial input in this package plus the shared
// search fixtures, in both Unicode forms, plus two generated variants per
// input: the same string with every non-spacing mark removed, and with every
// spacing mark removed.
//
// The second generator is the one that produces witnesses now. Stripping Mn
// used to leave the identity key untouched, because NormalizeAuthorName
// stripped marks itself; since #2452 it does so only on a Latin or Greek base,
// and there search strips them too, so those pairs agree. Stripping Mc still
// leaves the identity key untouched, because NormalizeAuthorName drops a
// spacing mark as a separator whatever the script, while search keeps it.
func blockingKeyCorpus(t *testing.T) []string {
	t.Helper()
	base := append([]string{}, adversarialTitles...)
	base = append(base, adversarialAuthors...)
	base = append(base, adversarialSearchInputs...)

	// testdata/search_fixtures.json is the corpus #2447 built for the search
	// fold, shared with the web suite. Read from its owning package rather
	// than copied, so it cannot go stale here.
	raw, err := os.ReadFile("../textutil/testdata/search_fixtures.json")
	if err != nil {
		t.Fatalf("read search fixtures: %v", err)
	}
	var fixtures []struct {
		Input string `json:"input"`
		Want  string `json:"want"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("parse search fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("search fixtures are empty")
	}
	for _, f := range fixtures {
		base = append(base, f.Input, f.Want)
	}

	out := append([]string{}, base...)
	for _, s := range base {
		out = append(out, norm.NFC.String(s), norm.NFD.String(s), stripCombiningMarks(s), stripSpacingMarks(s))
	}
	return out
}

// stripCombiningMarks removes every non-spacing mark from s. Since #2452 this
// no longer preserves the NormalizeAuthorName key outside Latin and Greek,
// which is the fix rather than a defect; it stays because the variants are
// still useful corpus inputs.
func stripCombiningMarks(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// stripSpacingMarks removes every spacing combining mark (Mc) from s. Whatever
// it returns has the same NormalizeAuthorName key as s, because that function
// keeps only letters and digits and a spacing mark is neither, while
// FoldForSearch keeps it. That gap is what makes each pair a witness.
func stripSpacingMarks(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mc, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}
