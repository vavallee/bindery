package indexer

import (
	"regexp"
	"strings"
)

// containsInOrder reports whether every word in seq appears in haystack in the
// given order, allowing arbitrary intervening text. Unlike ContainsPhrase
// (which requires the words contiguous) it tolerates gaps; unlike a plain
// all-present check it rejects REORDERED matches — so "Secrets of the Human
// Body" does not satisfy the ["body","secrets"] sequence, but "The Lord of the
// Rings" does satisfy ["lord","rings"].
func containsInOrder(haystack string, seq []string) bool {
	if len(seq) == 0 {
		return true
	}
	return inOrderRegex(seq).MatchString(haystack)
}

// inOrderRegex returns the cached in-order-sequence regex for seq. Shared with
// matchAnchored (searcher.go), which needs the leftmost match position to
// inspect what precedes the sequence.
func inOrderRegex(seq []string) *regexp.Regexp {
	parts := make([]string, len(seq))
	for i, w := range seq {
		parts[i] = umlautFlexRegex(regexp.QuoteMeta(strings.ToLower(w)))
	}
	// wordSep rather than \b so non-ASCII words match (#1642). The gap between
	// consecutive words is `SEP(?:.*SEP)?` rather than `.*` so that ADJACENT
	// words still match: the separator after each word is consumed, leaving no
	// separator for the next word to assert on, which a bare `.*` would break.
	gap := wordSep + `(?:.*` + wordSep + `)?`
	pattern := `(?i)(?:^|` + wordSep + `)` + strings.Join(parts, gap) + `(?:` + wordSep + `|$)`
	re, _ := regexCache.LoadOrStore(pattern, regexp.MustCompile(pattern))
	return re.(*regexp.Regexp)
}
