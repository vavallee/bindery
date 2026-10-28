package metadata

import (
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
)

// GoogleBooksAuthorPrefix starts the synthetic author ID Bindery makes for a
// Google Books result when no other provider knows the author. Google Books
// has no author IDs and no author endpoint, so the ID is only a stable key
// built from the name, the same idea as DNB's "dnb:author:<slug>" (#608).
const GoogleBooksAuthorPrefix = "gb:author:"

// SyntheticGoogleBooksAuthorID builds the synthetic author ID for name. It
// returns "" when the name has nothing usable in it (empty, or only
// punctuation), so the caller can refuse instead of keying every such author
// to one shared ID. The same name always gives the same ID, which is what lets
// a second book by the same author reuse the row.
func SyntheticGoogleBooksAuthorID(name string) string {
	slug := textutil.AuthorSlug(name)
	if slug == "" {
		return ""
	}
	return GoogleBooksAuthorPrefix + slug
}

// IsSyntheticAuthorID reports whether foreignID is a Bindery made Google Books
// author ID. No provider can look one up or list its works, so the aggregator
// answers for it directly instead of dispatching to the Google Books client.
func IsSyntheticAuthorID(foreignID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(foreignID)), GoogleBooksAuthorPrefix)
}
