// Package isbnutil normalizes the book identifiers Bindery stores and compares
// — ISBNs and Amazon/Audible ASINs — so that the same book carries the same
// identifier string whichever ingest path recorded it.
//
// Before this package was the single owner, an ISBN arriving in an EPUB's OPF,
// in a Deutsche Nationalbibliothek MARC record and in an Audiobookshelf library
// item each went through a separately written scanner, and an ASIN was
// upper-cased by every metadata provider but not by the Audiobookshelf import.
package isbnutil

import (
	"strings"
	"unicode"
)

// NormalizeASINRev is the revision of NormalizeASIN's output. Bump it whenever
// a change here would give an already-stored ASIN a different value; the
// startup backfill gated on this marker then re-normalizes existing rows. See
// runBackfillOnce in internal/db.
const NormalizeASINRev = 1

// Normalize strips common ISBN separators and uppercases ISBN-10 check digits.
// It intentionally leaves other characters alone so invalid inputs still fail
// at the provider instead of being silently rewritten into a different value.
//
// The separator set covers the Unicode dash class as well as the ASCII hyphen:
// an ISBN copied out of a PDF, a publisher's web page or a word processor
// routinely carries an en dash or a non-breaking hyphen instead, and treating
// those as part of the identifier turns a valid lookup into a miss.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteByte('X')
		case isSeparator(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeASIN trims and upper-cases an Amazon/Audible ASIN. ASINs are
// case-insensitive on Amazon's side but are stored and compared here as exact
// strings (importer.Lookup matches a filename's ASIN against books.asin with
// ==), so a single canonical casing is what makes those comparisons work.
func NormalizeASIN(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// ValidISBN10 reports whether raw is a well-formed ISBN-10 whose check digit
// verifies: nine digits followed by a digit or 'X', with the sum of each digit
// times its position weight (10 down to 1) divisible by 11. raw is normalized
// first, so hyphenated and spaced forms are accepted.
func ValidISBN10(raw string) bool {
	s := Normalize(raw)
	if len(s) != 10 || !allDigits(s[:9]) {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(s[i]-'0') * (10 - i)
	}
	switch c := s[9]; {
	case c == 'X':
		sum += 10
	case c >= '0' && c <= '9':
		sum += int(c - '0')
	default:
		return false
	}
	return sum%11 == 0
}

// ValidISBN13 reports whether raw is a well-formed ISBN-13 whose check digit
// verifies: thirteen digits behind a 978 or 979 Bookland prefix, with the
// EAN-13 alternating 1/3 weighting summing to a multiple of 10. raw is
// normalized first, so hyphenated and spaced forms are accepted.
//
// The prefix is part of the check: an EAN-13 outside 978/979 is some other
// product, not a book, and accepting one would let a barcode scanned off the
// back of a box become a book identifier.
func ValidISBN13(raw string) bool {
	s := Normalize(raw)
	if len(s) != 13 || !allDigits(s) {
		return false
	}
	if !strings.HasPrefix(s, "978") && !strings.HasPrefix(s, "979") {
		return false
	}
	return s[12] == '0'+ean13CheckDigit(s[:12])
}

// ToISBN13 normalizes raw and returns it in 13-digit ISBN-13 form, converting
// an ISBN-10 by prefixing "978" and recomputing the EAN-13 check digit. It
// returns "" for anything that is not a check-digit-valid ISBN-10 or ISBN-13,
// so callers can treat "" as "no usable ISBN" rather than having to re-validate.
//
// Bindery needs this because the two sides of an ISBN comparison can be
// recorded in different forms: a release name only ever yields an ISBN-13
// (indexer.ParseRelease requires a 978/979 prefix), while a catalogue edition
// may carry only isbn_10.
//
// The check digit is verified rather than assumed. Converting an ISBN-10 keeps
// only the nine-digit registrant core and computes a fresh ISBN-13 check digit,
// which means a mistyped or OCR-mangled core used to come back out as a
// perfectly well-formed ISBN-13 that pointed at a different book — the one
// shape of corruption an identifier must never have. Ingest stays permissive
// (see Extract); the tightening is here, at the point where an ISBN becomes a
// search criterion.
func ToISBN13(raw string) string {
	s := Normalize(raw)
	switch len(s) {
	case 13:
		if !ValidISBN13(s) {
			return ""
		}
		return s
	case 10:
		if !ValidISBN10(s) {
			return ""
		}
		// The ISBN-10 check digit is discarded (it may be 'X', which has no
		// place in an ISBN-13); only the 9-digit registrant core carries over.
		body := "978" + s[:9]
		return body + string('0'+ean13CheckDigit(body))
	default:
		return ""
	}
}

// Extract pulls the first ISBN-shaped token out of a free-form string such as
// an EPUB dc:identifier ("urn:isbn:9780345472199") or a MARC 020 $a value
// ("9783499015717 (pbk.)"). It returns (isbn13, isbn10); at most one is
// non-empty, and both are "" when nothing in raw is shaped like an ISBN.
//
// It scans for a run of ISBN characters (digits and the ISBN-10 'X' check
// digit) that may have separators inside it, then accepts the run only if it is
// a whole token — a run immediately followed by a letter is the digit part of a
// longer word, such as the hex of a "urn:uuid:…1234567890ab", and is skipped
// rather than mistaken for an ISBN-10.
//
// Extract deliberately checks shape and not the check digit. Publisher OPF
// files and library MARC records really do carry ISBNs with a wrong check
// digit, and dropping those at ingest would lose an identifier that is still
// the best evidence available for which book a file is. Validation happens
// later, in ToISBN13, where a wrong ISBN would go on to match the wrong
// release.
func Extract(raw string) (isbn13, isbn10 string) {
	runes := []rune(raw)
	for i := 0; i < len(runes); i++ {
		if !isISBNChar(runes[i]) {
			continue
		}
		token, last, end := scanISBNToken(runes, i)
		glued := end == last+1 && end < len(runes) && unicode.IsLetter(runes[end])
		if !glued {
			switch {
			case len(token) == 13 && allDigits(token) &&
				(strings.HasPrefix(token, "978") || strings.HasPrefix(token, "979")):
				return token, ""
			case len(token) == 10 && allDigits(token[:9]):
				return "", token
			}
		}
		i = end - 1 // the loop's i++ then resumes just past the token
	}
	return "", ""
}

// scanISBNToken reads one run of ISBN characters starting at start, skipping
// separators that appear inside it. It returns the collected token, the index
// of the last ISBN character consumed, and the index one past the run.
func scanISBNToken(runes []rune, start int) (token string, last, end int) {
	var b strings.Builder
	last = start
	end = start
	for ; end < len(runes); end++ {
		r := runes[end]
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			last = end
		case r == 'x' || r == 'X':
			b.WriteByte('X')
			last = end
		case isSeparator(r):
			// A separator may sit between the parts of a written ISBN; it ends
			// the run only if no ISBN character follows before a real
			// terminator, which the caller's glued check accounts for.
		default:
			return b.String(), last, end
		}
	}
	return b.String(), last, end
}

func isISBNChar(r rune) bool {
	return (r >= '0' && r <= '9') || r == 'x' || r == 'X'
}

// isSeparator reports whether r may appear between the parts of a written ISBN
// without belonging to it. Besides the ASCII hyphen and underscore and any
// Unicode space, this is the dash class a catalogue, word processor or OS
// keyboard substitutes for a hyphen. internal/importer's dashNormalizer holds
// the same list for filenames and web/src/api/booklookup.ts for what a user
// pastes into the search box; the three must agree or an ISBN normalizes
// differently depending on where it entered.
// The dashes are written as escapes rather than as literals: U+00AD is
// invisible in an editor and the rest are hard to tell apart on sight.
func isSeparator(r rune) bool {
	switch r {
	case '-', '_',
		'\u00ad', // soft hyphen
		'\u2010', // hyphen
		'\u2011', // non-breaking hyphen
		'\u2012', // figure dash
		'\u2013', // en dash
		'\u2014', // em dash
		'\u2015', // horizontal bar
		'\u2212': // minus sign
		return true
	}
	return unicode.IsSpace(r)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ean13CheckDigit computes the trailing check digit for the 12 leading digits
// of an EAN-13/ISBN-13: digits are weighted 1,3,1,3… and the check digit is
// whatever brings the weighted sum to a multiple of 10.
func ean13CheckDigit(body string) byte {
	sum := 0
	for i := 0; i < len(body); i++ {
		d := int(body[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return byte((10 - sum%10) % 10)
}
