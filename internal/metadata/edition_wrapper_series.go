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
	// Box set only as a trailing edition marker, optionally followed by a
	// number or a volume range ("Box Set 1: Volumes 1-23"). "The Boxed Set
	// Murders" is a title, not an edition.
	`|\bbox(?:ed)?\s*set\b(?:\s*[:,#-]?\s*(?:\d+|vol(?:ume)?s?\.?|-))*\s*\)?\s*$` +
	`|\(\s*boxed\s*\)` +
	`|\(\s*set\s*\)\s*$` +
	`|\bcollection\s*set\b` +
	`|\bcarton\s+of\s+\d+\s+signed\s+cop` +
	`|\b\d+\s*(?:books?|vol(?:ume)?s?)\s+set\b`)

// trailingVolumeSeriesRe matches a title that ends in a volume marker ("Storm
// Front Volume II"). On a series title that shape alone is not enough: "Fate/Zero
// Vol. 2", "Gundam: Volume 4" and "Star Wars: Darth Vader (2015) Volume 1" are
// names real series carry, so it only counts together with the book.
var trailingVolumeSeriesRe = regexp.MustCompile(`(?i)[\s,:(-]\s*(?:volume|vol\.?)\s*(?:\d{1,3}|[ivx]{1,5})\s*\)?\s*$`)

// IsEditionWrapperSeriesTitle reports whether a series title names an edition
// on its own (single issues, complete collection, coloring book, omnibus, a
// trailing box set). A trailing "Volume N" is not decided here, it needs the
// book. Real names with parentheses such as "Discworld (City Watch)" do not
// match.
func IsEditionWrapperSeriesTitle(title string) bool {
	return wrapperSeriesTitleRe.MatchString(title)
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
	// A series named by one generic word ("Stories") would swallow every
	// "Collected Stories" member, so the edition words do not apply to it.
	if len(series) == 1 && genericSeriesWords[series[0]] {
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
	boxSet := false
	for i := 0; i+1 < len(rest); i++ {
		if (rest[i] == "box" || rest[i] == "boxed") && rest[i+1] == "set" {
			boxSet = true
		}
	}
	strong := false
	for i, tok := range rest {
		switch {
		case tok == "omnibus":
			// A numbered omnibus ("Omnibus 2") is a volume of the real
			// series and stays in it; a bare "Omnibus" is the single
			// wrapper edition.
			if i+1 < len(rest) && isVolumeNumber(rest[i+1]) {
				return false
			}
			strong = true
		case strongWrapperWords[tok]:
			strong = true
		case tok == "vol" || tok == "volume":
			// A bare trailing "Vol." (no number) is the malformed edition
			// shape; "Vol. 3" is an ordinary numbered member.
			if i == len(rest)-1 {
				strong = true
			}
		case wrapperWords[tok]:
		case boxSet && (tok == "vols" || tok == "volumes" || isVolumeNumber(tok)):
			// "Box Set 1: Volumes 1-23" and "Vol. 3 Box Set".
		case isVolumeNumber(tok) && i > 0 && (rest[i-1] == "vol" || rest[i-1] == "volume"):
		default:
			return false
		}
	}
	return strong
}

// genericSeriesWords are series names too generic for the edition word rule.
var genericSeriesWords = map[string]bool{
	"stories": true, "novels": true, "books": true, "tales": true,
	"works": true, "essays": true, "poems": true, "writings": true,
	"fiction": true, "collection": true, "series": true,
}

// IsEditionWrapperSeriesRef reports whether the series ref a provider attached
// to a book describes an edition wrapper rather than a series the book belongs
// to: the ref's title names an edition, or the book is a wrapper of the series.
func IsEditionWrapperSeriesRef(ref models.SeriesRef, bookTitle string) bool {
	if IsEditionWrapperSeriesTitle(ref.Title) || IsEditionWrapperBook(ref.Title, bookTitle) {
		return true
	}
	return volumeNamedSeriesWrapsBook(ref.Title, bookTitle)
}

// volumeNamedSeriesWrapsBook handles a series title ending in a volume marker
// ("Dresden Files: Storm Front Volume II"). It is a wrapper only when the book
// is that same volume under a shorter name: the book's title minus its volume
// marker is a proper trailing part of the series title minus its marker
// ("Storm Front, Volume 2"). A book whose base name equals the series base
// ("Fate/Zero, Vol. 2" in "Fate/Zero Vol. 2") or that continues past it ("Star
// Wars: Darth Vader (2015) Volume 1") keeps its series.
func volumeNamedSeriesWrapsBook(seriesTitle, bookTitle string) bool {
	loc := trailingVolumeSeriesRe.FindStringIndex(seriesTitle)
	bloc := trailingVolumeSeriesRe.FindStringIndex(bookTitle)
	if loc == nil || bloc == nil {
		return false
	}
	sb := wrapperTokens(seriesTitle[:loc[0]])
	bb := wrapperTokens(bookTitle[:bloc[0]])
	if len(bb) == 0 || len(sb) <= len(bb) {
		return false
	}
	off := len(sb) - len(bb)
	for i, t := range bb {
		if sb[off+i] != t {
			return false
		}
	}
	return true
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
