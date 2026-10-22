package indexer

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Trailing title rules (#3268).
//
// The title identity gate (titleIdentityWords, ContainsPhrase) looks only at
// what sits BETWEEN the wanted title's words. It never reads what FOLLOWS
// them, so "Leigh Bardugo - King of Scars 02 - Rule of Wolves" was approved
// for "King of Scars": the title is there, whole, and the release is the next
// book in the series.
//
// These rules read the words after the wanted title and reject on POSITIVE
// evidence only. A false rejection hides a release the user wanted, so every
// signal below needs the release to say something specific, and anything
// unclear is kept.
//
// Signal A, a series number after the title. It applies ONLY when the wanted
// book's series position is known (it comes from the series data). The release
// has a number N of 2 to 99 directly after the title ("King of Scars 02",
// "[Rotten School 12]", "Series Book 3"), optionally behind marker words (book,
// bk, vol, volume, no, number, episode, tome, band, part, or a "#"), and N is
// outside the position (a stored range such as "1-3" counts as all its
// numbers). It rejects only when the number stands as a number and not as a
// count of something:
//
//   - the number ends the release name;
//   - or it is followed by punctuation (a dash, colon, comma, semicolon or a
//     bracket, or a period followed by a space);
//   - or it sits behind a marker word or a "#".
//
// A number followed by an ordinary word ("Title 12 files", "Title 4 mp3",
// "Title 24 bit", "Title 3 Tapes", "Title 2 vols") is a count and never
// rejects, whatever the word is. Neither does N of 1 ("Title 01" is the usual
// first book), "N of M" ("Part 2 of 2", "Book 2 of the Stormlight Archive"),
// a four digit year, or a number equal to the position.
//
// With no stored position the number is not judged on its own: no heuristic
// can tell "Title 02 - Rule of Wolves" from "Title 02 - Scott Brick" or
// "Title 02 - Dramatized". Such a release is caught only by signal B, through
// a library title after the number.
//
// Signal B, another of the author's titles after the title. When the caller
// supplies the titles of the author's other books in the library, a release
// whose remainder after the wanted title contains one of them as whole words
// is that other book. A known title BEFORE the wanted one is a series prefix
// ("Belgariad 2 - Queen of Sorcery") and is not read. Only titles of two or
// more significant words count, so a one word title ("Legion", "Wolves")
// cannot match the ordinary words of every release. Not read as "another
// title": a title that contains the wanted title ("Dune Messiah" for "Dune"),
// a title contained in the wanted title, a title that equals, contains or is
// contained in the wanted book's primary series title (releases name their
// series, "Title (Series #N)" and "Title - Series N", and the library often
// holds a series named entry), and a title that is itself a box set. The whole
// signal is off when the wanted title is itself a set (a set release lists its
// members).
//
// Both signals need to know where the wanted title ends in the release. They
// find it with the same phrase match the identity gate uses; a release that
// matched only through the unordered keyword fallback has no end to read from,
// and is left alone.

// FilterDebug reasons for the trailing title rules.
const (
	reasonAnotherTitle = "release names another title after this one"
	reasonLaterNumber  = "release names a different series number after this title"
)

// bookContext is what the library knows about the wanted book beyond its
// title and author. The zero value means nothing is known, and every rule that
// needs a fact stays silent without it.
type bookContext struct {
	// seriesPosition is the book's position in its primary series, as stored
	// ("3", "03", "1-3", "2.5", ""). Empty when it has none.
	seriesPosition string
	// seriesTitle is the title of the book's primary series, "" when it has
	// none.
	seriesTitle string
	// otherTitles are the titles of the author's other books in the library.
	otherTitles []string
}

// context is the book context carried by the search criteria.
func (c MatchCriteria) context() bookContext {
	return bookContext{seriesPosition: c.SeriesPosition, seriesTitle: c.SeriesTitle, otherTitles: c.AuthorTitles}
}

// SeriesReader reads a book's primary series: its title and the book's
// position in it. db.SeriesRepo.GetPrimarySeriesForBook is one.
type SeriesReader func(ctx context.Context, bookID int64) (seriesTitle, position string, err error)

// AuthorBooksReader lists an author's books. db.BookRepo.ListByAuthor is one.
type AuthorBooksReader func(ctx context.Context, authorID int64) ([]models.Book, error)

// SetLibraryContext fills the criteria's SeriesTitle, SeriesPosition and
// AuthorTitles from the library, for the relevance filter. Either reader may be
// nil (the repo was never attached) and then that part stays empty and the
// rules that need it stay quiet; a failing read does the same and is logged.
// It costs two reads per search, never one per release.
func (c *MatchCriteria) SetLibraryContext(ctx context.Context, book models.Book, series SeriesReader, books AuthorBooksReader) {
	if series != nil {
		if title, pos, err := series(ctx, book.ID); err != nil {
			slog.Warn("failed to load series for search", "book_id", book.ID, "error", err)
		} else {
			c.SeriesTitle, c.SeriesPosition = title, pos
		}
	}
	if books != nil {
		if bs, err := books(ctx, book.AuthorID); err != nil {
			slog.Warn("failed to load the author's titles for search", "author_id", book.AuthorID, "error", err)
		} else {
			c.AuthorTitles = AuthorTitlesExcluding(book.ID, bs)
		}
	}
}

// WarnLibraryContextMissing logs, once per owner, that the relevance filter's
// library context is only partly available because a repo was never attached.
// The filter then runs without the series rules, which is safe but weaker.
func WarnLibraryContextMissing(once *sync.Once, owner string, seriesMissing, booksMissing bool) {
	if !seriesMissing && !booksMissing {
		return
	}
	once.Do(func() {
		slog.Warn("search relevance filter is missing library context; series number and other title checks are weaker (#3268)",
			"owner", owner, "series_repo_missing", seriesMissing, "books_repo_missing", booksMissing)
	})
}

// relTok is one NormalizeRelease token of a release name, the raw word it came
// from (original case, for the roman numeral check) and the raw text that
// separated it from the token before it.
type relTok struct {
	text string
	raw  string
	sep  string
}

func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '\'' || r == rune(0x2019)
}

// tokenizeRelease splits raw into the tokens NormalizeRelease(raw) would
// produce, keeping the raw separator in front of each so punctuation can be
// told from a space. NormalizeRelease collapses every separator, which is right
// for matching and wrong for reading "02 - Rule of Wolves". ok is false when
// the two tokenisations disagree, in which case the caller must not read
// anything from the result.
func tokenizeRelease(raw string) ([]relTok, bool) {
	runes := []rune(norm.NFC.String(raw))
	var toks []relTok
	last := 0
	for i := 0; i < len(runes); {
		if !isTokenRune(runes[i]) {
			i++
			continue
		}
		j := i
		for j < len(runes) && isTokenRune(runes[j]) {
			j++
		}
		sep := string(runes[last:i])
		chunk := string(runes[i:j])
		for k, w := range strings.Fields(NormalizeRelease(chunk)) {
			if k > 0 {
				sep = " "
			}
			toks = append(toks, relTok{text: w, raw: chunk, sep: sep})
		}
		last, i = j, j
	}
	want := strings.Fields(NormalizeRelease(raw))
	if len(want) != len(toks) {
		return nil, false
	}
	for i := range want {
		if want[i] != toks[i].text {
			return nil, false
		}
	}
	return toks, true
}

// punctRunes are the characters that make a separator punctuation: hyphen, en
// dash, em dash, colon, comma, semicolon and brackets. Built from code points
// so the source carries no dash literally.
var punctRunes = string([]rune{'-', 0x2013, 0x2014, ':', ',', ';', '(', ')', '[', ']', '{', '}'})

// periodSpaceRe matches a period followed by white space in a separator: real
// sentence punctuation, unlike the bare dots that release names use as spaces.
var periodSpaceRe = regexp.MustCompile(`\.\s`)

// punctBefore reports whether the separator in front of tok contains
// punctuation. Spaces, underscores and bare dots are not punctuation.
func punctBefore(tok relTok) bool {
	return strings.ContainsAny(tok.sep, punctRunes) || periodSpaceRe.MatchString(tok.sep)
}

// seriesNumberMarkers are the words that may sit between a title and its
// series number: "Title Book 2", "Title Vol. 2", "Title Part 2".
var seriesNumberMarkers = map[string]bool{
	"book": true, "bk": true, "vol": true, "volume": true, "no": true,
	"number": true, "episode": true, "tome": true, "band": true, "part": true,
}

// setTitleRe finds a title that names a whole set rather than one book.
var setTitleRe = regexp.MustCompile(`(?i)\b(?:trilogy|tetralogy|quartet|quintet|saga|collection|omnibus|anthology|complete|boxed?\s*set|boxset|books?\s+\d+)\b`)

// isSetTitle reports whether title names a set of books.
func isSetTitle(title string) bool {
	return MultiBookPackMarker(title) != "" || setTitleRe.MatchString(title)
}

type otherTitle struct {
	ident []string
	// prefixed marks the suffix of an unnumbered, series prefixed library
	// title (#3382). A release that carries it straight after the wanted
	// title is that library title's own name, the same book written with its
	// subtitle, and is not another book.
	prefixed bool
	words    []string // ASCII identity words, for a cheap pre-check
}

// trailingRules holds the per search state of the trailing title rules.
type trailingRules struct {
	posLo, posHi int
	posKnown     bool
	// wantedEndsRoman is set when the wanted title itself ends in a capital
	// roman numeral ("Henry V", "Rocky II"): rule A then does not read
	// roman numerals after it.
	wantedEndsRoman bool
	others          []otherTitle
	// otherFull holds the identity words, joined, of every other library
	// title of any length. Part 2 asks whether the wanted book's main title
	// is itself another of the author's books.
	otherFull map[string]bool
	// siblingNums are the series numbers the library already gives to books
	// titled "<wanted title> <N> ..." (#3335). With no stored position of its
	// own, the wanted book cannot say which numbers are later volumes, but
	// these are known to be other books.
	siblingNums map[int]bool
}

// siblingTail reads a library title that is the wanted title followed by a
// series number and a further title ("Rotten School #12 - Battle of the Dum
// Diddys"). It returns the number and the title after it. ok is false for any
// other shape, so a title that merely contains the wanted one is untouched.
func siblingTail(cleaned string, wanted []string) (num int, tail string, ok bool) {
	if len(wanted) == 0 {
		return 0, "", false
	}
	norm := NormalizeRelease(cleaned)
	loc := phraseRegex(wanted).FindStringIndex(norm)
	if loc == nil || loc[0] != 0 {
		return 0, "", false
	}
	toks := strings.Fields(norm[loc[1]:])
	i := 0
	for i < len(toks) && seriesNumberMarkers[toks[i]] {
		i++
	}
	if i >= len(toks) || len(toks[i]) > 2 || !isAllDigits(toks[i]) {
		return 0, "", false
	}
	n, _ := strconv.Atoi(toks[i])
	if n < 2 {
		return 0, "", false
	}
	return n, strings.Join(toks[i+1:], " "), true
}

// editionWords are words that describe a printing and not a book. A suffix made
// only of them ("Large Print", "A Novel") names an edition of the wanted book.
var editionWords = map[string]bool{
	"illustrated": true, "annotated": true, "unabridged": true, "abridged": true,
	"revised": true, "expanded": true, "anniversary": true, "deluxe": true,
	"special": true, "collectors": true, "limited": true, "updated": true,
	"original": true, "classic": true, "novel": true, "novella": true, "audiobook": true,
	"ebook": true, "paperback": true, "hardcover": true, "large": true, "print": true,
	"reissue": true, "movie": true, "film": true, "tie": true, "new": true,
	"complete": true, "language": true,
}

// editionForms are the words for "edition" in the languages catalogues use.
// A suffix that starts or ends with one ("German Edition", "Edicion en
// Espanol", "Deutsche Ausgabe", "Norsk utgave") describes an edition.
var editionForms = map[string]bool{
	"edition": true, "editions": true, "edicion": true, "edizione": true, "ausgabe": true,
	"utgave": true, "udgave": true, "utgava": true, "upplaga": true, "editie": true,
	"edycja": true, "painos": true, "edisjon": true, "editora": true,
}

// editionLanguages are language names that start an edition suffix
// ("German Edition of ...", "Deutsche Ausgabe", "Espanol").
var editionLanguages = map[string]bool{
	"english": true, "german": true, "deutsche": true, "deutsch": true, "french": true,
	"francais": true, "spanish": true, "espanol": true, "italian": true, "italiano": true,
	"portuguese": true, "dutch": true, "nederlandse": true, "swedish": true, "svenska": true,
	"norwegian": true, "norsk": true, "danish": true, "dansk": true, "finnish": true,
	"polish": true, "russian": true, "japanese": true, "chinese": true, "korean": true,
}

// ordinalWords are the ordinals as words that start an anniversary or edition
// suffix ("Tenth Anniversary Edition").
var ordinalWords = map[string]bool{
	"first": true, "second": true, "third": true, "fourth": true, "fifth": true, "sixth": true,
	"seventh": true, "eighth": true, "ninth": true, "tenth": true, "eleventh": true,
	"twelfth": true, "fifteenth": true, "twentieth": true, "twenty": true, "thirtieth": true,
	"fortieth": true, "fiftieth": true, "hundredth": true,
}

var ordinalDigitsRe = regexp.MustCompile(`^\d+(?:st|nd|rd|th)$`)

// isEditionSuffix reports whether the normalised suffix words describe an
// edition of the wanted book and not another book. The test is structural: the
// suffix starts or ends with a word for "edition", starts with an ordinal or a
// language name, mentions an anniversary, or is made only of edition words.
func isEditionSuffix(raw []string) bool {
	words := make([]string, len(raw))
	for i, w := range raw {
		words[i] = stripMarks(w)
	}
	if len(words) == 0 {
		return false
	}
	first, last := words[0], words[len(words)-1]
	if editionForms[first] || editionForms[last] || editionLanguages[first] ||
		ordinalWords[first] || ordinalDigitsRe.MatchString(first) {
		return true
	}
	allEdition := true
	for _, w := range words {
		if w == "anniversary" {
			return true
		}
		if !editionWords[w] && len(newznab.SigWords(w)) != 0 {
			allEdition = false
		}
	}
	return allEdition
}

// prefixedSuffix reads a library title that is the wanted title, a punctuation
// separator and a further title, with no series number ("Rotten School - Punk'd
// and skunked", #3382). The suffix is the name of another book that the library
// writes with the series prefix. It returns the suffix as normalised words.
// ok is false for every other shape: no separator ("Dune Messiah"), a number
// or marker word right after the title (siblingTail reads those), a suffix
// made only of edition words, or a title that does not start with the wanted
// one.
func prefixedSuffix(cleaned string, wanted []string) (tail string, ok bool) {
	if len(wanted) == 0 {
		return "", false
	}
	norm := NormalizeRelease(cleaned)
	loc := phraseRegex(wanted).FindStringIndex(norm)
	if loc == nil || loc[0] != 0 {
		return "", false
	}
	k := len(strings.Fields(norm[:loc[1]]))
	toks, tokOK := tokenizeRelease(cleaned)
	if !tokOK || k >= len(toks) || !punctBefore(toks[k]) {
		return "", false
	}
	if first := toks[k].text; seriesNumberMarkers[first] || isAllDigits(first) {
		return "", false
	}
	words := make([]string, 0, len(toks)-k)
	for _, t := range toks[k:] {
		words = append(words, t.text)
	}
	if isEditionSuffix(words) {
		return "", false
	}
	tail = strings.Join(words, " ")
	return tail, true
}

// newTrailingRules prepares the rules for one wanted title. wanted are the
// identity word lists of every reading of the wanted title, used to keep a
// library title that contains the wanted one out of signal B.
func newTrailingRules(title string, bc bookContext, wanted ...[]string) *trailingRules {
	tr := &trailingRules{otherFull: map[string]bool{}, siblingNums: map[int]bool{}}
	tr.posLo, tr.posHi, tr.posKnown = parsePosition(bc.seriesPosition)
	if f := strings.Fields(title); len(f) > 0 {
		last := strings.Trim(f[len(f)-1], ".,:;!?()[]")
		tr.wantedEndsRoman = last != "" && last == strings.ToUpper(last) && romanRe.MatchString(last)
	}

	cleanedSeries := newznab.StripFormatQualifiers(newznab.NormalizeQueryTitle(bc.seriesTitle))
	seriesIdent := titleIdentityKeywords(cleanedSeries)
	seriesNorm := NormalizeRelease(cleanedSeries)
	// relatedToSeries reports whether v equals, contains or is contained in the
	// wanted book's primary series title.
	relatedToSeries := func(v string) bool {
		if len(seriesIdent) == 0 {
			return false
		}
		vi := titleIdentityKeywords(v)
		return ContainsPhrase(NormalizeRelease(v), seriesIdent) || (len(vi) > 0 && ContainsPhrase(seriesNorm, vi))
	}

	for _, raw := range bc.otherTitles {
		cleaned := newznab.StripFormatQualifiers(newznab.NormalizeQueryTitle(raw))
		if relatedToSeries(cleaned) {
			continue
		}
		if ident := titleIdentityKeywords(cleaned); len(ident) > 0 {
			tr.otherFull[strings.Join(ident, " ")] = true
		}
	}
	if isSetTitle(title) {
		return tr
	}
	wantedNorm := NormalizeRelease(title)
	seen := map[string]bool{}
	othersAt := map[string]int{}
	for _, raw := range bc.otherTitles {
		cleaned := newznab.StripFormatQualifiers(newznab.NormalizeQueryTitle(raw))
		if isSetTitle(cleaned) {
			continue
		}
		variants := []string{cleaned, primaryTitle(cleaned), newznab.SeriesPositionTitle(cleaned)}
		prefixedTail := ""
		if !tr.posKnown && len(wanted) > 0 {
			if n, tail, ok := siblingTail(cleaned, wanted[0]); ok {
				tr.siblingNums[n] = true
				variants = append(variants, tail)
			} else if tail, ok := prefixedSuffix(cleaned, wanted[0]); ok {
				// An unnumbered series prefixed sibling (#3382): its suffix is
				// another book's name, whether or not the standalone row of
				// that book is excluded from the listing.
				variants = append(variants, tail)
				prefixedTail = tail
			}
		}
		for _, v := range variants {
			ident := titleIdentityKeywords(v)
			if len(ident) < 2 {
				continue
			}
			key := strings.Join(ident, " ")
			isPrefixed := prefixedTail != "" && v == prefixedTail
			if seen[key] {
				// The same title also stands as a book of its own: it is
				// then not only a subtitle.
				if i, ok := othersAt[key]; ok && !isPrefixed {
					tr.others[i].prefixed = false
				}
				continue
			}
			seen[key] = true
			if relatedToSeries(v) || ContainsPhrase(wantedNorm, ident) {
				continue
			}
			normTitle := NormalizeRelease(v)
			contained := false
			for _, w := range wanted {
				if len(w) > 0 && ContainsPhrase(normTitle, w) {
					contained = true
					break
				}
			}
			if contained {
				continue
			}
			ot := otherTitle{ident: ident, prefixed: isPrefixed}
			for _, w := range ident {
				if isASCII(w) && !isAllDigits(w) && !newznab.IsVolumeMarker(w) {
					ot.words = append(ot.words, w)
				}
			}
			othersAt[key] = len(tr.others)
			tr.others = append(tr.others, ot)
		}
	}
	return tr
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

var (
	positionIntRe   = regexp.MustCompile(`^(\d{1,3})(?:\.0+)?$`)
	positionRangeRe = regexp.MustCompile(`^(\d{1,3})\s*[-\x{2013}\x{2014}]\s*(\d{1,3})$`)
)

// parsePosition reads a stored series position. Only a whole number or a
// whole number range is understood; "2.5", "Prequel" and "" are unknown, and
// signal A then stays quiet.
func parsePosition(s string) (lo, hi int, ok bool) {
	s = strings.TrimSpace(s)
	if m := positionIntRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, n, true
	}
	if m := positionRangeRe.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a <= b {
			return a, b, true
		}
	}
	return 0, 0, false
}

// check applies both signals to one release for one reading of the wanted
// title. n is NormalizeRelease(raw); idents are the identity word lists of the
// reading (strict, then the apostrophe elided one). It returns the drop reason,
// or "" when the release is not read as another book.
func (tr *trailingRules) check(raw, n string, idents ...[]string) string {
	toks, k, ok := locateTitleEnd(raw, n, idents...)
	if !ok {
		return ""
	}
	if reason := tr.seriesNumberAfter(toks, k); reason != "" {
		return reason
	}
	return tr.otherTitleInLead(toks[k:], numberBeforeTitle(toks, n, idents...))
}

// locateTitle finds where the wanted title ends in the release: the tokens of
// raw and the index of the first token after the title, which equals
// len(toks) when the title ends the release. found is false when the title has
// no contiguous phrase hit (a release that matched only through the unordered
// keyword fallback) or when the raw tokenisation cannot be trusted.
func locateTitle(raw, n string, idents ...[]string) (toks []relTok, k int, found bool) {
	end := -1
	for _, ident := range idents {
		if len(ident) == 0 {
			continue
		}
		if loc := phraseRegex(ident).FindStringIndex(n); loc != nil {
			end = loc[1]
			break
		}
	}
	if end < 0 {
		return nil, 0, false
	}
	toks, ok := tokenizeRelease(raw)
	if !ok {
		return nil, 0, false
	}
	return toks, len(strings.Fields(n[:end])), true
}

// locateTitleEnd is locateTitle for the rules that read what follows: ok is
// false when nothing follows the title.
func locateTitleEnd(raw, n string, idents ...[]string) (toks []relTok, k int, ok bool) {
	toks, k, found := locateTitle(raw, n, idents...)
	if !found || k >= len(toks) {
		return nil, 0, false
	}
	return toks, k, true
}

// readMarkers are the words that may stand between a title and a series number
// when the number is only READ for equality ("Mistborn Book Two", "Mistborn
// Series 02", "Mistborn Season 2"). A wider set than signal A's, because
// equality only ever keeps a release.
var readMarkers = map[string]bool{
	"book": true, "bk": true, "vol": true, "volume": true, "no": true,
	"number": true, "episode": true, "tome": true, "band": true, "part": true,
	"series": true, "season": true,
}

var (
	seasonTokenRe = regexp.MustCompile(`^s(\d{1,2})$`)
	numberWords   = map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
		"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
		"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
		"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
	}
)

// readableRoman returns the value of a capital roman numeral that may be read
// as a series number: II to XXX and I..XXX minus the letters that are far more
// often an initial or a word. A lone I (the pronoun), V and X (author
// initials: "V. E. Schwab", "X. Y. Smith") are never read; lowercase is never
// read; nothing above XXX is read (L, C, D and M alone are 50 and up).
func readableRoman(raw string) (int, bool) {
	if raw == "" || raw != strings.ToUpper(raw) || !romanRe.MatchString(raw) {
		return 0, false
	}
	switch raw {
	case "I", "V", "X":
		return 0, false
	}
	v := romanValue(raw)
	return v, v >= 1 && v <= 30
}

// loneRomanReadable reports whether the capital I, V or X at toks[i] names the
// wanted number when read for equality (never when read to reject, where
// readableRoman keeps them out so "V. E. Schwab" is never a number). It does so
// behind a marker word or a hash, at the end of the name, or before a format or
// other release furniture. An author initial is followed by another initial or
// a name, so a following single letter or ordinary word rules it out.
func loneRomanReadable(toks []relTok, i int, afterMarker bool) bool {
	switch toks[i].raw {
	case "I", "V", "X":
	default:
		return false
	}
	if afterMarker || strings.Contains(toks[i].sep, "#") || i+1 >= len(toks) {
		return true
	}
	next := toks[i+1].text
	return len(next) > 1 && benignTrailingToken(next, nil)
}

// standsAsNumber reports whether the number token at toks[i] is a number and
// not a count of something: it ends the name, is followed by punctuation, or
// sits behind a marker word or a hash. "of" and another number after it never
// make it one. This is signal A's clause set.
func standsAsNumber(toks []relTok, i int, viaMarker bool) bool {
	if i+1 >= len(toks) {
		return true
	}
	next := toks[i+1]
	if next.text == "of" || isAllDigits(next.text) {
		return false
	}
	return viaMarker || strings.Contains(toks[i].sep, "#") || punctBefore(next)
}

// numbersAfter reads the series numbers named directly after the wanted title:
// digits ("2", "02"), a roman numeral in capitals ("II", see readableRoman), a
// number word after a marker ("Book Two"), and a season token ("S1"), behind
// any run of marker words ("Series 02", "S1 Part 2"). The scan stops at the
// first token that is none of these or after a plain number.
//
// Two uses. Read for equality, to KEEP a release that carries the wanted number
// in any spelling, it is lenient (guarded false). Read to REJECT a release that
// names a different number it is guarded: a digit number followed by an ordinary
// word ("64 kbps", "24 bit", "12 files") is a count and is not read, as in
// signal A.
func numbersAfter(raw, n string, guarded bool, idents ...[]string) []int {
	toks, k, found := locateTitle(raw, n, idents...)
	if !found {
		return nil
	}
	var out []int
	afterMarker := false
	for i := k; i < len(toks); i++ {
		t := toks[i]
		roman, isRoman := readableRoman(t.raw)
		switch {
		case readMarkers[t.text]:
			afterMarker = true
		case seasonTokenRe.MatchString(t.text):
			v, _ := strconv.Atoi(seasonTokenRe.FindStringSubmatch(t.text)[1])
			out = append(out, v)
			afterMarker = true
		case len(t.text) <= 2 && isAllDigits(t.text):
			if guarded && !standsAsNumber(toks, i, afterMarker) {
				return out
			}
			v, _ := strconv.Atoi(t.text)
			return append(out, v)
		case isRoman:
			// A capital roman numeral is never a count, so it is not guarded.
			return append(out, roman)
		case !guarded && loneRomanReadable(toks, i, afterMarker):
			return append(out, romanValue(t.raw))
		case afterMarker && numberWords[t.text] > 0:
			return append(out, numberWords[t.text])
		default:
			return out
		}
	}
	return out
}

// seriesNumberAfter is signal A. toks[k:] is what follows the wanted title.
func (tr *trailingRules) seriesNumberAfter(toks []relTok, k int) string {
	if !tr.posKnown && len(tr.siblingNums) == 0 {
		return ""
	}
	i := k
	viaMarker := false
	for i < len(toks) && seriesNumberMarkers[toks[i].text] {
		i++
		viaMarker = true
	}
	if i >= len(toks) {
		return ""
	}
	num := toks[i]
	n := 0
	switch {
	case len(num.text) <= 2 && isAllDigits(num.text):
		n, _ = strconv.Atoi(num.text)
	case !tr.wantedEndsRoman:
		// A capital roman numeral II to XXX (see readableRoman: never a lone
		// I, V or X). A title that itself ends in one ("Henry V") is not read
		// this way.
		if v, ok := readableRoman(num.raw); ok {
			n = v
		}
	}
	if n < 2 {
		return ""
	}
	if tr.posKnown {
		if n >= tr.posLo && n <= tr.posHi {
			return ""
		}
	} else if !tr.siblingNums[n] {
		return ""
	}
	if strings.Contains(num.sep, "#") {
		viaMarker = true
	}
	if i+1 < len(toks) {
		next := toks[i+1]
		if next.text == "of" || isAllDigits(next.text) {
			return ""
		}
		// A number followed by an ordinary word is a count ("12 files",
		// "4 mp3"), unless a marker word says it numbers the book.
		if !viaMarker && !punctBefore(next) {
			return ""
		}
	}
	return reasonLaterNumber
}

// otherTitleIn is signal B. rest is what follows the wanted title.
func (tr *trailingRules) otherTitleIn(rest []relTok) string {
	return tr.otherTitleInLead(rest, false)
}

// otherTitleInLead is otherTitleIn that also knows whether a series number
// stands before the wanted title, which stops a prefixed sibling's suffix from
// counting as the sibling's own full title.
func (tr *trailingRules) otherTitleInLead(rest []relTok, numberedLead bool) string {
	if len(tr.others) == 0 {
		return ""
	}
	words := make(map[string]bool, len(rest))
	texts := make([]string, len(rest))
	for i, t := range rest {
		words[t.text] = true
		texts[i] = t.text
	}
	haystack := strings.Join(texts, " ")
	for _, ot := range tr.others {
		present := true
		for _, w := range ot.words {
			if !words[w] {
				present = false
				break
			}
		}
		if !present {
			continue
		}
		// Another book is any occurrence that is not a series label (see
		// seriesLabel). One real occurrence is enough.
		for _, loc := range phraseRegex(ot.ident).FindAllStringIndex(haystack, -1) {
			start := len(strings.Fields(haystack[:loc[0]]))
			end := len(strings.Fields(haystack[:loc[1]]))
			if ot.prefixed && !numberedLead && startsRightAfterTitle(rest, start) {
				continue
			}
			if !seriesLabel(rest, start, end) {
				return reasonAnotherTitle
			}
		}
	}
	return ""
}

// stripMarks removes combining accents ("edición" becomes "edicion") so the
// edition words match whatever diacritics a catalogue wrote.
func stripMarks(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(s))
}

// connectingWords are the only words that may sit between the wanted title and
// a prefixed sibling's suffix when the release is still that sibling's own full
// title ("Sapiens - A Brief History of Humankind", "Frankenstein or The Modern
// Prometheus"). Everything else, a roman numeral ("II"), a short word ("Mr"), a
// number or any ordinary word, makes the release name another book.
var connectingWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "in": true,
	"to": true, "by": true, "for": true, "with": true, "at": true, "from": true, "on": true,
}

// startsRightAfterTitle reports whether nothing but connecting words sits
// between the wanted title and rest[start:]. The release then reads wanted
// title plus suffix, the library sibling's own full title (#3382). A number or
// any other word in between ("[Rotten School 11] - Punk'd and Skunked") means a
// different book.
func startsRightAfterTitle(rest []relTok, start int) bool {
	for _, t := range rest[:start] {
		if !connectingWords[t.text] {
			return false
		}
	}
	return true
}

// numberBeforeTitle reports whether a series number of up to three digits sits
// directly before the wanted title in the release ("11 - Rotten School - ..."),
// which names a numbered volume whatever follows the title.
func numberBeforeTitle(toks []relTok, n string, idents ...[]string) bool {
	for _, ident := range idents {
		if len(ident) == 0 {
			continue
		}
		if loc := phraseRegex(ident).FindStringIndex(n); loc != nil {
			start := len(strings.Fields(n[:loc[0]]))
			return start > 0 && start <= len(toks) && len(toks[start-1].text) <= 3 && isAllDigits(toks[start-1].text)
		}
	}
	return false
}

// seriesWords are the words after a series name that say it is a series.
var seriesWords = map[string]bool{
	"series": true, "trilogy": true, "saga": true, "cycle": true, "chronicles": true,
	"quartet": true, "quintet": true, "duology": true, "tetralogy": true,
}

// seriesLabel reports whether the library title at rest[start:end] is used as a
// series label and not as another book: it sits inside brackets or
// parentheses; or it is followed by a series word ("The Dark Tower Series"), a
// number ("Rotten School 12"; three digits at most, so a year is not one), a
// marker word and a number or number word ("Rotten School Book 12"), or a
// capital roman numeral ("The Dark Tower III"). A different book whose title
// really ends with a number, or is written in brackets, is read as a label too;
// that is a known limit.
func seriesLabel(rest []relTok, start, end int) bool {
	// An opening bracket may sit before an article that belongs to the title
	// ("(The Dark Tower)"), so look back over insignificant words too.
	for i := start; i < len(rest); {
		if strings.ContainsAny(rest[i].sep, "([{") {
			return true
		}
		if i == 0 || len(newznab.SigWords(rest[i-1].text)) != 0 {
			break
		}
		i--
	}
	if end >= len(rest) {
		return false
	}
	a := rest[end]
	if seriesWords[a.text] || (len(a.text) <= 3 && isAllDigits(a.text)) {
		return true
	}
	if a.raw == strings.ToUpper(a.raw) && romanRe.MatchString(a.raw) && romanValue(a.raw) >= 1 && romanValue(a.raw) <= 30 {
		return true
	}
	if readMarkers[a.text] && end+1 < len(rest) {
		b := rest[end+1]
		return (len(b.text) <= 3 && isAllDigits(b.text)) || numberWords[b.text] > 0
	}
	return false
}

// AuthorTitlesExcluding returns the titles of books other than selfID, for
// MatchCriteria.AuthorTitles. The caller loads the author's books once per
// search; nothing here touches the database, so the cost per release is a
// string comparison.
func AuthorTitlesExcluding(selfID int64, books []models.Book) []string {
	titles := make([]string, 0, len(books))
	for _, b := range books {
		if b.ID == selfID || strings.TrimSpace(b.Title) == "" {
			continue
		}
		titles = append(titles, b.Title)
	}
	return titles
}
