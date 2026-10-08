package seriesmatch

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// splitEditionMarkerRe matches a "Part N" marker, with an optional "of M":
// "Part 1", "Pt. 2", "Part 1 of 3". It is the same pattern the importer and
// the series diff (#2524) use for the same marker.
var splitEditionMarkerRe = regexp.MustCompile(`(?i)\b(?:part|pt)\.?\s*\d+(?:\.\d+)?(?:\s+of\s+\d+)?\b`)

// SplitEditionPartOf reports whether the book titled partTitle at
// partPosition is one part of a split edition of the book titled wholeTitle
// at wholePosition: Stormlight's "The Way of Kings, Part 1" at 1.1 under
// "The Way of Kings" at 1.
//
// It is the two signal rule #2524 introduced for the Hardcover series diff,
// applied to rows already in the library (#3048). Both signals are required:
//
//  1. partPosition is fractional and its integer floor is wholePosition.
//  2. partTitle is wholeTitle followed by a "Part N" marker. A subtitle after
//     the marker is ignored ("The Great Hunt, Part 2 of 2: New Threads in
//     the Pattern").
//
// Position alone is not enough: a real novella sits at a fractional position
// too (Edgedancer at 2.5 beside Words of Radiance at 2) and has no marker. A
// title alone is not enough either: a series made only of split parts, with
// no whole at the integer position, has nothing to be covered by.
func SplitEditionPartOf(partTitle, partPosition, wholeTitle, wholePosition string) bool {
	floor, ok := fractionalFloor(partPosition)
	if !ok || !SamePosition(floor, wholePosition) {
		return false
	}
	loc := splitEditionMarkerRe.FindStringIndex(partTitle)
	if loc == nil {
		return false
	}
	prefix := strings.TrimRight(strings.TrimSpace(partTitle[:loc[0]]), " ,:;-")
	if prefix == "" {
		return false
	}
	whole := CleanTitle(wholeTitle)
	return whole != "" && CleanTitle(prefix) == whole
}

// fractionalFloor returns the integer floor of pos when pos is a fractional
// number ("1.1" gives "1"), and false for a whole number, a blank or anything
// that does not parse.
func fractionalFloor(pos string) (string, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(pos), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	floor := math.Floor(f)
	if math.Abs(f-floor) < 1e-9 {
		return "", false
	}
	return strconv.FormatFloat(floor, 'f', -1, 64), true
}
