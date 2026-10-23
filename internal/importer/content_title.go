package importer

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

// Title comparison shared by the two post-download content checks: the ebook
// check (#3015) compares an EPUB's dc:title with the grabbed book, and the
// audiobook check (#3246) compares each file's album or track title with it.
// Both are the same question, "is this title the wanted book, allowing for how
// titles are routinely dressed up", so they share one normaliser and one
// notion of support.
//
// Direction of every judgement here: when in doubt, a title SUPPORTS the book.
// A false "supports" lets one more mismatched file through, which the user can
// still see in warn mode and fix with Fix match. A false "does not support"
// refuses a good download in block mode, which is the worse failure. So forms
// are generous (series suffix stripped, subtitle stripped, both kept), and a
// short title is supported by any title that contains it as whole words.

// contentTitleForms returns every comparable form of title, normalised and
// without duplicates, most specific first:
//
//   - the title as written, folded (case, punctuation, diacritics, "&" as
//     "and", a leading article dropped, a filing form "Racketeer, The" put back
//     in natural order);
//   - the same after a trailing bracketed or parenthesised qualifier is dropped
//     ("(Volume 1)", "[Unabridged]");
//   - that with a numbered series suffix removed (", Book 11" and
//     ": Alex Cross, Book 11");
//   - the main title before a ": subtitle";
//   - the subtitle itself, but only when the main title reads as a series prefix
//     ("The Wheel of Time, Book 1: The Eye of the World", "The Dark Tower IV:
//     Wizard and Glass"), where the subtitle is the book's real title.
//
// An umlaut is read both ways ("Müller" as "muller" and "mueller"), because
// metadata sources disagree about which they use.
//
// Empty forms are never returned.
func contentTitleForms(title string) []string {
	var out []string
	add := func(s string) {
		for _, k := range titleFoldVariants(s) {
			if k == "" {
				continue
			}
			dup := false
			for _, have := range out {
				if have == k {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, k)
			}
		}
	}
	add(title)
	trimmed := indexer.StripBracketSuffixes(strings.TrimSpace(title))
	add(trimmed)
	noSeries := stripSeriesSuffix(trimmed)
	add(noSeries)
	main, sub := indexer.SplitSubtitle(noSeries)
	add(main)
	// The subtitle is an accepted form when it is a title of its own: at least
	// two meaningful words, and either the main title reads as a series prefix
	// or the subtitle does not end in a generic word ("A LitRPG Adventure", "A
	// Novel"), which tells a book nothing.
	if sub != "" && significantTitleTokens(sub) >= 2 && (looksLikeSeriesPrefix(main) || !genericSubtitleTail(sub)) {
		add(sub)
	}
	return out
}

var genericSubtitleTails = map[string]bool{
	"novel": true, "adventure": true, "thriller": true, "mystery": true, "memoir": true, "story": true,
	"stories": true, "romance": true, "saga": true, "tale": true, "edition": true, "audiobook": true,
	"unabridged": true, "series": true,
	"part": true, "episode": true, "volume": true, "vol": true, "book": true, "chapter": true,
}

// genericSubtitleTail reports whether a subtitle ends in a word that labels a
// kind of book rather than naming one.
func genericSubtitleTail(sub string) bool {
	toks := strings.Fields(textutil.FoldForSearch(sub))
	if len(toks) == 0 {
		return false
	}
	// "Part Two", "Book 3", "Episode Four": a label that opens the subtitle
	// (the number after it is the last word) is as generic as one that closes it.
	return genericSubtitleTails[toks[len(toks)-1]] || genericSubtitleTails[toks[0]]
}

// titleFoldVariants folds s into the comparison alphabet and returns it, plus
// the umlaut-expanded reading when that differs.
func titleFoldVariants(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	s = textutil.RestoreFilingForm(s)
	a := dropLeadingArticle(textutil.FoldForSearch(s))
	b := dropLeadingArticle(textutil.FoldForSearch(textutil.TransliterateUmlauts(strings.ToLower(s))))
	out := []string{a}
	if b != a {
		out = append(out, b)
	}
	// Bounded folds, kept as extra forms beside the plain ones so they only ever
	// add ways to match: abbreviations, spelled numbers, and the same title with
	// its spaces removed (so "Hitch-Hiker's" meets "Hitchhiker's").
	base := a
	if c := foldTitleTokens(a); c != a {
		out = append(out, c)
		base = c
	}
	if strings.Contains(base, " ") {
		out = append(out, spacelessMark+strings.ReplaceAll(base, " ", ""))
	}
	return out
}

// spacelessMark starts a form with its spaces removed. Such a form can only
// ever equal another spaceless form: the mark is not a word character, so no
// whole word search lands inside one.
const spacelessMark = "~"

var titleAbbreviations = map[string]string{"dr": "doctor", "mr": "mister", "mrs": "missus", "st": "saint"}

var titleNumberValues = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
	"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30,
	"forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}

// foldTitleTokens rewrites a folded title so common spellings meet: Dr, Mr, Mrs
// and St become their long forms, spelled numbers up to twenty and the tens
// become digits ("twenty two" is 22), "twenty thousand" and "20 000" both become
// 20000. It is a bounded list on purpose. It does not try to make "1984" meet
// "Nineteen Eighty-Four", and "Sorcerer's" never meets "Philosopher's".
func foldTitleTokens(s string) string {
	toks := strings.Fields(s)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if long, ok := titleAbbreviations[t]; ok {
			out = append(out, long)
			continue
		}
		if n, ok := titleNumberValues[t]; ok {
			t = strconv.Itoa(n)
		}
		last := len(out) - 1
		switch {
		case last >= 0 && t == "thousand" && isDigits(out[last]):
			out[last] += "000"
			continue
		case last >= 0 && len(t) == 3 && isDigits(t) && len(out[last]) <= 3 && isDigits(out[last]):
			out[last] += t
			continue
		case last >= 0 && len(t) == 1 && isDigits(t) && t != "0" && len(out[last]) == 2 && out[last][1] == '0' && isDigits(out[last]):
			// tens then units: "20" "2" is 22.
			out[last] = out[last][:1] + t
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nonLatinAgainstLatin reports whether one of a and b is written in a script
// with no Latin letter in it and the other has Latin letters. A comparison of
// such a pair says nothing: a Russian or Japanese edition of a Latin catalogued
// book is the same book, not a different one. Text with no letters at all (a
// bare number) is neutral, and two non-Latin texts are compared as usual.
func nonLatinAgainstLatin(a, b string) bool {
	al, alat := scriptOf(a)
	bl, blat := scriptOf(b)
	return (al && !alat && blat) || (bl && !blat && alat)
}

// scriptOf reports whether s has any letter and whether any of them is Latin.
func scriptOf(s string) (hasLetter, hasLatin bool) {
	for _, r := range s {
		if unicode.IsLetter(r) {
			hasLetter = true
			if unicode.Is(unicode.Latin, r) {
				return true, true
			}
		}
	}
	return hasLetter, false
}

// dropLeadingArticle removes a leading "the", "a" or "an" from a folded title,
// unless that would leave nothing.
func dropLeadingArticle(s string) string {
	for _, art := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(s, art) && len(s) > len(art) {
			return s[len(art):]
		}
	}
	return s
}

const titleNumberWords = `one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve`

// seriesSuffixRe matches a numbered series suffix at the end of a title:
// ", Book 11", ": Book 11", ": Alex Cross, Book 11", " - Volume 3". The series
// name before the marker is optional and may not itself contain a separator, so
// "Mary, Mary: Alex Cross, Book 11" loses ": Alex Cross, Book 11" and keeps
// "Mary, Mary".
var seriesSuffixRe = regexp.MustCompile(`(?i)(?:\s*[,:;]|\s+[-\x{2013}\x{2014}])\s*(?:[^,:;]+,\s*)?(?:book|bk|vol|volume)\.?\s*(?:\d+(?:\.\d+)?|` + titleNumberWords + `)\s*$`)

// hashSuffixRe matches a trailing "#3".
var hashSuffixRe = regexp.MustCompile(`\s+#\s*\d+(?:\.\d+)?\s*$`)

func stripSeriesSuffix(title string) string {
	t := strings.TrimSpace(title)
	if loc := seriesSuffixRe.FindStringIndex(t); loc != nil && loc[0] > 0 {
		t = strings.TrimSpace(t[:loc[0]])
	} else if loc := hashSuffixRe.FindStringIndex(t); loc != nil && loc[0] > 0 {
		t = strings.TrimSpace(t[:loc[0]])
	}
	return t
}

var (
	seriesPrefixMarkerRe = regexp.MustCompile(`(?i)\b(?:book|bk|vol|volume)\.?\s*\d|\d\s*$|\s(?:i{1,3}|iv|v|vi{1,3}|ix|x|xi{1,3})$`)
)

// looksLikeSeriesPrefix reports whether a main title reads as a series name
// followed by a position ("The Wheel of Time, Book 1", "The Stormlight Archive
// 4", "The Dark Tower IV"), so that the subtitle after it is the real title.
func looksLikeSeriesPrefix(main string) bool {
	return seriesPrefixMarkerRe.MatchString(strings.TrimSpace(main))
}

// significantTitleTokens counts the words of s that carry meaning, leaving out
// the stop words titleMatch ignores. "A Novel" has one; "A New Hope" has two.
func significantTitleTokens(s string) int {
	n := 0
	for _, tok := range strings.Fields(textutil.FoldForSearch(s)) {
		if !titleStopwords[tok] {
			n++
		}
	}
	return n
}

// containsWholeWords reports whether the folded title haystack contains needle
// as a run of whole words.
func containsWholeWords(haystack, needle string) bool {
	if haystack == "" || needle == "" {
		return false
	}
	return strings.Contains(" "+haystack+" ", " "+needle+" ")
}

// contentTitleSupports reports whether file names the wanted book: some form of
// file equals some form of wanted, or file contains a form of wanted as whole
// words (so the short title "Dune" is supported by "Dune Messiah", and "Mary,
// Mary" by "Mary, Mary: Alex Cross, Book 11"). It does not say the reverse: a
// longer wanted title ("Dune Messiah") is not supported by the bare "Dune".
//
// fileMayBeInsideWanted adds that reverse reading: file counts when a form of it
// of at least two words sits inside the wanted title as whole words. The ebook
// check turns it on, because a dc:title is often the bare title of a book the
// catalogue lists under a long series dressed form. Two words, because a one
// word file title such as "It" sits inside far too many titles to say anything.
//
// An empty file title supports nothing.
func contentTitleSupports(wanted, file string, fileMayBeInsideWanted bool) bool {
	wf := contentTitleForms(wanted)
	ff := contentTitleForms(file)
	if len(wf) == 0 || len(ff) == 0 {
		return false
	}
	for _, w := range wf {
		for _, f := range ff {
			if w == f || containsWholeWords(f, w) {
				return true
			}
		}
	}
	if fileMayBeInsideWanted {
		for _, f := range ff {
			if len(strings.Fields(f)) < 2 {
				continue
			}
			for _, w := range wf {
				if containsWholeWords(w, f) {
					return true
				}
			}
		}
	}
	return false
}

// numberTokenRe is a number, a spelled out number or a roman numeral: the tokens that may
// follow "chapter", "part" and the like in a generic track title.
var (
	numberTokenRe      = regexp.MustCompile(`^(?:\d+|` + titleNumberWords + `|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety|hundred|zero|[ivxlc]+)$`)
	genericWordRe      = regexp.MustCompile(`^(?:chapter|track|disc|disk|cd|part|section|file)$`)
	bareNumberRe       = regexp.MustCompile(`^\d+(?: of \d+)?$`)
	genericStandalones = map[string]bool{
		"prologue": true, "epilogue": true, "introduction": true, "intro": true, "outro": true,
		"credits": true, "opening credits": true, "closing credits": true, "end credits": true,
		"foreword": true, "afterword": true, "preface": true, "dedication": true, "copyright": true,
		"acknowledgments": true, "acknowledgements": true, "acknowledgment": true, "acknowledgement": true,
		"about the author": true, "title page": true, "table of contents": true, "contents": true,
		"authors note": true, "author s note": true,
	}
)

// genericTrackTitle reports whether a track title names no work at all:
// "Chapter 12", "Track 03", "Disc 2", "Part IV", a bare number, "Prologue",
// "Epilogue", "Introduction", "Credits". Such a title says where in a recording
// a file sits, not which book it belongs to, so it is never evidence either way.
func genericTrackTitle(title string) bool {
	k := textutil.FoldForSearch(title)
	if k == "" {
		return false
	}
	if bareNumberRe.MatchString(k) || genericStandalones[k] {
		return true
	}
	toks := strings.Fields(k)
	if len(toks) == 0 || !genericWordRe.MatchString(toks[0]) {
		return false
	}
	rest := toks[1:]
	// An optional "of N" total.
	if n := len(rest); n >= 2 && rest[n-2] == "of" && bareNumberRe.MatchString(rest[n-1]) {
		rest = rest[:n-2]
	}
	if len(rest) > 4 {
		return false
	}
	for _, t := range rest {
		if !numberTokenRe.MatchString(t) {
			return false
		}
	}
	return true
}

var promoTokens = map[string]bool{
	"excerpt": true, "excerpts": true, "preview": true, "sample": true, "sampler": true,
	"bonus": true, "interview": true, "teaser": true, "trailer": true,
}

// promoTrackTitle reports whether a title is promotional material that rides
// along in an audiobook folder: an excerpt of another book, a preview, a
// sample, a bonus track, an author interview, an "also by" list, a teaser or a
// trailer. Like a generic title it is never evidence about which book a file
// belongs to. The match is on whole words, so "Examples of Grace" is not one.
func promoTrackTitle(title string) bool {
	k := textutil.FoldForSearch(title)
	if k == "" {
		return false
	}
	toks := strings.Fields(k)
	for i, t := range toks {
		if promoTokens[t] {
			return true
		}
		if t == "also" && i+1 < len(toks) && toks[i+1] == "by" {
			return true
		}
	}
	return false
}

var (
	explicitVolumeRe = regexp.MustCompile(`(?i)\b(?:vol|volume|bk|book)\b\.?\s*(\d+(?:\.\d+)?)|#\s*(\d+(?:\.\d+)?)`)
	trailingNumberRe = regexp.MustCompile(`^(.*?)[\s.,:;\-]*(\d+(?:\.\d+)?)\s*$`)
)

// volumeStem splits a title into the series words and the volume number it
// carries: the number after an explicit marker ("Book 3", "Vol. 2", "(Volume
// 1)", "#4") or, failing that, a bare number ending the main title ("NYPD Red
// 3"). The stem is folded and has any subtitle removed. ok is false when there
// is no number or nothing before it, so "1984" and "2001: A Space Odyssey" are
// titles, not volumes.
func volumeStem(title string) (stem, num string, ok bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", "", false
	}
	if m := explicitVolumeRe.FindStringSubmatchIndex(t); m != nil {
		switch {
		case m[2] >= 0:
			num = t[m[2]:m[3]]
		case m[4] >= 0:
			num = t[m[4]:m[5]]
		}
		rest := t[:m[0]] + " " + t[m[1]:]
		main, _ := indexer.SplitSubtitle(rest)
		if forms := titleFoldVariants(main); len(forms) > 0 && forms[0] != "" && num != "" {
			return forms[0], num, true
		}
		return "", "", false
	}
	main, _ := indexer.SplitSubtitle(t)
	m := trailingNumberRe.FindStringSubmatch(main)
	if m == nil {
		return "", "", false
	}
	forms := titleFoldVariants(m[1])
	if len(forms) == 0 || forms[0] == "" {
		return "", "", false
	}
	return forms[0], m[2], true
}

// otherVolumeOfSameWork reports whether file is provably another volume of the
// series wanted belongs to: both carry a volume number, the words before the
// number are the same, and the numbers differ ("NYPD Red 3" and "NYPD Red 4",
// "Dominion of Blades (Volume 1)" and "Dominion of Blades (Volume 2)"). Anything
// less, such as different series words, a missing number on either side, or a
// number that is part of a title ("Fahrenheit 451"), is not a conflict.
//
// Plain title similarity cannot see this: the volumes of one series differ by a
// single character, so each one reads as supporting the others.
func otherVolumeOfSameWork(wanted, file string) bool {
	ws, wn, ok := volumeStem(wanted)
	if !ok {
		return false
	}
	fs, fn, ok := volumeStem(file)
	if !ok || ws != fs {
		return false
	}
	return !seriesmatch.SamePosition(wn, fn)
}
