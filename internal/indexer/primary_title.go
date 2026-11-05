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
	reasonMainTitleRunsOn        = "release carries only the main title, and goes on into a different title by the same author"
)

// subtitleInfo describes the "Main: Subtitle" shape of the wanted title.
type subtitleInfo struct {
	has        bool
	ident      []string // identity words of the subtitle
	seriesLike bool     // the subtitle only numbers the book
	n          int      // the number it names, when seriesLike
	// known holds the release alphabet words that may follow the main title
	// without naming a different book: the subtitle's own words and the
	// primary series title's (see runsIntoOtherTitle).
	known map[string]bool
	// knownSeqs are the same words in title order, one slice per source, for
	// run together abbreviations (see abbreviates).
	knownSeqs [][]string
	// authorSets are the author token sets of the search, whose words are
	// release furniture after the main title.
	authorSets [][]string
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
	si := subtitleInfo{has: true, ident: titleIdentityKeywords(sub), known: map[string]bool{}}
	si.addKnown(sub)
	si.n, si.seriesLike = seriesLikeSubtitle(sub)
	return si
}

// withContext adds what the search knows beyond the title: the author token
// sets and the book's primary series title, whose words may follow the main
// title in a release of this book.
func (si subtitleInfo) withContext(authorSets [][]string, seriesTitle string) subtitleInfo {
	if !si.has {
		return si
	}
	si.authorSets = authorSets
	si.addKnown(seriesTitle)
	return si
}

// addKnown records the identity words of s as words that may follow the main
// title.
func (si *subtitleInfo) addKnown(s string) {
	words := titleIdentityWords(s)
	for _, w := range words {
		si.known[w] = true
	}
	if len(words) > 1 {
		si.knownSeqs = append(si.knownSeqs, words)
	}
}

// abbreviates reports whether tok is the leading words of seq run together,
// two or more of them, each cut to a prefix of at least two letters: "exforce"
// and "exfor" for "expeditionary force". The abbreviation must start with the
// first word and be at least five letters long, so a short ordinary word that
// happens to split into word prefixes does not pass: "risk" for "rise
// skywalker", "most" for "morning star", "fino" for "fireteam noble".
func abbreviates(tok string, seq []string) bool {
	if len(tok) < 5 {
		return false
	}
	var from func(rest string, j, parts int) bool
	from = func(rest string, j, parts int) bool {
		if rest == "" {
			return parts >= 2
		}
		if j >= len(seq) {
			return false
		}
		w := seq[j]
		for l := min(len(w), len(rest)); l >= 2; l-- {
			if rest[:l] == w[:l] && from(rest[l:], j+1, parts+1) {
				return true
			}
		}
		return false
	}
	return from(tok, 0, 0)
}

// languageCodes are the three letter codes releaseLanguageTags maps to
// ("eng", "ger"), which releases also carry on their own.
var languageCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, code := range releaseLanguageTags {
		m[code] = true
	}
	return m
}()

// runsIntoOtherTitle reports whether a release that carries only the main
// title goes on into a different title by the wanted author (#3615): "Halo:
// Oblivion: A Master Chief Story by Troy Denning" for "Halo: Fireteam Noble",
// "Camp Half-Blood Confidential by Rick Riordan" for "Camp Half-Blood: From the
// World of Percy Jackson: The Rite of Aphrodite". Both credit the right author,
// so the author checks pass them, and the main title is all the two share.
//
// It fires only on that shape: words after the main title that are not known
// furniture, then a credit to the wanted author, either "by <author>" or a
// dash and the author. Unknown words alone are not enough. An allow list of
// what may follow a title can never be complete, and a release that names the
// book plus some unlisted word is far more often the book than another one. A
// credit to the author closing the run is what marks the words before it as a
// title. The words that do not count are furnitureAfterMainTitle's: release,
// edition and publisher furniture, numbers and number words, the wanted
// subtitle and series words and their run together abbreviations ("ExForce"
// for "Expeditionary Force").
//
// Known limits, both accepted because a same author sequel written "Main
// Other Title - Author" is the bug this exists for, and a release dropped here
// stays visible and grabbable in interactive search:
//   - the same book under a subtitle the library does not hold, credited the
//     same way ("Atomic Habits Tiny Changes Remarkable Results by James
//     Clear"): no string rule can tell that from another book;
//   - a narrator or co author between the main title and the credit with no
//     role word ("Project Hail Mary Ray Porter - Andy Weir"). A name cannot be
//     told from a title by its shape: "Halo Silent Storm - Troy Denning" is
//     two capitalised words before the dash too.
//
// Reading starts only when the main title is followed by a colon or plain
// spacing, dots or underscores. After a dash, comma or bracket what follows
// may be a series label, an author or a narrator, and nothing is read. A "by"
// that credits someone else, or a narrator or translator, ends the reading:
// the first is conflictingTitleAuthor's to judge. So does a role word such as
// "narrated" or "translated" before any unknown word.
func (si subtitleInfo) runsIntoOtherTitle(raw, n string, primary ...[]string) bool {
	toks, k, ok := locateTitleEnd(raw, n, primary...)
	if !ok {
		return false
	}
	if punctBefore(toks[k]) && !strings.Contains(toks[k].sep, ":") {
		return false
	}
	foreign := false
	for i := k; i < len(toks); i++ {
		t := toks[i]
		if t.text == "by" {
			if i > k && creditRoleWords[toks[i-1].text] {
				return false
			}
			return foreign && si.creditsWantedAuthor(toks[i+1:], false, false)
		}
		// A role word with nothing unknown before it ("Translated Hazel E.
		// Barnes", "narrated Ray Porter") is followed by a contributor's name,
		// not a title.
		if creditRoleWords[t.text] && !foreign {
			return false
		}
		if i > k && dashBefore(t) && si.creditsWantedAuthor(toks[i:], true, bareHyphen(t)) {
			return foreign
		}
		if !isAllDigits(t.text) && len(newznab.SigWords(t.text)) == 0 {
			continue
		}
		// A word inside a hyphenated compound ("Sci-Fi", "WEB-DL",
		// "Holdbrook-Smith") is one unit with its neighbour, not a title word
		// of its own, unless it is the last word before a credit
		// ("Storm-Troy.Denning").
		if (i > k && bareHyphen(t)) ||
			(i+1 < len(toks) && bareHyphen(toks[i+1]) && !si.creditsWantedAuthor(toks[i+1:], true, true)) {
			continue
		}
		// A roman numeral is a series number, which the number rules below
		// judge ("Dune III" for "Dune: Part Two").
		if _, roman := readableRoman(t.raw); roman {
			continue
		}
		if !si.furnitureAfterMainTitle(t.text) {
			foreign = true
		}
	}
	return false
}

// runOnFurniture are words that follow a title in releases of the book itself
// and name a source, an edition, a publisher's line or a production rather
// than another title. They matter only to runsIntoOtherTitle, which reads them
// before a credit to the wanted author. The tens complete numberWords, so a
// title's numerals spelled out ("1984 Nineteen Eighty Four") do not count.
var runOnFurniture = map[string]bool{
	"audible": true, "libation": true, "repack": true, "kepub": true,
	"chapterized": true, "graphicaudio": true, "graphic": true, "dramatized": true,
	"dramatised": true, "adaptation": true, "signed": true, "first": true,
	"full": true, "cast": true, "classics": true, "critical": true, "norton": true,
	"penguin": true, "oxford": true, "worlds": true, "folio": true, "text": true,
	"ungekuerzte": true, "ungekurzte": true, "ungekrzte": true, "lesung": true,
	"gekuerzte": true, "gekurzte": true, "gekrzte": true, "recorded": true,
	"books": true, "brilliance": true, "macmillan": true, "junior": true,
	"novel": true, "science": true, "fiction": true, "hugo": true, "nominee": true,
	"hoerspiel": true, "horspiel": true, "routledge": true,
	"thirty": true, "forty": true, "fifty": true, "sixty": true, "seventy": true,
	"eighty": true, "ninety": true, "hundred": true, "thousand": true,
}

// dashRunes are hyphen, en dash and em dash, built from code points so the
// source carries no dash literally.
var dashRunes = string([]rune{'-', 0x2013, 0x2014})

// dashBefore reports whether a dash separates tok from the token before it. A
// bare hyphen counts too, because dotted release names join their fields with
// one ("Halo.Silent.Storm-Troy.Denning"); the caller then needs the wanted
// author's name to start right at tok (see bareHyphen).
func dashBefore(tok relTok) bool {
	return strings.ContainsAny(tok.sep, dashRunes)
}

// bareHyphen reports whether tok is joined to the token before it by a dash
// with no space, dot or underscore beside it: the hyphen of "Sci-Fi" or
// "Jean-Paul", and the field joint of "Storm-Troy.Denning". Such a hyphen only
// opens a credit when the author's name starts right after it.
func bareHyphen(tok relTok) bool {
	return strings.ContainsAny(tok.sep, dashRunes) && !strings.ContainsAny(tok.sep, " \t_.")
}

// creditsWantedAuthor reports whether toks open with a credit to the wanted
// author: the words up to the first attributionStopToken (or, when segment is
// set, up to the next punctuation other than a hyphen inside a name, so
// "Jean-Paul Sartre" is read whole) name one of the author token sets. With
// mustStart the first word must itself be one of the author's words, so a
// credit cannot begin inside "Sci-Fi Andy Weir" and pick the name up later.
func (si subtitleInfo) creditsWantedAuthor(toks []relTok, segment, mustStart bool) bool {
	var words []string
	for j, t := range toks {
		if attributionStopToken(t.text) || (segment && j > 0 && punctBefore(t) && !bareHyphen(t)) {
			break
		}
		words = append(words, t.text)
	}
	if len(words) == 0 {
		return false
	}
	credit := strings.Join(words, " ")
	for _, set := range si.authorSets {
		if len(set) == 0 || !authorMatchesRelease(credit, set) {
			continue
		}
		if !mustStart || startsWithAuthorWord(words[0], set) {
			return true
		}
	}
	return false
}

// startsWithAuthorWord reports whether word is one of the author tokens.
func startsWithAuthorWord(word string, set []string) bool {
	for _, a := range set {
		if WordBoundaryRegex(a).MatchString(word) {
			return true
		}
	}
	return false
}

// furnitureAfterMainTitle reports whether tok, a significant word after the
// main title, is known to be compatible with the release being the wanted
// book.
func (si subtitleInfo) furnitureAfterMainTitle(tok string) bool {
	if si.known[tok] || languageCodes[tok] || readMarkers[tok] || runOnFurniture[tok] ||
		editionWords[tok] || editionForms[tok] || editionLanguages[tok] {
		return true
	}
	if _, ok := numberWords[tok]; ok {
		return true
	}
	for _, seq := range si.knownSeqs {
		if abbreviates(tok, seq) {
			return true
		}
	}
	if benignTrailingToken(tok, nil) {
		return true
	}
	for _, set := range si.authorSets {
		if benignTrailingToken(tok, set) {
			return true
		}
	}
	return false
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
	// The release puts a different title where the subtitle would be.
	if si.runsIntoOtherTitle(raw, n, primary...) {
		return reasonMainTitleRunsOn
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
