package db

import (
	"strings"
	"testing"
)

// TestSearchRankClauseBindsEveryPlaceholder is the contract between the two
// halves of searchrank.go. They are written apart — one builds SQL, the other
// builds the values — so a new tier added to one and not the other produces a
// count mismatch that SQLite reports only when someone actually searches, and
// only as a generic bind error.
func TestSearchRankClauseBindsEveryPlaceholder(t *testing.T) {
	for _, columns := range [][]string{
		{"authors.search_key"},
		{"books.search_key", "COALESCE(au.search_key, '')"},
		{"a", "b", "c"},
	} {
		clause := searchRankClause(columns...)
		want := strings.Count(clause, "?")
		if got := len(searchRankArgs("query", len(columns))); got != want {
			t.Errorf("%d column(s): clause has %d placeholders, args supplies %d", len(columns), want, got)
		}
	}
}

// TestSearchRankPatternsCoverEveryTier keeps the pattern list and the WHEN list
// the same length, which is the other half of the same contract.
func TestSearchRankPatternsCoverEveryTier(t *testing.T) {
	if got := len(searchRankPatterns("q")); got != searchRankTiers {
		t.Fatalf("searchRankPatterns returned %d values, want one per tier (%d)", got, searchRankTiers)
	}
	if got := strings.Count(searchRankClause("c"), " WHEN "); got != searchRankTiers {
		t.Errorf("clause has %d WHEN branches, want %d", got, searchRankTiers)
	}
}

// TestSearchRankEscapesOnlyThePatterns pins a distinction that is easy to get
// backwards: the equality tier compares the folded query verbatim, while the
// LIKE tiers compare an escaped copy. Escaping the equality operand would make
// a title containing "%" or "_" unfindable by its own exact name.
func TestSearchRankEscapesOnlyThePatterns(t *testing.T) {
	patterns := searchRankPatterns("50% off_now")
	if patterns[0] != "50% off_now" {
		t.Errorf("equality operand = %q, want the query unescaped", patterns[0])
	}
	for i, p := range patterns[1:] {
		if !strings.Contains(p, `\%`) || !strings.Contains(p, `\_`) {
			t.Errorf("LIKE pattern %d = %q, want the metacharacters escaped", i+1, p)
		}
	}
}
