package indexer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// The main title reading (#3268, part 2).
//
// A "Main: Subtitle" book is also searched under its main title alone, because
// releases drop the subtitle ("The Hobbit" for "The Hobbit: Or There and Back
// Again"). That reading used to be judged only on the title words and the
// volume markers, so "Legion (2012)" satisfied "Legion: Skin Deep" and "The
// High Druid of Shannara 3 - Straken" satisfied "High Druid of Shannara: Jarka
// Ruus". Three rules now sit on top of it. All of them apply only to a release
// that carries the main title and NOT the subtitle: a release that names the
// subtitle too ("Prelude to Foundation") is not a main title only release.
//
//  1. The trailing title rules (series number, another library title) read
//     what follows the main title, exactly as they do for the full title.
//  2. When some other result in the same search carries the FULL title, is
//     approved on it, is the same media type and is the book itself (no
//     summary or study aid wording, only release furniture after the title),
//     a main title only release is its sibling and is dropped.
//     Only when nothing carries the full title is the main title reading used
//     at all. The exception is a subtitle that only numbers the book ("Dune:
//     Part One", "Foo: I", "Foo: 1"), because releases drop such a subtitle
//     and keep the main title.
//  3. A main title only release is dropped on two pieces of positive
//     evidence: the main title is itself the title of another of the author's
//     library books that is not the book's series ("Legion" for "Legion: Skin
//     Deep"), or the subtitle numbers the book 2 or more ("Dune: Part Two") and
//     the release names a different number. The release is kept if it carries
//     the wanted number right after the main title ("Legion 2"). A stored
//     series position of 2 or more is NOT evidence on its own: the plain
//     "Author - Main (epub)" release is the commonest form of a subtitled book
//     after the first.
//
// A lone main title release with no position, no library title to compare it
// with, and no full title in the batch stays approved: nothing in the release
// or the library says it is another book. That is the rule's known limit.

// FilterDebug reasons for the main title reading.
const (
	reasonMainTitleOnlyFullFound = "release carries only the main title, and another result carries the full title"
	reasonMainTitleOnlySequel    = "release carries only the main title, but this book is a later entry or the main title is another of the author's books"
)

// subtitleInfo describes the "Main: Subtitle" shape of the wanted title.
type subtitleInfo struct {
	has        bool
	ident      []string // identity words of the subtitle
	seriesLike bool     // the subtitle only numbers the book
	n          int      // the number it names, when seriesLike
}

func newSubtitleInfo(title string) subtitleInfo {
	i := strings.Index(title, ":")
	if i <= 0 {
		return subtitleInfo{}
	}
	sub := strings.TrimSpace(title[i+1:])
	if sub == "" {
		return subtitleInfo{}
	}
	si := subtitleInfo{has: true, ident: titleIdentityKeywords(sub)}
	si.n, si.seriesLike = seriesLikeSubtitle(sub)
	return si
}

// mainTitleOnly judges a release that matched through the main title alone and
// returns the drop reason, or "" to keep it. identity is the release's identity
// words joined; anyFull reports whether another result of the given media
// type carries the full title (see fullTitleCounts).
func (si subtitleInfo) mainTitleOnly(tr *trailingRules, raw, n, identity string, anyFull func(mediaType string) bool, primary ...[]string) string {
	if !si.has {
		return ""
	}
	// The release names the subtitle as well, so it is not main title only.
	if len(si.ident) > 0 && ContainsPhrase(identity, si.ident) {
		return ""
	}
	// A subtitle that only numbers the book as the first ("Part One", "I")
	// is dropped by releases as a matter of course.
	if si.seriesLike && si.n <= 1 {
		return ""
	}
	if mt := releaseMediaType(raw); mt != "" && anyFull(mt) {
		return reasonMainTitleOnlyFullFound
	}
	// A subtitle that numbers the book 2 or more: a release that names a
	// DIFFERENT number is another book. A release that names no number is
	// not evidence of anything and is kept: a stored series position of 2 or
	// more is not evidence either, since the plain "Author - Main (epub)"
	// release is the commonest form of a subtitled book after the first.
	want := 0
	if si.seriesLike {
		want = si.n
		nums := numbersAfter(raw, n, true, primary...)
		if len(nums) > 0 && !containsInt(nums, want) {
			return reasonMainTitleOnlySequel
		}
	}
	// The main title is itself another of the author's library books (and not
	// the book's series): the release is that book, unless it carries the
	// wanted number right after the main title.
	for _, p := range primary {
		if len(p) > 0 && tr.otherFull[strings.Join(p, " ")] {
			if want == 0 && tr.posKnown && tr.posLo == tr.posHi {
				want = tr.posLo
			}
			if want > 0 && containsInt(numbersAfter(raw, n, false, primary...), want) {
				return ""
			}
			return reasonMainTitleOnlySequel
		}
	}
	return ""
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

var (
	// subtitleMarkers are the words that may precede the number in a subtitle
	// that only numbers the book.
	subtitleMarkers = map[string]bool{
		"book": true, "part": true, "volume": true, "vol": true, "episode": true,
		"ep": true, "chapter": true, "tome": true, "band": true, "no": true,
		"number": true, "issue": true,
	}
	romanRe = regexp.MustCompile(`^(?:M{0,3})(?:CM|CD|D?C{0,3})(?:XC|XL|L?X{0,3})(?:IX|IV|V?I{0,3})$`)
)

// seriesLikeSubtitle reports whether sub only numbers the book, and which
// number: a bare number ("3"), a roman numeral written in capitals ("II"), or a
// marker word and a number, roman numeral or number word ("Book 1", "Part
// One", "Volume 4", "Episode 5"). A roman numeral needs capitals so "Mix" and
// "Civil" are words; a number word needs a marker so "Two" alone is a title.
func seriesLikeSubtitle(sub string) (int, bool) {
	fields := strings.Fields(strings.NewReplacer(".", " ", ",", " ").Replace(sub))
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false
	}
	marked := false
	if len(fields) == 2 {
		if !subtitleMarkers[strings.ToLower(fields[0])] {
			return 0, false
		}
		marked = true
	}
	tok := fields[len(fields)-1]
	if isAllDigits(tok) && len(tok) <= 3 {
		v, _ := strconv.Atoi(tok)
		return v, true
	}
	if marked {
		if v, ok := numberWords[strings.ToLower(tok)]; ok {
			return v, true
		}
	}
	if tok == strings.ToUpper(tok) && romanRe.MatchString(tok) {
		if v := romanValue(tok); v >= 1 && v <= 30 {
			return v, true
		}
	}
	return 0, false
}

func romanValue(s string) int {
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			total -= v
		} else {
			total += v
		}
	}
	return total
}

// releaseMediaType returns the media type a release names by its format words,
// or "" when it names none or names both kinds.
func releaseMediaType(title string) string {
	mt := ""
	for _, f := range ReleaseFormats(title) {
		t := MediaTypeForFormat(f)
		if t == "" {
			continue
		}
		if mt != "" && mt != t {
			return ""
		}
		mt = t
	}
	return mt
}

// derivativeWordRe finds the words of a summary, study aid or companion, which
// is about a book and is not the book.
var derivativeWordRe = regexp.MustCompile(`(?i)\b(?:summary|summaries|analysis|analyses|study\s+guide|workbook|companion|review|reviews|notes|guide|cheat\s*sheets?|cliffs?\s*notes|spark\s*notes|highlights|synopsis|key\s+takeaways|insights)\b`)

// fullTitleCounts reports whether a result that carries the full title is good
// evidence that a main title only sibling is not the book (the batch rule).
// It counts only when the text after the full title is empty or only release
// furniture (a format, a year, a bitrate, the author, a credit tag: what
// benignTrailingToken accepts) and no summary or study aid wording appears.
// Anything else, a title that cannot be located included, does not count, so
// the main title only release is kept.
func fullTitleCounts(raw, n string, authorSets [][]string, idents ...[]string) bool {
	if derivativeWordRe.MatchString(raw) {
		return false
	}
	toks, k, found := locateTitle(raw, n, idents...)
	if !found {
		return false
	}
	for _, t := range toks[k:] {
		if len(newznab.SigWords(t.text)) == 0 {
			continue
		}
		benign := benignTrailingToken(t.text, nil)
		for _, set := range authorSets {
			benign = benign || benignTrailingToken(t.text, set)
		}
		if !benign {
			return false
		}
	}
	return true
}
