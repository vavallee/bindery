package seriesmatch

import (
	"math"
	"slices"
	"strings"
)

// This file is a first-party implementation of the four FuzzyWuzzy similarity
// ratios TitleScore combines. It replaces github.com/creditx/go-fuzzywuzzy,
// which is GPL-3.0 and so could not be statically linked into the MIT-licensed
// binaries we publish (#1988).
//
// The formulas are the published FuzzyWuzzy definitions, written here from the
// algorithm rather than ported from any GPL source:
//
//   - ratio(a, b) = 100 * (|a| + |b| - d(a, b)) / (|a| + |b|), where d is the
//     Levenshtein distance with insertion and deletion weighted 1 and
//     substitution weighted 2. Equivalently 100 * 2M/T, M being the number of
//     characters that align in an optimal edit script. The doubled
//     substitution cost is what makes the two forms equal, and is the reason a
//     generic Levenshtein library cannot be dropped in here: the usual
//     1 - d/max(|a|,|b|) similarity is a different number entirely.
//   - partialRatio(a, b) = the best ratio between the shorter string and any
//     equal-length window of the longer one. A shorter string contained
//     verbatim in the longer one therefore scores 100, which several callers
//     and the omnibus/boxed-set matching work depend on.
//   - tokenSortRatio: ratio over whitespace tokens sorted and rejoined.
//   - tokenSetRatio: tokens become sets; the max ratio among the sorted
//     intersection and the two "intersection + own remainder" strings.
//
// Scores are integers in [0, 100].

// ratio scores two strings by weighted Levenshtein distance.
func ratio(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	total := len(ra) + len(rb)
	if total == 0 {
		return 0
	}
	return int(math.RoundToEven(100 * float64(total-substitutionWeightedDistance(ra, rb)) / float64(total)))
}

// partialRatio scores the shorter string against the best-matching window of
// the longer one, so that a fully contained substring scores 100.
func partialRatio(a, b string) int {
	shorter, longer := []rune(a), []rune(b)
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) == 0 {
		return 0
	}

	// Every window is scanned rather than only those anchored on a matching
	// block. Titles are short, so the cost is negligible, and the exhaustive
	// scan is by definition never worse than an anchored search at finding the
	// best window.
	best := 0.0
	for start := 0; start+len(shorter) <= len(longer); start++ {
		window := longer[start : start+len(shorter)]
		total := 2 * len(shorter)
		score := float64(total-substitutionWeightedDistance(shorter, window)) / float64(total)
		if score > 0.995 {
			return 100
		}
		if score > best {
			best = score
		}
	}
	return int(math.Round(100 * best))
}

// tokenSortRatio compares the strings with their whitespace tokens sorted, so
// that word order stops mattering.
func tokenSortRatio(a, b string) int {
	return ratio(sortTokens(a), sortTokens(b))
}

func sortTokens(s string) string {
	tokens := strings.Fields(s)
	slices.Sort(tokens)
	return strings.Join(tokens, " ")
}

// tokenSetRatio compares the strings as token sets, so that a title which is
// another title plus extra words can still score highly.
func tokenSetRatio(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	left := uniqueTokens(a)
	right := uniqueTokens(b)

	var shared, leftOnly, rightOnly []string
	for _, token := range left {
		if slices.Contains(right, token) {
			shared = append(shared, token)
		} else {
			leftOnly = append(leftOnly, token)
		}
	}
	for _, token := range right {
		if !slices.Contains(left, token) {
			rightOnly = append(rightOnly, token)
		}
	}
	slices.Sort(shared)
	slices.Sort(leftOnly)
	slices.Sort(rightOnly)

	intersection := strings.Join(shared, " ")
	withLeft := strings.TrimSpace(intersection + " " + strings.Join(leftOnly, " "))
	withRight := strings.TrimSpace(intersection + " " + strings.Join(rightOnly, " "))

	return max(
		ratio(intersection, withLeft),
		ratio(intersection, withRight),
		ratio(withLeft, withRight),
	)
}

func uniqueTokens(s string) []string {
	seen := make(map[string]struct{})
	var tokens []string
	for _, token := range strings.Fields(s) {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return tokens
}

// substitutionWeightedDistance is the Levenshtein distance between two rune
// slices with insertion and deletion costing 1 and substitution costing 2 —
// the weighting the FuzzyWuzzy ratio is defined over. A substitution priced at
// 2 is exactly an insertion plus a deletion, which is what lets the ratio be
// read as "share of characters that align".
func substitutionWeightedDistance(a, b []rune) int {
	// A common prefix and suffix can never be part of a cheaper edit script,
	// so trimming them shrinks the matrix without changing the result.
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Keep the inner loop over the shorter slice so the two rows stay small.
	if len(b) < len(a) {
		a, b = b, a
	}

	previous := make([]int, len(a)+1)
	current := make([]int, len(a)+1)
	for i := range previous {
		previous[i] = i
	}
	for j, rb := range b {
		current[0] = j + 1
		for i, ra := range a {
			deletion := previous[i+1] + 1
			insertion := current[i] + 1
			substitution := previous[i] + 2
			if ra == rb {
				substitution = previous[i]
			}
			current[i+1] = min(deletion, insertion, substitution)
		}
		previous, current = current, previous
	}
	return previous[len(a)]
}
