package indexer

import (
	"strings"
	"testing"
)

// Tests for the fourth review round on #3268.

// An author initial after the title is not a roman numeral.
func TestAuthorInitialsAreNotRomanNumerals(t *testing.T) {
	cases := []struct {
		title, author, release, pos string
	}{
		{"A Gathering of Shadows", "V. E. Schwab", "A Gathering of Shadows - V. E. Schwab (epub)", "2"},
		{"A Gathering of Shadows", "V. E. Schwab", "A Gathering of Shadows - V.E. Schwab (epub)", "2"},
		{"A Gathering of Shadows", "V. E. Schwab", "A Gathering of Shadows - V E Schwab (epub)", "2"},
		{"A Gathering of Shadows", "V. E. Schwab", "V. E. Schwab - A Gathering of Shadows (epub)", "2"},
		{"Vicious", "V. E. Schwab", "Vicious - V. E. Schwab (epub)", "1"},
		{"Vengeful", "V. E. Schwab", "Vengeful - V. E. Schwab (Villains #2)", "2"},
		{"If There Be Thorns", "V. C. Andrews", "If There Be Thorns - V. C. Andrews", "3"},
		{"Petals on the Wind", "V. C. Andrews", "Petals on the Wind - V. C. Andrews MP3", "2"},
		{"Petals on the Wind", "V. C. Andrews", "Petals on the Wind - V. C. Andrews MP3", "1"},
		{"Flowers in the Attic", "X. Y. Smith", "Flowers in the Attic - X. Y. Smith (epub)", "1"},
	}
	for _, c := range cases {
		if k, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{seriesPosition: c.pos}); !k {
			t.Errorf("%q at position %s dropped (%s)", c.release, c.pos, reason)
		}
	}
	// Numerals that are real numbers are still read; every single letter that
	// could be one is checked, and only I, V and X are numerals that short.
	for _, letter := range []string{"I", "V", "X", "L", "C", "D", "M"} {
		rel := "Frank Herbert - Dune - " + letter + ". Nobody (epub)"
		if k, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bookContext{seriesPosition: "1"}); !k {
			t.Errorf("%q dropped (%s)", rel, reason)
		}
	}
	for _, rel := range []string{"Frank Herbert - Dune IV (epub)", "Frank Herbert - Dune VI (epub)", "Frank Herbert - Dune II - Messiah", "Frank Herbert - Dune XX (epub)"} {
		if k, _ := relevanceVerdict("Dune", "Frank Herbert", rel, bookContext{seriesPosition: "1"}); k {
			t.Errorf("%q kept at position 1", rel)
		}
	}
	// The reader used for equality and for "a different number" skips them too.
	ident := titleIdentityKeywords("Vicious")
	for _, raw := range []string{"Vicious - V. E. Schwab", "Vicious X Marks", "Vicious I Know"} {
		if got := numbersAfter(raw, NormalizeRelease(raw), true, ident); len(got) != 0 {
			t.Errorf("%q read as numbers %v", raw, got)
		}
	}
	// And the main title trigger does not see an initial as a different number.
	if k, reason := relevanceVerdict("Vicious: Book 2", "V. E. Schwab", "Vicious - V. E. Schwab (epub)", bookContext{}); !k {
		t.Errorf("an initial read as a different number (%s)", reason)
	}
}

// A series name in brackets, or with a series word or roman numeral after it,
// is a label, not another book.
func TestSeriesNameWithoutNumberIsALabel(t *testing.T) {
	dark := bookContext{otherTitles: []string{"The Dark Tower"}}
	keep := []struct {
		title, author, release string
		bc                     bookContext
	}{
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger (The Dark Tower) (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger [The Dark Tower] (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger (The Dark Tower Series) (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower Series (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower Saga (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower Cycle (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower Chronicles (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower I (epub)", dark},
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower III (epub)", dark},
		{"Bring Up the Bodies", "Hilary Mantel", "Hilary Mantel - Bring Up the Bodies (Wolf Hall Trilogy Book 2) (epub)", bookContext{otherTitles: []string{"Wolf Hall: A Novel"}}},
		{"Bring Up the Bodies", "Hilary Mantel", "Hilary Mantel - Bring Up the Bodies (Wolf Hall Trilogy) (epub)", bookContext{otherTitles: []string{"Wolf Hall: A Novel"}}},
	}
	for _, c := range keep {
		if k, reason := relevanceVerdict(c.title, c.author, c.release, c.bc); !k {
			t.Errorf("%q dropped for %q (%s)", c.release, c.title, reason)
		}
	}
	reject := []struct {
		title, author, release string
		bc                     bookContext
	}{
		{"Other Thing", "Some Author", "Some Author - Other Thing - Other Book 3 - Other Book (epub)", bookContext{otherTitles: []string{"Other Book"}}},
		{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars - Rule of Wolves (epub)", bookContext{otherTitles: []string{"Rule of Wolves"}}},
		// A year after a library title is not a label: the label digits stop at 3.
		{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars - Rule of Wolves 2021 (epub)", bookContext{otherTitles: []string{"Rule of Wolves"}}},
		// A lowercase i is a word, not a numeral.
		{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars - Rule of Wolves i (epub)", bookContext{otherTitles: []string{"Rule of Wolves"}}},
	}
	for _, c := range reject {
		if k, _ := relevanceVerdict(c.title, c.author, c.release, c.bc); k {
			t.Errorf("%q kept for %q", c.release, c.title)
		}
	}
}

// A library title contained in the series title is skipped, which is the
// direction no other test pins.
func TestLibraryTitleContainedInSeriesTitleIsSkipped(t *testing.T) {
	tr := newTrailingRules("The Gunslinger", bookContext{
		seriesTitle: "The Wheel of Time Saga",
		otherTitles: []string{"Wheel of Time", "Rule of Wolves"},
	}, titleIdentityKeywords("The Gunslinger"))
	var kept []string
	for _, ot := range tr.others {
		kept = append(kept, strings.Join(ot.ident, " "))
	}
	if len(kept) != 1 || kept[0] != "rule wolves" {
		t.Errorf("others = %v, want only rule wolves", kept)
	}
	if tr.otherFull["wheel time"] {
		t.Errorf("a library title contained in the series title must leave otherFull too")
	}
}

// Signal (b) counts only numbers that stand as numbers.
func TestSubtitleNumberedTriggerSkipsCounts(t *testing.T) {
	keep := []string{
		"Foo - 64 kbps MP3", "Foo 24 bit", "Foo 12 files", "Foo 2 CD", "Foo 4 mp3", "Foo 12 hours", "Foo 3 Tapes",
		"Foo 2 of 2", "Foo (Unabridged) 128 kbps MP3", "Foo Book 2 MP3", "Foo 02 (retail) (epub)",
	}
	for _, rel := range keep {
		if k, reason := relevanceVerdict("Foo: Book 2", "Some Author", "Some Author - "+rel, bookContext{}); !k {
			t.Errorf("%q dropped for Foo: Book 2 (%s)", rel, reason)
		}
	}
	for _, rel := range []string{"Foo 3 (retail) (epub)", "Foo Book 3 mp3", "Foo - 3", "Foo #3 epub", "Foo III (epub)"} {
		if k, _ := relevanceVerdict("Foo: Book 2", "Some Author", "Some Author - "+rel, bookContext{}); k {
			t.Errorf("%q kept for Foo: Book 2", rel)
		}
	}
}

// Policy: a subtitle that numbers the book 2 or more, and a release that names
// no number, is not evidence of a different book. "Foundation and Empire: Book
// Two" against the plain release is the same shape and must be approved, so
// the lone "Dune: Part Two" case is kept too.
func TestNumberedSubtitleWithNoNumberInReleaseIsKept(t *testing.T) {
	for _, rel := range []string{"Frank Herbert - Dune (Unabridged) MP3", "Frank Herbert - Dune (epub)"} {
		if k, reason := relevanceVerdict("Dune: Part Two", "Frank Herbert", rel, bookContext{}); !k {
			t.Errorf("%q dropped (%s)", rel, reason)
		}
	}
}

// With the series linked, the series title skip spares a library entry that
// carries the series name, so the plain release is approved for a later book.
// This is a known limit, pinned so it is not mistaken for a regression.
func TestSeriesLinkedMainTitleKnownLimit(t *testing.T) {
	bc := bookContext{seriesPosition: "2", seriesTitle: "Dune Chronicles", otherTitles: []string{"Dune"}}
	if k, reason := relevanceVerdict("Dune: Messiah", "Frank Herbert", "Frank Herbert - Dune (epub)", bc); !k {
		t.Errorf("limit changed: the series title skip spares the library entry Dune (%s)", reason)
	}
	// Without the series link the library entry is another book.
	if k, _ := relevanceVerdict("Dune: Messiah", "Frank Herbert", "Frank Herbert - Dune (epub)",
		bookContext{seriesPosition: "2", otherTitles: []string{"Dune"}}); k {
		t.Errorf("without the series link the plain release is the library book Dune")
	}
}
