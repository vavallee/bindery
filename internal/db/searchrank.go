package db

import (
	"strconv"
	"strings"
)

// This file holds the tiered ordering shared by the Books and Authors search
// (#1660). Both lists match a folded query against a folded key column with
// LIKE, which answers "does this row match" but says nothing about how well, so
// without a ranking the best hit for "dune" was whichever matching row happened
// to sort first alphabetically.
//
// The tiers are the ones every typeahead system converges on — Algolia's
// "exact" ranking criterion, Meilisearch's exactness rule, Typesense's
// prioritize_exact_match — with one distinction those systems make that a naive
// prefix/substring ladder misses: matching a COMPLETE WORD is stronger evidence
// than matching the beginning of a longer one. Searching "thor" should offer
// Brad Thor before Thornton Wilder, even though only the latter is a prefix of
// the field, because the user typed a whole name and one of the two rows has it.
//
//	0  the query IS the whole field                "dune"      → Dune
//	1  the field STARTS with it, as a whole word   "dune"      → Dune Messiah
//	2  it appears as a whole word                  "dune"      → Children of Dune
//	3  the field starts with it, mid-word          "thor"      → Thornton Wilder
//	4  it starts a word, mid-word                  "thor"      → Brad Thornton
//	5  it appears anywhere at all                  "une"       → Dune
//	6  no contiguous hit: the row matched only because its words were found
//	   separately, which is the weakest evidence the WHERE clause accepts
//
// Whole-word tests run against the field padded with a space at each end, so
// one pattern covers a word at the start, in the middle and at the end rather
// than needing three.
const searchRankTiers = 6

// searchRankPatterns returns the value bound at each tier, in tier order. The
// first is an equality operand; the rest are LIKE patterns over the padded or
// bare column, matching the WHEN order in searchRankClause.
func searchRankPatterns(folded string) []string {
	esc := escapeLike(folded)
	return []string{
		folded,            // 0: field = query
		" " + esc + " %",  // 1: padded field starts with the whole word
		"% " + esc + " %", // 2: padded field contains the whole word
		esc + "%",         // 3: bare field starts with it
		"% " + esc + "%",  // 4: padded field has it starting a word
		"%" + esc + "%",   // 5: bare field contains it
	}
}

// searchRankArgs binds searchRankPatterns once per column, in the order the
// placeholders appear: every column of tier 0, then every column of tier 1, and
// so on.
func searchRankArgs(folded string, columns int) []any {
	patterns := searchRankPatterns(folded)
	args := make([]any, 0, len(patterns)*columns)
	for _, p := range patterns {
		for i := 0; i < columns; i++ {
			args = append(args, p)
		}
	}
	return args
}

// searchRankClause builds the CASE expression that scores a row against the
// given key columns, followed by a trailing comma so callers can prepend it to
// their own ORDER BY. Column expressions come from this package, never from a
// request; only the patterns are bound.
//
// A row is ranked by its best column: an author matched on their own name and
// an author matched on a pen name reach the same tier, because to the person
// searching they are the same person under a name they used.
func searchRankClause(columns ...string) string {
	// tierTest reports how tier i probes one column.
	tierTest := func(tier int, col string) string {
		switch tier {
		case 0:
			return col + " = ?"
		case 1, 2, 4:
			return "(' ' || " + col + " || ' ') LIKE ? ESCAPE '\\'"
		default:
			return col + " LIKE ? ESCAPE '\\'"
		}
	}

	var b strings.Builder
	b.WriteString("CASE")
	for tier := 0; tier < searchRankTiers; tier++ {
		tests := make([]string, 0, len(columns))
		for _, col := range columns {
			tests = append(tests, tierTest(tier, col))
		}
		b.WriteString(" WHEN ")
		b.WriteString(strings.Join(tests, " OR "))
		b.WriteString(" THEN ")
		b.WriteString(strconv.Itoa(tier))
	}
	b.WriteString(" ELSE ")
	b.WriteString(strconv.Itoa(searchRankTiers))
	b.WriteString(" END, ")
	return b.String()
}
