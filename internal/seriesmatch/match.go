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

// TitleScore scores two titles for similarity in [0, 100].
//
// It weights its four components the way RapidFuzz's WRatio does (MIT, ideas
// only, no code taken), rather than taking a flat maximum of all four. The flat
// maximum let partialRatio decide every comparison where one title contains the
// other verbatim, and partialRatio returns 100 for exactly that case by design.
// So a title scored a perfect 100 against a different book:
//
//	TitleScore("Dune", "Dune Messiah")     = 100
//	TitleScore("Vol. 1", "Vol. 13")        = 100
//	TitleScore("The Hobbit (Illustrated)", "The Hobbit") = 100
//
// The weighting makes the length difference pay for itself. When the two are of
// comparable length there is no containment story worth telling, so partialRatio
// is excluded outright and the token ratios are discounted. When one is much
// longer, partialRatio is admitted but scaled down, hard, and the more extreme
// the length gap the harder: a four-character title inside a forty-character one
// says very little about whether they are the same work.
//
// This does NOT make TitleScore a decision. A high score on a containment still
// happens and still should: "The Hobbit" really is inside "The Hobbit
// (Illustrated Edition)". Distinguishing that from "Dune" inside "Dune Messiah"
// needs the runner-up gap in BestWithGap, because the evidence is not in the
// pair, it is in whether something else scored nearly as well.
func TitleScore(a, b string) int {
	cleanA := CleanTitle(a)
	cleanB := CleanTitle(b)
	if cleanA == "" || cleanB == "" {
		return 0
	}

	whole := safeFuzzyScore(ratio, cleanA, cleanB)
	tokens := max(
		safeFuzzyScore(tokenSetRatio, cleanA, cleanB),
		safeFuzzyScore(tokenSortRatio, cleanA, cleanB),
	)

	shorter, longer := len([]rune(cleanA)), len([]rune(cleanB))
	if shorter > longer {
		shorter, longer = longer, shorter
	}
	if shorter == 0 {
		return 0
	}

	// Comparable length: a containment match carries no information the whole
	// string ratio has not already scored, so partialRatio is left out.
	if float64(longer)/float64(shorter) < titleLengthRatioCutoff {
		return max(whole, scaleScore(tokens, titleTokenScale))
	}

	// Different lengths: admit the containment, discounted by how extreme the
	// difference is.
	partialScale := titlePartialScaleNear
	if float64(longer)/float64(shorter) >= titleLengthRatioFar {
		partialScale = titlePartialScaleFar
	}
	score := max(
		whole,
		scaleScore(safeFuzzyScore(partialRatio, cleanA, cleanB), partialScale),
		scaleScore(tokens, titleTokenScale*partialScale),
	)
	return max(score, qualifierOnlyScore(a, b))
}

// qualifierOnlyScore returns titleQualifierScore when the two titles are the
// same work distinguished only by a trailing edition qualifier, and 0
// otherwise.
//
// "The Hobbit (Illustrated Edition)" and "The Hobbit" are one book, and the
// length weighting above scores them 90 because one contains the other and is
// three times longer. That is the right answer for "Dune" inside "Dune
// Messiah" and the wrong one here, and nothing in the character sequence tells
// the two cases apart: the difference is that a bracketed suffix is a
// CATALOGUING note rather than part of the title.
//
// So the qualifier is removed from both sides and, if what remains is
// identical, the pair earns a floor just short of a perfect match. Short of it
// deliberately: beets down-weights a parenthetical rather than deleting it, on
// the grounds that "(Abridged)" really can be a different product, so a
// qualifier still costs something and an exact title still wins.
func qualifierOnlyScore(a, b string) int {
	// Stripping must never be allowed to merge two volumes of one series.
	// "Overlord, Vol. 1" and "Overlord, Vol. 13" both reduce to "overlord",
	// and awarding them a near-perfect score is precisely the #2343 collapse
	// this file already fights elsewhere.
	if DifferentVolumes(a, b) {
		return 0
	}
	sa, sb := stripQualifiers(a), stripQualifiers(b)
	if sa == "" || sa != sb {
		return 0
	}
	return titleQualifierScore
}

// stripQualifiers removes the trailing notes a cataloguer or a scanner appends
// to a title, and cleans what is left. "The Hobbit [Unabridged] (Illustrated
// Edition)" and "The Hobbit" both reduce to "hobbit".
//
// It also removes a trailing series-position segment, because Calibre and
// Audiobookshelf routinely write one into the title itself: "The Way of Kings:
// The Stormlight Archive, Book One" is the same book as "The Way of Kings".
// The segment has to CARRY a position marker to be removed, so an ordinary
// subtitle survives: "Mistborn: The Final Empire" keeps its subtitle, because
// dropping it would make it indistinguishable from "Mistborn: The Well of
// Ascension".
func stripQualifiers(title string) string {
	stripped := indexer.StripBracketSuffixes(strings.TrimSpace(title))
	for {
		trimmed := parenSuffixRe.ReplaceAllString(stripped, "")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == stripped || trimmed == "" {
			break
		}
		stripped = trimmed
	}
	if trimmed := seriesPositionSuffixRe.ReplaceAllString(stripped, ""); strings.TrimSpace(trimmed) != "" {
		stripped = strings.TrimSpace(trimmed)
	}
	return CleanTitle(stripped)
}

// parenSuffixRe matches one trailing parenthesised qualifier. Anchored at the
// end so a parenthetical in the middle of a title, which is part of the title
// rather than a note about it, is left alone.
var parenSuffixRe = regexp.MustCompile(`\s*\([^()]*\)\s*$`)

// seriesPositionSuffixRe matches a trailing segment, introduced by a colon or
// comma, that carries an explicit series position: ": The Stormlight Archive,
// Book One", ", Book 3", ": Discworld Novel 5". The position marker is what
// makes it a note about the title rather than part of it, so a plain subtitle
// with no marker is left alone.
var seriesPositionSuffixRe = regexp.MustCompile(
	`(?i)\s*[:,]\s*.*\b(?:book|vol|volume|bk|part|pt)\b\.?\s*` +
		`(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s*$`)

// titleQualifierScore is the floor for two titles that differ only by an
// edition qualifier. Below 100 so an exact match still beats it.
const titleQualifierScore = 97

// The WRatio constants. Named rather than inlined because every one of them is
// a threshold someone will want to move, and moving one without the fixtures in
// match_test.go is how a scoring change ships a wrong grab.
const (
	titleLengthRatioCutoff = 1.5  // below this, partialRatio is not consulted
	titleLengthRatioFar    = 8.0  // above this, the discount gets much harsher
	titleTokenScale        = 0.95 // token ratios never quite beat a whole-string match
	titlePartialScaleNear  = 0.90
	titlePartialScaleFar   = 0.60
)

// scaleScore multiplies a [0,100] score by a scale and rounds to nearest, so
// the discount cannot be lost to truncation on a boundary.
func scaleScore(score int, scale float64) int {
	return int(float64(score)*scale + 0.5)
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
