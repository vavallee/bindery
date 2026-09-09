package indexer

import (
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// titleIdentityWords keeps numbers as well as significant words: dropping "12"
// lets "12 More Rules for Life" satisfy "12 Rules for Life".
func titleIdentityWords(s string) []string {
	var words []string
	for _, word := range strings.Fields(NormalizeRelease(s)) {
		if isAllDigits(word) || len(newznab.SigWords(word)) != 0 {
			words = append(words, word)
		}
	}
	return words
}

// conflictingTitleAuthor recognises explicit trailing attribution only when
// the left side names the requested title. Title-only releases remain valid;
// release metadata and narrator credits are not treated as authors.
func conflictingTitleAuthor(release, title string, authorSets [][]string) bool {
	normalizedTitle := NormalizeRelease(title)
	normalizedRelease := NormalizeRelease(release)
	var attribution string
	if after, ok := strings.CutPrefix(normalizedRelease, normalizedTitle+" by "); ok {
		attribution = after
	} else {
		for _, separator := range []string{" - ", " – ", " — "} {
			before, after, ok := strings.Cut(release, separator)
			if ok && NormalizeRelease(before) == normalizedTitle {
				attribution = NormalizeRelease(after)
				break
			}
		}
	}
	if attribution == "" {
		return false
	}
	words := newznab.SigWords(attribution)
	if len(words) == 0 || benignTrailingToken(words[0], nil) {
		return false
	}
	knownAuthor := false
	for _, tokens := range authorSets {
		if len(tokens) == 0 {
			continue
		}
		knownAuthor = true
		if authorMatchesRelease(attribution, tokens) {
			return false
		}
		// A surname-only credit is not evidence of a conflicting author.
		if words[0] == tokens[len(tokens)-1] && (len(words) == 1 || benignTrailingToken(words[1], nil)) {
			return false
		}
	}
	return knownAuthor
}
