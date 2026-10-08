package api

import (
	"unicode"

	"github.com/vavallee/bindery/internal/models"
)

// titleScripts are the writing systems titleScript tells apart. The question
// it answers is only whether a title is plainly not in the script the
// author's majority-language titles are in (#3091), so a coarse set is
// enough.
//
// Han, Hiragana and Katakana are one bucket, "CJK": a Japanese title mixes all
// three ("ノルウェイの森" is mostly katakana, "騎士団長殺し" all kanji), so
// telling them apart would judge an author's own titles foreign to each other.
// Hangul stays separate. Korean titles are written in it almost entirely, so a
// Japanese or Chinese translation in a Korean catalogue is plainly foreign,
// and a Korean translation in a Japanese one likewise.
var titleScripts = []struct {
	name  string
	table *unicode.RangeTable
}{
	{"Latin", unicode.Latin},
	{"Cyrillic", unicode.Cyrillic},
	{"Greek", unicode.Greek},
	{"Arabic", unicode.Arabic},
	{"Hebrew", unicode.Hebrew},
	{"CJK", unicode.Han},
	{"CJK", unicode.Hiragana},
	{"CJK", unicode.Katakana},
	{"Hangul", unicode.Hangul},
	{"Thai", unicode.Thai},
	{"Devanagari", unicode.Devanagari},
	{"Armenian", unicode.Armenian},
	{"Georgian", unicode.Georgian},
}

// titleScript returns the writing system most of title's letters are in, or
// "" when it has no letters from any of titleScripts (digits only, say).
func titleScript(title string) string {
	counts := make(map[string]int, 2)
	for _, r := range title {
		if !unicode.IsLetter(r) {
			continue
		}
		for _, s := range titleScripts {
			if unicode.Is(s.table, r) {
				counts[s.name]++
				break
			}
		}
	}
	return mostCommon(counts)
}

// majorityTitleScript returns the script most of the titles in language are
// written in, or "" when none of them has a recognisable one.
func majorityTitleScript(books []models.Book, language string) string {
	counts := make(map[string]int, 2)
	for _, b := range books {
		if b.Language != language {
			continue
		}
		if script := titleScript(b.Title); script != "" {
			counts[script]++
		}
	}
	return mostCommon(counts)
}

// mostCommon returns the key with the highest count, the alphabetically first
// on a tie so the answer does not depend on map order.
func mostCommon(counts map[string]int) string {
	best, bestN := "", 0
	for key, n := range counts {
		if n > bestN || (n == bestN && key < best) {
			best, bestN = key, n
		}
	}
	return best
}
