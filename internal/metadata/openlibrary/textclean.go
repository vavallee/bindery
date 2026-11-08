package openlibrary

import (
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
)

// olTitle tidies a work title as OpenLibrary returns it. Records waiting in
// OpenLibrary's merge queue carry a "(Duplicate of OL...)" note on the end of
// the title, which would otherwise become the book's name in the library.
func olTitle(raw string) string {
	return textutil.StripOpenLibraryDuplicateMarker(raw)
}

// olDisplayTitle is the title a book is created under: the merge queue's
// duplicate note comes off first, then the audio format note, so
// "X [Audiobook] (Duplicate of OL1W)" becomes "X". Author sync keeps using
// olTitle until its noise filter has seen the "(Audio CD)" form.
func olDisplayTitle(raw string) string {
	return stripFormatMarker(olTitle(raw))
}

// trimUnmatchedQuote drops a double quote that opens or closes a description
// without a partner, the stray leading quote some OpenLibrary records carry
// (Golden Son's starts with one).
//
// It only acts when the quote is the very first or very last character and is
// the only quote of its kind in the text: the only straight quote, or the only
// curly one. Any other quote makes the pairing a guess, so the text is left
// alone. That keeps a balanced description, a line of dialogue like
// "Hello," she said. Then "bye, and the multi paragraph convention where each
// quoted paragraph opens with “ and only the last one closes. A description
// that is nothing but the quote is kept as it is.
func trimUnmatchedQuote(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 2 {
		return s
	}
	for _, kind := range [][]string{{`"`}, {"“", "”"}} {
		n := 0
		for _, q := range kind {
			n += strings.Count(s, q)
		}
		if n != 1 {
			continue
		}
		for _, q := range kind {
			switch {
			case strings.HasPrefix(s, q):
				return strings.TrimSpace(strings.TrimPrefix(s, q))
			case strings.HasSuffix(s, q):
				return strings.TrimSpace(strings.TrimSuffix(s, q))
			}
		}
	}
	return s
}
