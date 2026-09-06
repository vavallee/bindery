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
// a blocking key may over-collect, never under-collect. It does not imply it,
// and the direction of the failure is the surprising one: FoldForSearch is the
// LOSSIER fold on Latin (ß→ss, NFKC, "&"→and), but the STRICTER one on every
// other script. NormalizeAuthorName drops every combining mark it sees, while
// FoldForSearch is script-aware and keeps the marks that are part of the
// letter — a kana dakuten, Hebrew niqqud, Arabic harakat, a Devanagari vowel
// sign, the Cyrillic breve on й (#1645). So one identity routinely has two
// search keys, and a search_key predicate would silently drop the true match.
//
// The witnesses below are asserted individually, and then re-derived from the
// shared corpora so the property does not rest on a hand-written list. If this
// test ever fails for want of a witness, the implication may have become true
// — do not take that as permission to use the blocking key without proving it
// again over a corpus wider than this one.
func TestSearchKeyIsNotAnAuthorIdentityBlockingKey(t *testing.T) {
	witnesses := []struct {
		a, b string
		why  string
	}{
		{"ハード", "ハート", "kana dakuten: identity strips it, search keeps it (#1645)"},
		{"Толстой", "Толстои", "Cyrillic breve on й: a letter, not a diacritic"},
		{"Фёдор Достоевский", "Федор Достоевскии", "the same, in a name a provider really sends"},
		{"कमला", "कमल", "Devanagari spacing vowel sign"},
		{"עִברִית", "עברית", "Hebrew niqqud"},
		{"كَتَبَ", "كتب", "Arabic harakat"},
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
// search fixtures, in both Unicode forms, plus one generated variant per input:
// the same string with every combining mark removed. That generator is the
// point — NormalizeAuthorName strips marks itself, so dropping them cannot
// change the identity key, which makes each pair a candidate the folds have to
// agree on and they mostly do not.
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
		out = append(out, norm.NFC.String(s), norm.NFD.String(s), stripCombiningMarks(s))
	}
	return out
}

// stripCombiningMarks removes every non-spacing mark from s. Whatever it
// returns has the same NormalizeAuthorName key as s.
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
