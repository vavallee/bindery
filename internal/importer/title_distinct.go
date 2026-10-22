package importer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/textutil"
)

// titlesNameDifferentWorks reports whether two titles that already look alike
// to a similarity score are provably two different works, because each has
// words the other lacks and none of those words is close to any of the other's.
//
// Jaro-Winkler rewards a long shared prefix, so series entries clear any
// threshold on the strength of the series name alone: "Harry Potter en de
// Relieken van de Dood" against "Harry Potter en de Orde van de Feniks" scores
// 0.929, and what separates the books is only "relieken dood" against "orde
// feniks" (#3388). The score cannot say that, and the volume rule cannot
// either when the catalogue title carries no number.
//
// The words both titles share are set aside, then each side's remaining words
// are compared. The titles are different works only when BOTH sides have
// remaining words and no remaining word of one is near a remaining word of
// the other. So these are not different works:
//
//   - a subtitle added or dropped ("Dune" and "Dune Messiah" leave nothing on
//     one side, "The Final Empire" inside "Mistborn: The Final Empire" the
//     same), because one title's words contained in the other's is one work;
//   - a typo or spelling variant ("Fire" and "Fyre", "Colour" and "Color",
//     "Punk'd" and "Punkd", which fold to one word), because near words count
//     as the same word;
//   - a word order or article difference, which leave nothing on either side;
//   - a title of one or two words, which has no remaining words to speak of
//     unless it really does differ.
//
// Words are the title-comparison tokens (titleSigTokens), so case, accents and
// punctuation are already folded and the English stopwords are gone. Tokens
// containing a digit are ignored: the numbers of a series are libraryVolumeConflict's
// business and a different number on each side must not read as a different
// work here. A title in a script written without spaces is one token, so it is
// compared whole and counts as the same work when one contains the other or
// they are a character apart.
//
// A false negative (two titles of one work read as different) leaves a file
// unmatched, which the Import page handles; a false positive attached a file
// to a book it is not. This rule is therefore deliberately cautious about
// ruling a pair out, and wide about what it counts as near.
func titlesNameDifferentWorks(a, b string) bool {
	aOnly, bOnly := titleExclusiveWords(a, b)
	if len(aOnly) == 0 || len(bOnly) == 0 {
		return false
	}
	for _, x := range aOnly {
		for _, y := range bOnly {
			if titleWordsNear(x, y) {
				return false
			}
		}
	}
	return true
}

// titleExclusiveWords returns the digit-free significant words each title has
// that the other does not, in order of appearance.
func titleExclusiveWords(a, b string) (aOnly, bOnly []string) {
	aWords, bWords := guardWords(withoutAnnotations(a)), guardWords(withoutAnnotations(b))
	aSet, bSet := make(map[string]bool, len(aWords)), make(map[string]bool, len(bWords))
	for _, w := range aWords {
		aSet[w] = true
	}
	for _, w := range bWords {
		bSet[w] = true
	}
	collect := func(words []string, other map[string]bool) []string {
		var out []string
		seen := make(map[string]bool, len(words))
		for _, w := range words {
			if !other[w] && !seen[w] && !titleFillerWords[w] && !strings.ContainsFunc(w, unicode.IsDigit) {
				seen[w] = true
				out = append(out, w)
			}
		}
		return out
	}
	return collect(aWords, bSet), collect(bWords, aSet)
}

// guardWords is a title's significant words in the guard's own alphabet: the
// title-comparison tokens, further folded so a ligature or a letter NFD leaves
// whole reads as its ASCII spelling ("Æsop" and "Aesop", "Øya" and "Oya",
// "ﬁre" and "fire"), and mapped through titleWordEquivalents. The extra fold
// belongs to this guard only, which can only add rejections, so a spelling it
// fails to equate costs a file its match and never gives it a wrong one.
func guardWords(title string) []string {
	toks := titleSigTokens(title)
	for i, w := range toks {
		w = textutil.FoldNonDecomposableLatin(strings.ToLower(norm.NFKC.String(w)))
		if c, ok := titleWordEquivalents[w]; ok {
			w = c
		}
		toks[i] = w
	}
	return toks
}

// titleWordEquivalents maps words that are the same word to one spelling:
// abbreviations a catalogue and a file name write differently ("Dr. Jekyll and
// Mr. Hyde" and "Doctor Jekyll and Mister Hyde"), and the one regional pair
// that names a famous work two ways (Philosopher's Stone in the UK, Sorcerer's
// Stone in the US, with the possessive's apostrophe already folded away). Keep
// this minimal. It is not a thesaurus: a word belongs here only when two
// editions of one book are known to disagree on it and edit distance cannot
// bridge them. Every other alternate title stays unmatched and waits on Import.
var titleWordEquivalents = map[string]string{
	"dr": "doctor", "mr": "mister", "mrs": "missus", "st": "saint", "jr": "junior", "vs": "versus",
	"philosopher": "sorcerer", "philosophers": "sorcerers",
}

// annotationRe matches a bracketed run of a title: the series note of
// "Rhythm of War (The Stormlight Archive, Book 4)" or the tag of "[Unabridged]".
var annotationRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// withoutAnnotations drops the bracketed runs of a title, unless that would
// leave nothing. They are where a catalogue puts its series and edition notes
// and a file name its tags, none of which name the work.
func withoutAnnotations(s string) string {
	if out := strings.TrimSpace(annotationRe.ReplaceAllString(s, " ")); out != "" {
		return out
	}
	return s
}

// titleFillerWords are words that label an edition, a position or a file and
// never say which work it is, so one side having them is no evidence of a
// different work: "Rhythm of War Part 1" is the book "Rhythm of War, Book 4".
// Lowercase and folded, as titleSigTokens returns them.
var titleFillerWords = map[string]bool{
	"book": true, "books": true, "part": true, "parts": true, "volume": true, "vol": true,
	"disc": true, "disk": true, "cd": true, "chapter": true, "track": true, "series": true,
	"novel": true, "edition": true, "unabridged": true, "abridged": true, "audiobook": true,
	"audio": true, "stereo": true, "mp3": true, "m4b": true, "epub": true,
}

// titleWordsNear reports whether two distinct title words are plausibly one
// word: one holds the other (a plural, a compound, a subtitle written without a
// space), or they are a spelling slip apart. A word under four characters has
// no slip to forgive.
func titleWordsNear(x, y string) bool {
	xl, yl := utf8.RuneCountInString(x), utf8.RuneCountInString(y)
	short, long := x, y
	shortLen := xl
	if yl < xl {
		short, long = y, x
		shortLen = yl
	}
	if shortLen >= 2 && strings.HasPrefix(long, short) {
		return true
	}
	if shortLen >= 3 && strings.Contains(long, short) {
		return true
	}
	if shortLen < 4 {
		return false
	}
	limit := 1
	if shortLen >= 8 {
		limit = 2
	}
	return runeEditDistanceWithin(x, y, limit)
}

// runeEditDistanceWithin reports whether the Levenshtein distance between x
// and y, counted in runes, is at most limit.
func runeEditDistanceWithin(x, y string, limit int) bool {
	a, b := []rune(x), []rune(y)
	if d := len(a) - len(b); d > limit || -d > limit {
		return false
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > limit {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(b)] <= limit
}
