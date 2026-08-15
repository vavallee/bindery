// Package seriesmatch contains title and position matching helpers shared by
// Audiobookshelf import and user-triggered series linking.
package seriesmatch

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/indexer"
	"golang.org/x/text/unicode/norm"
)

func SamePosition(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	af, aerr := strconv.ParseFloat(a, 64)
	bf, berr := strconv.ParseFloat(b, 64)
	return aerr == nil && berr == nil && math.Abs(af-bf) < 0.001
}

// NormalizeSeriesName reduces a series name for comparison: the canonical
// dedup key, minus a redundant trailing collective noun ("… Series", "…
// Trilogy").
//
// It builds on CanonicalDedupKey rather than NormalizeTitleForDedup so that it
// is a strict WIDENING of the key ABS uses to LOOK UP an existing series
// (findSeriesByTitle → normalizeTitle → CanonicalDedupKey). The two differ by
// StripBracketSuffixes, and before #1648 neither was a subset of the other, so
// both directions leaked: "The Expanse Series" never reached the promotion
// check it exists for, while "The Expanse [Audiobook]" matched on lookup and
// was then refused promotion. Widening keeps the implication that matters —
// if a series matched on lookup, it can still be promoted.
func NormalizeSeriesName(name string) string {
	normalized := indexer.CanonicalDedupKey(strings.TrimSpace(name))
	if normalized == "" {
		return ""
	}
	suffixes := map[string]struct{}{
		"series":     {},
		"trilogy":    {},
		"saga":       {},
		"chronicles": {},
		"cycle":      {},
		"books":      {},
		"novels":     {},
	}
	words := strings.Fields(normalized)
	if len(words) > 1 {
		if _, ok := suffixes[words[len(words)-1]]; ok {
			words = words[:len(words)-1]
		}
	}
	return strings.Join(words, " ")
}

func TitleScore(a, b string) int {
	cleanA := CleanTitle(a)
	cleanB := CleanTitle(b)
	if cleanA == "" || cleanB == "" {
		return 0
	}
	return max(
		safeFuzzyScore(tokenSetRatio, cleanA, cleanB),
		safeFuzzyScore(tokenSortRatio, cleanA, cleanB),
		safeFuzzyScore(ratio, cleanA, cleanB),
		safeFuzzyScore(partialRatio, cleanA, cleanB),
	)
}

func safeFuzzyScore(score func(string, string) int, a, b string) (value int) {
	defer func() {
		if recover() != nil {
			value = 0
		}
	}()
	return score(a, b)
}

// Ratio scores whole-string similarity between two already-cleaned titles,
// unlike TitleScore's PartialRatio/TokenSetRatio components, which score a
// substring match as a perfect 100 regardless of how much surrounding text
// the longer string carries. Exported so callers that need TitleScore's raw
// non-substring-friendly component — e.g. breaking a tie between two
// candidates that both score 100 on TitleScore — don't need their own
// fuzzy-matcher import; the dependency stays encapsulated here.
func Ratio(a, b string) int {
	return safeFuzzyScore(ratio, a, b)
}

func CleanTitle(title string) string {
	title = norm.NFC.String(strings.TrimSpace(title))
	title = strings.ToLower(title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(' ')
		}
	}
	noise := map[string]struct{}{
		"a":     {},
		"an":    {},
		"the":   {},
		"novel": {},
		"book":  {},
	}
	words := strings.Fields(b.String())
	out := words[:0]
	for _, word := range words {
		if _, ok := noise[word]; ok {
			continue
		}
		out = append(out, word)
	}
	return strings.Join(out, " ")
}

// volumeNumberRe matches an EXPLICIT volume marker followed by a number:
// "Vol. 3", "Volume 3", "Book 3", "Part 3", "#3". A bare trailing number is
// deliberately not matched — "Fahrenheit 451" and "Catch 22" are titles, not
// volume three hundred and something.
var volumeNumberRe = regexp.MustCompile(`(?i)(?:\b(?:vol|volume|bk|book|part|pt)\b\.?\s*|#\s*)(\d+(?:\.\d+)?)`)

// VolumeNumber returns the explicit volume number carried by a title, if any.
// The bool reports whether one was found; a title with several markers yields
// the first.
func VolumeNumber(title string) (string, bool) {
	m := volumeNumberRe.FindStringSubmatch(title)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// DifferentVolumes reports whether two titles carry explicit volume numbers
// that disagree — i.e. they are provably different books, however similar the
// strings look.
//
// This exists because fuzzy title similarity cannot separate the volumes of a
// light novel or manga series: they differ by one number in an otherwise
// identical string, so every pair scores far above any workable threshold.
// Measured with TitleScore: "Trapped in a Dating Sim Vol. 1" against "Vol. 13"
// scores 100 (PartialRatio sees "vol 1" as a substring of "vol 13"), "…Vol. 1"
// against "…Vol. 2" scores 98, "The Mimosa Confessions Vol. 1" against "Vol. 2"
// scores 96, "Overlord, Vol. 1" against "Vol. 9" scores 93.
//
// A caller using TitleScore to decide "is this the same book?" must consult
// this first, or an entire series collapses onto its first volume.
//
// Deliberately conservative: it returns true ONLY when BOTH titles carry an
// explicit number and the two disagree. A title with no volume marker tells us
// nothing — an omnibus, a re-issue, or just sloppy metadata — so those keep
// falling through to the similarity score exactly as before.
func DifferentVolumes(a, b string) bool {
	av, aok := VolumeNumber(a)
	if !aok {
		return false
	}
	bv, bok := VolumeNumber(b)
	if !bok {
		return false
	}
	return !SamePosition(av, bv)
}
