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

// differentVolumes is seriesmatch.DifferentVolumes after volumeTitles: true
// only when both titles carry a volume number and the numbers disagree.
func differentVolumes(a, b string) bool {
	a, b = volumeTitles(a, b)
	return seriesmatch.DifferentVolumes(a, b)
}

// trailingDigitRe reports a title that ends in a number, the bare-number
// spelling seriesmatch.DifferentVolumes compares ("Defiance of the Fall 01").
var trailingDigitRe = regexp.MustCompile(`\d\s*$`)

// carriesVolumeNumber reports whether s has a number DifferentVolumes could
// compare: an explicit marker ("Vol. 3", "Book 3", "#3") or a trailing one.
func carriesVolumeNumber(s string) bool {
	if _, ok := seriesmatch.VolumeNumber(s); ok {
		return true
	}
	return trailingDigitRe.MatchString(s)
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
// seriesmatch.DifferentVolumes after volumeTitles, so a one-sided "Part N"
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
		return seriesmatch.DifferentVolumes(folder, w)
	}
	if fileTitle == "" || normalizeTitle(fileTitle) == normalizeTitle(wanted) {
		return false
	}
	return differentVolumes(fileTitle, wanted)
}
