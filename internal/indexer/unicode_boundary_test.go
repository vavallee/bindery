package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// Regression tests for #1642: Go's RE2 `\b` is defined against ASCII `\w`, so
// every matcher built on it was structurally incapable of matching a token
// whose first or last rune is non-ASCII — while NormalizeRelease deliberately
// preserves \p{L}/\p{N}. Titles and authors in Cyrillic, CJK, Hebrew, Arabic,
// Greek and edge-accented Latin returned zero results even against a
// byte-identical release name.
//
// The cases below are drawn from a real library: 110 of 382 books with a
// non-ASCII title could not match ANY release before this fix.

func TestWordBoundaryRegexMatchesNonASCIITokens(t *testing.T) {
	cases := []struct {
		name     string
		keyword  string
		haystack string
		want     bool
	}{
		{"leading accent", "åsa", "åsa larsson sun storm epub", true},
		{"trailing accent", "josé", "josé saramago blindness epub", true},
		{"trailing accent 2", "créo", "el mundo que jones créo epub", true},
		{"nordic slash", "nesbø", "jo nesbø the snowman epub", true},
		{"cjk single token", "刘慈欣", "刘慈欣 三体 epub", true},
		{"cjk title", "三体", "刘慈欣 三体 epub", true},
		{"cyrillic", "достоевский", "фёдор достоевский преступление epub", true},
		{"greek", "ὀδύσσεια", "ὀδύσσεια epub", true},
		{"interior accent still works", "miéville", "china miéville kraken epub", true},
		{"ascii control", "sparrow", "mary doria russell the sparrow epub", true},

		// Must NOT match: the boundary still has to be a real boundary.
		{"substring rejected", "sparrow", "sparrows epub", false},
		{"cjk substring rejected", "三体", "三体问题 epub", false},
		{"accent substring rejected", "josé", "josép epub", false},
		{"digit adjacency rejected", "dune", "dune2 epub", false},
		{"absent", "åsa", "stieg larsson epub", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WordBoundaryRegex(tc.keyword).MatchString(tc.haystack); got != tc.want {
				t.Errorf("WordBoundaryRegex(%q).MatchString(%q) = %v, want %v",
					tc.keyword, tc.haystack, got, tc.want)
			}
		})
	}
}

func TestContainsPhraseNonASCII(t *testing.T) {
	if !ContainsPhrase("фёдор достоевский братья карамазовы epub",
		[]string{"братья", "карамазовы"}) {
		t.Error("Cyrillic contiguous phrase should match")
	}
	if !ContainsPhrase("刘慈欣 三体 epub", []string{"刘慈欣", "三体"}) {
		t.Error("CJK contiguous phrase should match")
	}
	if !ContainsPhrase("mary doria russell the sparrow epub", []string{"the", "sparrow"}) {
		t.Error("ASCII phrase regression")
	}
	// Non-contiguous must still be rejected.
	if ContainsPhrase("братья и карамазовы epub", []string{"братья", "карамазовы"}) {
		t.Error("non-contiguous Cyrillic phrase should NOT match as a phrase")
	}
}

// TestContainsPhraseTreatsNonASCIIWordsAsWords pins a deliberate behaviour
// change from #1642. The old pattern joined words with `\W+`, and RE2's `\W`
// is [^0-9A-Za-z_] — so every non-ASCII LETTER counted as punctuation and a
// "contiguous" phrase would silently skip straight over an intervening
// Cyrillic/CJK word. ASCII never behaved that way ("lord rings" does not match
// "lord of the rings" as a phrase), so the two scripts disagreed.
//
// Now they agree: an intervening word breaks contiguity regardless of script,
// and such titles are handled by containsInOrder like their ASCII equivalents.
func TestContainsPhraseTreatsNonASCIIWordsAsWords(t *testing.T) {
	hay := "преступление и наказание epub"
	kws := []string{"преступление", "наказание"}
	if ContainsPhrase(hay, kws) {
		t.Error("an intervening Cyrillic word must break phrase contiguity, as it does for ASCII")
	}
	// ...and the in-order path, which tolerates gaps, still accepts it — so the
	// title is not lost, it is just routed the same way ASCII titles are.
	if !containsInOrder(hay, kws) {
		t.Error("containsInOrder should still accept the gapped Cyrillic sequence")
	}
}

func TestContainsInOrderNonASCIIAndAdjacency(t *testing.T) {
	// Adjacency is the case the separator-consuming rewrite has to get right:
	// the separator after word N is consumed, so word N+1 has none to assert on.
	if !containsInOrder("刘慈欣 三体 epub", []string{"刘慈欣", "三体"}) {
		t.Error("adjacent CJK words should match in order")
	}
	if !containsInOrder("the lord of the rings epub", []string{"lord", "rings"}) {
		t.Error("ASCII in-order with gap regression")
	}
	if !containsInOrder("lord rings epub", []string{"lord", "rings"}) {
		t.Error("ADJACENT ASCII words should match in order")
	}
	if !containsInOrder("ὀδύσσεια και ιλιάδα epub", []string{"ὀδύσσεια", "ιλιάδα"}) {
		t.Error("Greek in-order should match")
	}
	// Reordered must still be rejected — that is the whole point of in-order.
	if containsInOrder("rings of the lord epub", []string{"lord", "rings"}) {
		t.Error("reordered sequence should NOT match")
	}
	if containsInOrder("三体 刘慈欣 epub", []string{"刘慈欣", "三体"}) {
		t.Error("reordered CJK sequence should NOT match")
	}
}

// TestFilterRelevantNonASCIIEndToEnd drives the real pipeline, not just the
// regex helpers, using titles taken verbatim from a real affected library.
func TestFilterRelevantNonASCIIEndToEnd(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		author  string
		release string
	}{
		{"cjk title and author", "三体", "刘慈欣", "刘慈欣 - 三体 - retail epub"},
		{"spanish trailing accent", "El mundo que Jones creó", "Philip K. Dick",
			"El.mundo.que.Jones.creó.Philip.K.Dick.epub"},
		{"french leading accent", "Édition collector", "Frank Herbert",
			"Frank Herbert - Édition collector - epub"},
		{"turkish", "Ölümün Sonu", "Cixin Liu", "Cixin Liu - Ölümün Sonu.epub"},
		{"hebrew", "Ḥalaliyah", "Stanislaw Lem", "Stanislaw Lem - Ḥalaliyah epub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := toResults(tc.release)
			got := filterRelevant(results, tc.title, tc.author, nil)
			if len(got) != 1 {
				t.Errorf("filterRelevant(%q, %q) dropped its own byte-identical release %q; got %v",
					tc.title, tc.author, tc.release, resultTitles(got))
			}
		})
	}
}

// TestFilterRelevantCJKAuthorRescuesSingleWordASCIITitle covers the subtler half
// of #1642: a single-significant-word title takes the case-1 branch, which
// REQUIRES authorMatchesRelease. With a CJK author token that branch could never
// pass, so pure-ASCII titles like "Circle" by 刘慈欣 were unfindable.
func TestFilterRelevantCJKAuthorRescuesSingleWordASCIITitle(t *testing.T) {
	for _, title := range []string{"Circle", "Butterfly", "Eden"} {
		release := "刘慈欣 - " + title + " - retail epub"
		got := filterRelevant(toResults(release), title, "刘慈欣", nil)
		if len(got) != 1 {
			t.Errorf("single-word ASCII title %q by CJK author: expected %q to pass, got %v",
				title, release, resultTitles(got))
		}
	}
	// The author gate must still REJECT a release by someone else.
	got := filterRelevant(toResults("Andy Weir - Circle - epub"), "Circle", "刘慈欣", nil)
	if len(got) != 0 {
		t.Errorf("wrong-author release should still be rejected for a CJK author; got %v", resultTitles(got))
	}
}

// TestNonASCIISigWordsSurviveTokenization guards the assumption the fix rests
// on: SigWords must actually emit non-ASCII tokens for the matchers to match.
func TestNonASCIISigWordsSurviveTokenization(t *testing.T) {
	if got := newznab.SigWords("三体"); len(got) != 1 || got[0] != "三体" {
		t.Errorf(`SigWords("三体") = %v, want ["三体"]`, got)
	}
	if got := newznab.SigWords("Ölümün Sonu"); len(got) != 2 {
		t.Errorf(`SigWords("Ölümün Sonu") = %v, want 2 tokens`, got)
	}
}
