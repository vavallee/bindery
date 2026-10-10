package importer

import (
	"regexp"

	"github.com/vavallee/bindery/internal/seriesmatch"
)

// volumeTitles returns a and b ready for a volume-number comparison.
//
// seriesmatch.VolumeNumber counts "Part N" as a volume marker, which is right
// when both sides say "Part": "The Way of Kings, Part 1" and "Part 2" are the
// two halves of a split edition, and different books. Beside a library file it
// is usually something else. A multi-file audiobook is delivered as "Title
// Part 1.mp3", "Title Part 2.mp3" or as Part 1/ and Part 2/ folders, and there
// the number counts files, not books. Compared against a wanted "Rhythm of War
// (The Stormlight Archive, Book 4)", the "Part 1" read as volume 1 and vetoed
// the book's own files (#2810 review). So a Part marker on one side only is
// dropped before the comparison, and kept when both sides carry one.
//
// partMarkerRe/stripPartMarker moved to seriesmatch.HasPartMarker /
// seriesmatch.StripPartMarker (#2524), which also needs them to recognize a
// split-edition catalogue row — one regex, one package, instead of two.
func volumeTitles(a, b string) (string, string) {
	ap, bp := seriesmatch.HasPartMarker(a), seriesmatch.HasPartMarker(b)
	switch {
	case ap && !bp:
		a = seriesmatch.StripPartMarker(a)
	case bp && !ap:
		b = seriesmatch.StripPartMarker(b)
	}
	return a, b
}

// differentVolumes is volumesDisagree after volumeTitles: true only when both
// titles carry a volume number and the numbers disagree.
func differentVolumes(a, b string) bool {
	a, b = volumeTitles(a, b)
	return volumesDisagree(a, b)
}

// volumesDisagree is seriesmatch.DifferentVolumes widened to a bare number
// that is followed by a subtitle (see differentNumberedStems).
func volumesDisagree(a, b string) bool {
	return seriesmatch.DifferentVolumes(a, b) || differentNumberedStems(a, b)
}

// stemNumberRe splits a title at its first standalone number: the words before
// it and the number. Standalone means no letter or digit touches it on either
// side, so the "8" in "The 8th Habit" is not one.
var stemNumberRe = regexp.MustCompile(`^(.*?)(?:^|[^\p{L}\p{N}])(\d+(?:\.\d+)?)(?:$|[^\p{L}\p{N}])`)

// stemmedNumber returns the cleaned words before s's first standalone number
// and the number. ok is false when there is no such number or nothing but
// noise words precede it: "2001: A Space Odyssey" and "The 7 Habits" open
// with their number, which names the book rather than a place in a series.
func stemmedNumber(s string) (stem, num string, ok bool) {
	m := stemNumberRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	stem = seriesmatch.CleanTitle(m[1])
	if stem == "" {
		return "", "", false
	}
	return stem, m[2], true
}

// differentNumberedStems reports whether two titles are the same words
// followed by different numbers, whatever comes after the number: "The Primal
// Hunter 3" against "The Primal Hunter 17: A LitRPG Adventure".
//
// seriesmatch.DifferentVolumes reads a bare number only at the END of a title,
// so a subtitle after it hid the number completely. One author's catalogue
// carries "The Primal Hunter 3", "The Primal Hunter 9: A LitRPG Adventure" and
// "The Primal Hunter 7 - A LitRPG Adventure" side by side, and with nothing to
// compare the two shared words "primal" and "hunter" carried the match: volume
// 3's audiobook was bound to volume 17, and volume 3's own import then wrote
// nothing because book_files.path is unique (#2934).
//
// Requiring identical words before the number keeps this as narrow as the
// trailing rule: "Fahrenheit 451" against "Catch 22" has different stems, and
// a title against itself with or without its subtitle has the same number.
func differentNumberedStems(a, b string) bool {
	as, an, aok := stemmedNumber(a)
	if !aok {
		return false
	}
	bs, bn, bok := stemmedNumber(b)
	if !bok || as != bs {
		return false
	}
	return !seriesmatch.SamePosition(an, bn)
}

// trailingDigitRe reports a title that ends in a number, the bare-number
// spelling seriesmatch.DifferentVolumes compares ("Defiance of the Fall 01").
var trailingDigitRe = regexp.MustCompile(`\d\s*$`)

// carriesVolumeNumber reports whether s has a number volumesDisagree could
// compare: an explicit marker ("Vol. 3", "Book 3", "#3"), a trailing one, or
// one after the series words and before a subtitle ("The Primal Hunter 3 A
// LitRPG Adventure").
func carriesVolumeNumber(s string) bool {
	if _, ok := seriesmatch.VolumeNumber(s); ok {
		return true
	}
	if trailingDigitRe.MatchString(s) {
		return true
	}
	_, _, ok := stemmedNumber(s)
	return ok
}

// libraryVolumeConflict reports whether a library file is provably a different
// volume of a series from the wanted title, so no title similarity may pair
// them. It is the one volume rule for every matcher that pairs a library file
// with a catalogue book: FindExisting on the add path (#2810), the library
// scan's title tier (#2860) and the scan's adoption suggestions.
//
// fileTitle is what the file itself says (its name, or its tags in the scan);
// folderTitle is the cleaned book folder name, "" when the file has none.
//
// The volume comes from the book folder when the folder carries a number, and
// from the file otherwise. A numbered folder is the better evidence on both
// sides of #2810: in a Libation layout ("Defiance of the Fall 01/Defiance of
// the Fall_B094JZMCJX_….m4b") it is the only place the number appears, and
// beside track files named "Defiance of the Fall 01.mp3" inside "Defiance of
// the Fall 7" the filename's number counts tracks, so letting it veto would
// lose the book's own files. Either way the comparison is
// volumesDisagree after volumeTitles, so a one-sided "Part N"
// never stands in for a series position, and a number that is part of a
// title ("Fahrenheit 451", "Catch-22") never vetoes its own book.
//
// A file title that normalises to the wanted title is never a conflict, which
// keeps titleMatch's exact fast path ahead of its veto.
func libraryVolumeConflict(fileTitle, folderTitle, wanted string) bool {
	if wanted == "" {
		return false
	}
	folder, w := volumeTitles(folderTitle, wanted)
	if carriesVolumeNumber(folder) {
		return volumesDisagree(folder, w)
	}
	if fileTitle == "" || normalizeTitle(fileTitle) == normalizeTitle(wanted) {
		return false
	}
	return differentVolumes(fileTitle, wanted)
}
