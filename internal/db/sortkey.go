package db

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// authorSortKey derives an accent-folded, lowercased, BINARY-comparable key
// from an author's sort_name.
//
// SQLite's built-in NOCASE collation folds only ASCII A–Z, so #1312 (which
// added COLLATE NOCASE) still left any sort_name beginning with a non-ASCII
// letter — "Östergaard", "Łukasz", "Ángel", "Ørsted" — sorting after "Z",
// which users read as the A–Z list being out of order (#1347). We fold once on
// write into authors.sort_key and ORDER BY that column with a plain BINARY
// index (migration 058), so the ordering needs no Unicode-aware collation at
// query time.
//
// Folding strips combining marks via NFD decomposition (é→e, ö→o, ñ→n) and
// then maps the common Latin letters that do NOT decompose under NFD (ø, ł, æ,
// ß, þ, ð, đ, …) to an ASCII approximation, so a Scandinavian/Polish/German
// catalogue sorts in the expected place. The result is lowercased so case no
// longer interleaves. It is intentionally lossy and for ordering only — the
// human-facing value remains sort_name.
func authorSortKey(sortName string) string {
	s := strings.TrimSpace(sortName)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = nonDecomposableFolder.Replace(s)
	folded, _, err := transform.String(accentStripper, s)
	if err != nil {
		// transform only errors on malformed input we can't normalize; fall
		// back to the lowercased+replaced form rather than dropping the row to
		// an empty key (which would sort it to the very top).
		return s
	}
	return folded
}

// accentStripper decomposes runes (NFD) and removes combining marks (Mn), then
// recomposes (NFC). This folds precomposed accented letters to their base.
var accentStripper = transform.Chain(
	norm.NFD,
	runes.Remove(runes.In(unicode.Mn)),
	norm.NFC,
)

// nonDecomposableFolder handles the Latin letters NFD leaves intact because
// they are atomic code points, not base+combining-mark compositions. Applied
// to already-lowercased input. Order/uppercase variants are unnecessary since
// authorSortKey lowercases first.
var nonDecomposableFolder = strings.NewReplacer(
	"ø", "o",
	"ł", "l",
	"æ", "ae",
	"œ", "oe",
	"ß", "ss",
	"þ", "th",
	"ð", "d",
	"đ", "d",
	"ħ", "h",
	"ı", "i",
	"ŀ", "l",
)
