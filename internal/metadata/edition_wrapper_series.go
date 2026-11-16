package metadata

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
)

// Edition wrapper series (#3317).
//
// Hardcover files some editions as a series of one: an omnibus, a boxed set, a
// coloring book or a single comic issue gets a series ref of its own, or a ref
// to the real franchise series, and every book carrying a ref becomes a local
// series row. The row is not a series the user holds, and it cannot be cleaned
// up by hand: deleting a series is not durable (#2554), because a provider id
// with no alias row is recreated by the next sync.
//
// The rule therefore runs where the ref is built, before any caller can turn
// it into a row. A ref that is dropped is simply never seen again, so a
// re-sync cannot undo it, no schema changes, and a series the user creates or
// links by hand is untouched (those paths never read the ref).
//
// Two signals identify a wrapper, both deliberately conservative: a false
// negative leaves a series card the user already had, a false positive hides a
// real series' membership.

// wrapperSeriesTitleRe matches series TITLES that name an edition rather than
// a series. Unlike the box-set words in unambiguousBundleTitleRe, these only
// make sense on a title Hardcover gave to a series.
//
// A bare "Collection", "Complete" or "Volume" is never enough: "The Collection"
// and "The Complete Chronicles of Conan" are real series names, and "Volume"
// appears inside real ones. "Volume N" counts only as the last words of the
// title, the shape of an edition ("Storm Front Volume II"), never mid title.
var wrapperSeriesTitleRe = regexp.MustCompile(`(?i)` +
	`\bsingle\s+issues?\b` +
	`|\bcomplete\s+collection\b` +
	`|\bcolou?ring\s+book\b` +
	`|\bomnibus\s*(?:edition\s*)?\)?\s*$` +
	`|\(\s*omnibus\s*\)` +
	`|[\s,:(-]\s*(?:volume|vol\.?)\s*(?:\d{1,3}|[ivx]{1,5})\s*\)?\s*$`)

// IsEditionWrapperSeriesTitle reports whether a series title names an edition
// (box set, omnibus, complete collection, coloring book, single issues, or a
// trailing "Volume N") rather than a series. Real names with parentheses such
// as "Discworld (City Watch)" do not match.
func IsEditionWrapperSeriesTitle(title string) bool {
	return unambiguousBundleTitleRe.MatchString(title) || wrapperSeriesTitleRe.MatchString(title)
}

// wrapperWords is every word a wrapper edition adds to the series name it
// wraps. A book is a wrapper only when, after taking the series name out of
// its title, nothing else is left.
var wrapperWords = map[string]bool{
	"the": true, "a": true, "an": true, "complete": true, "collection": true,
	"collections": true, "collected": true, "trilogy": true, "omnibus": true,
	"series": true, "box": true, "boxed": true, "set": true, "coloring": true,
	"colouring": true, "book": true, "books": true, "volume": true, "vol": true,
	"edition": true, "and": true,
}

// strongWrapperWords are the words that make a book a wrapper. "The", "series"
// and "volume 2" alone describe plenty of real members.
var strongWrapperWords = map[string]bool{
	"complete": true, "collection": true, "collected": true, "trilogy": true,
	"omnibus": true, "box": true, "boxed": true, "set": true, "coloring": true,
	"colouring": true,
}

var romanVolumes = map[string]bool{
	"i": true, "ii": true, "iii": true, "iv": true, "v": true,
	"vi": true, "vii": true, "viii": true, "ix": true, "x": true,
}

func wrapperTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isVolumeNumber(tok string) bool {
	if romanVolumes[tok] {
		return true
	}
	if tok == "" || len(tok) > 3 {
		return false
	}
	for _, r := range tok {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// IsEditionWrapperBook reports whether bookTitle is the series' name plus
// nothing but edition words: "The Liveship Traders Trilogy" for "The Liveship
// Traders", "Maze Runner Complete Collection" for "The Maze Runner", "The
// Complete Sherlock Holmes, Volume II" for "Sherlock Holmes", "Legends of the
// Dragonrealm, Vol." for the series of that name. The series name must appear
// whole in the book title and at least one strong wrapper word must remain, so
// "The Hollows, Vol. 3" (volume number only), "Foo: Book One of the Foo
// Trilogy" ("one", "of" are not wrapper words) and "Sherlock Holmes: The
// Complete Novels" are real members and stay.
func IsEditionWrapperBook(seriesTitle, bookTitle string) bool {
	series := wrapperTokens(leadingArticleRe.ReplaceAllString(strings.TrimSpace(seriesTitle), ""))
	book := wrapperTokens(bookTitle)
	if len(series) == 0 || len(book) <= len(series) {
		return false
	}
	at := -1
	for i := 0; i+len(series) <= len(book) && at < 0; i++ {
		match := true
		for j, t := range series {
			if book[i+j] != t {
				match = false
				break
			}
		}
		if match {
			at = i
		}
	}
	if at < 0 {
		return false
	}
	rest := append(append([]string{}, book[:at]...), book[at+len(series):]...)
	strong := false
	for i, tok := range rest {
		switch {
		case strongWrapperWords[tok]:
			strong = true
		case tok == "vol" || tok == "volume":
			// A bare trailing "Vol." (no number) is the malformed edition
			// shape; "Vol. 3" is an ordinary numbered member.
			if i == len(rest)-1 {
				strong = true
			}
		case wrapperWords[tok]:
		case isVolumeNumber(tok) && i > 0 && (rest[i-1] == "vol" || rest[i-1] == "volume"):
		default:
			return false
		}
	}
	return strong
}

// IsEditionWrapperSeriesRef reports whether the series ref a provider attached
// to a book describes an edition wrapper rather than a series the book belongs
// to: the ref's title names an edition, or the book is a wrapper of the series.
func IsEditionWrapperSeriesRef(ref models.SeriesRef, bookTitle string) bool {
	return IsEditionWrapperSeriesTitle(ref.Title) || IsEditionWrapperBook(ref.Title, bookTitle)
}

// DropEditionWrapperSeriesRefs returns refs without the edition wrappers (see
// IsEditionWrapperSeriesRef). If the Primary ref was dropped, the first
// remaining ref becomes Primary so a book keeps a primary series when it still
// has a real one.
func DropEditionWrapperSeriesRefs(bookTitle string, refs []models.SeriesRef) []models.SeriesRef {
	if len(refs) == 0 {
		return refs
	}
	kept := make([]models.SeriesRef, 0, len(refs))
	hadPrimary, keptPrimary := false, false
	for _, ref := range refs {
		if ref.Primary {
			hadPrimary = true
		}
		if IsEditionWrapperSeriesRef(ref, bookTitle) {
			continue
		}
		if ref.Primary {
			keptPrimary = true
		}
		kept = append(kept, ref)
	}
	if len(kept) == len(refs) {
		return refs
	}
	if len(kept) == 0 {
		return nil
	}
	if hadPrimary && !keptPrimary {
		kept[0].Primary = true
	}
	return kept
}
