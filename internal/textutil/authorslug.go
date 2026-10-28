package textutil

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// AuthorSlug reduces an author name to the identifier tail of a synthetic
// author foreign ID ("dnb:author:<slug>", "gb:author:<slug>"). The result is a
// stored foreign ID, so it must never change for a name that already has one.
// It was lifted out of the DNB client unchanged so a second provider can share
// it. An empty result means the name has nothing usable in it and the caller
// must not build an ID from it.
//
// ASCII names get the lowercase, hyphen separated reduction. A name that
// reduction cannot represent at all (Chinese, Cyrillic, Greek, Hebrew, Arabic)
// falls back to a script preserving form, so two scripts never share one key.
func AuthorSlug(name string) string {
	if out := asciiSlug(name); out != "" {
		return out
	}
	return nonLatinSlug(name)
}

// asciiSlug is the original reduction, preserved verbatim. See AuthorSlug.
func asciiSlug(name string) string {
	folded := norm.NFD.String(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(folded))
	prevDash := false
	for _, r := range folded {
		switch {
		case unicode.Is(unicode.Mn, r):
			// Combining mark from NFD decomposition: drop it, keeping the base letter.
			continue
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevDash = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "-")
	return out
}

// nonLatinSlug derives an identifier for a name the ASCII reduction cannot
// represent at all. It keeps the script rather than transliterating: a
// romanisation would have to pick one scheme per script, and picking wrongly
// merges two people instead of merely writing an ugly ID.
//
// Marks are kept as word characters because in these scripts they are part of
// the letter, the point FoldForSlug makes at length. An empty result is still
// possible in principle (a name of nothing but punctuation), and the caller
// treats that the way it always has.
func nonLatinSlug(name string) string {
	folded := FoldForSlug(name)
	var b strings.Builder
	b.Grow(len(folded))
	prevDash := false
	for _, r := range folded {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
