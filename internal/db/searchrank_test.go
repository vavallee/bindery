package db

import (
	"strings"
	"testing"
	"time"
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

// TestSearchTokensCapBoundsQueryCost pins the bound on how many words of a
// query become WHERE clauses. Without it the statement grows with the input:
// a 50,000 word search param ran for over a minute before SQLite gave up on
// expression depth. With it a 1,000 word query is eight LIKE pairs, returns
// in milliseconds, and still matches on the words that made the cut.
func TestSearchTokensCapBoundsQueryCost(t *testing.T) {
	books, authors, _, ctx := seedSearchLibrary(t)

	// Every token must appear in the row, so the filler repeats a word the
	// expected row has: the query is 1,000 tokens long, the statement binds
	// eight of them, and the match is decided by the words that made the cut.
	start := time.Now()
	got, _, err := books.ListPageFiltered(ctx, BookListFilter{Search: "harry orden" + strings.Repeat(" potter", 998)}, 10, 0)
	if err != nil {
		t.Fatalf("books search with 1,000 tokens: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Harry Potter und der Orden des Phönix" {
		t.Errorf("expected the first tokens to still match, got %+v", got)
	}
	gotAuthors, _, err := authors.ListPageFiltered(ctx, AuthorListFilter{Search: "holly" + strings.Repeat(" black", 999)}, 10, 0)
	if err != nil {
		t.Fatalf("authors search with 1,000 tokens: %v", err)
	}
	if len(gotAuthors) != 1 || gotAuthors[0].Name != "Holly Black" {
		t.Errorf("expected the first tokens to still match, got %+v", gotAuthors)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("two 1,000 token searches took %s; the token cap is not bounding the statement", elapsed)
	}

	// The ninth word and beyond never reach the WHERE clause.
	if toks := searchTokens(strings.TrimSpace(strings.Repeat("w ", 20))); len(toks) != maxSearchTokens {
		t.Errorf("expected %d tokens, got %d", maxSearchTokens, len(toks))
	}
}
