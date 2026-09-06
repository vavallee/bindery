package db

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/textutil"
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
	s = textutil.FoldNonDecomposableLatin(s)
	folded, _, err := transform.String(newAccentStripper(), s)
	if err != nil {
		// transform only errors on malformed input we can't normalize; fall
		// back to the lowercased+replaced form rather than dropping the row to
		// an empty key (which would sort it to the very top).
		return s
	}
	return folded
}

// authorSortKeyRev is the revision of the folder above. backfillAuthorSortKeys
// records it after rewriting authors.sort_key and name_sort_key, and skips its
// table scan while the stored value still matches, so the scan only runs on
// the boot after this function's output changes (#2346).
//
// BUMP THIS whenever a change to authorSortKey, newAccentStripper or
// textutil.FoldNonDecomposableLatin can produce a different key for the same
// name. Missing a bump leaves existing rows folded by the old rules, which is
// exactly the out-of-order Authors list #1347 was about. Bumping when nothing
// changed costs one extra table scan, so when in doubt, bump.
const authorSortKeyRev = 1

// newAccentStripper builds a transformer that decomposes runes (NFD), removes
// combining marks (Mn), then recomposes (NFC), folding precomposed accented
// letters to their base. Constructed PER CALL: transform.Chain returns a
// stateful transformer whose Transform mutates internal buffers, so a shared
// package-level instance panics under concurrent author writes (#1374). The
// three small allocations are noise next to the DB write that follows.
func newAccentStripper() transform.Transformer {
	return transform.Chain(
		norm.NFD,
		runes.Remove(runes.In(unicode.Mn)),
		norm.NFC,
	)
}

// The Latin letters NFD leaves intact (ø, ł, æ, ß, …) are folded by
// textutil.FoldNonDecomposableLatin, which is shared so this table and the
// author-identity fold cannot drift apart (#1648).

// bookSortKey derives the ordering key for the Books A–Z list from a book's
// sort_title, falling back to its title when sort_title was never populated
// (the metadata providers all set it, but the Calibre, Audiobookshelf and CSV
// import paths create rows without one).
//
// It is authorSortKey applied to a title rather than a name: the problem is
// identical (#1347), only the column differs. Ordering on the raw sort_title
// left any title beginning with a non-ASCII letter — "Ödland", "Ångström",
// "Łódź" — sorting after "Z", because COLLATE NOCASE folds ASCII and nothing
// else. Lossy and ORDERING-ONLY; the human-facing value remains sort_title.
func bookSortKey(sortTitle, title string) string {
	if s := strings.TrimSpace(sortTitle); s != "" {
		return authorSortKey(s)
	}
	return authorSortKey(title)
}

// bookSortKeyRev is the revision of bookSortKey, gating backfillBookSortKeys the
// way authorSortKeyRev gates the author pass (#2346). It is separate from
// authorSortKeyRev even though the two folders share an implementation today,
// so that a change aimed at one list cannot force a needless rescan of the
// other table — and so that whichever one is being changed is the one that has
// to be bumped.
//
// BUMP THIS whenever bookSortKey's output can change for the same title.
const bookSortKeyRev = 1
