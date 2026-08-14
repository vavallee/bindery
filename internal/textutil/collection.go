package textutil

import "strings"

// collectionTitleKeywords are lowercase substrings that mark a title as
// describing a box set, omnibus, anthology, or other multi-work bundle
// rather than a single book. Shared between the recommender (don't surface
// a bundle as a suggestion) and metadata author-work ingestion (don't
// catalog one as a book, #1780) so both consult one list instead of two
// that can drift apart.
var collectionTitleKeywords = []string{
	"complete", "collected", "omnibus", "boxed set", "box set", "anthology",
	"the best of", "stories of", "tales of", "(omnibus)", "(collection)",
	"complete works", "complete collection",
}

// LooksLikeCollectionTitle reports whether title appears to describe a box
// set, omnibus, anthology, or other multi-work collection rather than a
// single book. The check is intentionally broad: some false positives
// (e.g. "Complete Guide to Go Programming") are accepted to keep the logic
// simple, matching the tolerance already established for the recommender's
// use of this list.
func LooksLikeCollectionTitle(title string) bool {
	lower := strings.ToLower(title)
	for _, kw := range collectionTitleKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
