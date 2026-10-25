package newznab

import (
	"regexp"
	"strconv"
	"strings"
)

// numberWords are the spelled out numbers that FoldNumberWords reads. One to
// twenty only: past that the spelling is compound ("twenty-one") and a title
// that uses it is rare enough not to be worth the ambiguity. Ordinals ("First
// Book") are left alone on purpose; "first" and "1st" are not interchangeable
// in every title the way "Book One" and "Book 1" are.
var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20,
}

// numberWordRe finds a number word that directly follows a volume style word.
// The marker is required: a bare "One" or "Two" is far too often a real word
// of a title ("Number One", "Two Towers", "Catch 22"), while "Book One" or
// "Part Two" is a position in a set. The separator between the two may be
// whitespace, a dot or an underscore or hyphen, so the same pattern reads a
// metadata title ("Vol. One") and a raw release name ("Path.of.the.Deathless.Book.One").
var numberWordRe = regexp.MustCompile(
	`(?i)\b(book|volume|vol|part|episode|season|year|arc)(\.?[\s._-]+)(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\b`)

// twentyCompoundRe is what follows "twenty" when it is the start of "twenty
// one" to "twenty nine". Folding only the first word would leave "20 One".
var twentyCompoundRe = regexp.MustCompile(`(?i)^[\s._-]+(?:one|two|three|four|five|six|seven|eight|nine)\b`)

// FoldNumberWords rewrites a spelled out number that follows a volume style
// word as digits: "Path of the Deathless Book One" becomes "Path of the
// Deathless Book 1". Everything else, including the original separator and
// casing of the rest of the string, is left as it was.
//
// It is meant to be applied to BOTH sides of a comparison (the wanted title and
// the release name), so a metadata title that spells the number out and a
// release that uses the digit compare equal. Because both sides are folded the
// same way it can only make "Book One" equal to "Book 1"; it never makes
// "Book One" equal to "Book 10" or "Book 2".
//
// It also builds the digit variant of an outgoing query, see BookSearch.
func FoldNumberWords(s string) string {
	locs := numberWordRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range locs {
		word := strings.ToLower(s[m[6]:m[7]])
		if word == "twenty" && twentyCompoundRe.MatchString(s[m[1]:]) {
			continue
		}
		b.WriteString(s[last:m[6]])
		b.WriteString(strconv.Itoa(numberWords[word]))
		last = m[7]
	}
	b.WriteString(s[last:])
	return b.String()
}
