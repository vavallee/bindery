package db

import (
	"path/filepath"
	"strings"
)

// ebookFormatPreference is the one order Bindery uses to choose between
// several ebook files of the same book: the file a book renders (its
// ebookFilePath, which OPDS and downloads serve, #3467) and the file that
// makes the Calibre record (#2832). EPUB is the most widely readable, so it
// leads; anything not listed comes after, by extension.
var ebookFormatPreference = map[string]int{
	"epub":  0,
	"kepub": 1,
	"azw3":  2,
	"mobi":  3,
	"pdf":   4,
}

// EbookFormatOf is the format name the preference order uses for path: the
// extension, lower case, with a Kobo ".kepub.epub" read as kepub.
func EbookFormatOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, ".kepub.epub") {
		return "kepub"
	}
	return strings.TrimPrefix(filepath.Ext(base), ".")
}

// EbookFormatRank places path in the preference order, lower is better.
// Unlisted formats share the rank after the last listed one.
func EbookFormatRank(path string) int {
	if r, ok := ebookFormatPreference[EbookFormatOf(path)]; ok {
		return r
	}
	return len(ebookFormatPreference)
}

// EbookFormatLess orders two paths by format preference, then by extension
// among the unlisted formats. Paths of the same format compare equal, so a
// stable sort keeps their existing order.
func EbookFormatLess(a, b string) bool {
	ra, rb := EbookFormatRank(a), EbookFormatRank(b)
	if ra != rb {
		return ra < rb
	}
	return EbookFormatOf(a) < EbookFormatOf(b)
}
