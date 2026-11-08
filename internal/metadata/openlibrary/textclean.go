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

// trimUnmatchedQuote drops a double quote that opens or closes a description
// without a partner, the stray leading quote some OpenLibrary records carry
// (Golden Son's starts with one). A description that is quoted as a whole, or
// that quotes something inside it, is balanced and comes back unchanged.
//
// Straight quotes have no direction, so they are judged by count: an odd
// number means one is unpaired, and it is taken off only when it sits at one
// end and the other end has none, which is the only case where it is clear
// which quote is the stray one. Curly quotes say which way they face, so a
// leading opener is dropped when openers outnumber closers, and a trailing
// closer when closers outnumber openers.
func trimUnmatchedQuote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if strings.Count(s, `"`)%2 == 1 {
		starts, ends := strings.HasPrefix(s, `"`), strings.HasSuffix(s, `"`)
		switch {
		case starts && !ends:
			s = strings.TrimSpace(s[1:])
		case ends && !starts:
			s = strings.TrimSpace(s[:len(s)-1])
		}
	}
	opens, closes := strings.Count(s, "“"), strings.Count(s, "”")
	switch {
	case opens > closes && strings.HasPrefix(s, "“"):
		s = strings.TrimSpace(strings.TrimPrefix(s, "“"))
	case closes > opens && strings.HasSuffix(s, "”"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "”"))
	}
	return s
}
