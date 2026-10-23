package indexer

import "testing"

// Round 5: a lone I, V or X names the wanted number when a marker word or the
// end of the release name says it is a number, and is never read when it is an
// author initial.
func TestLoneRomanNumeralsNameTheWantedNumber(t *testing.T) {
	bc := func(pos string) bookContext { return bookContext{seriesPosition: pos, otherTitles: []string{"Alpha"}} }
	kept := []struct{ release, pos string }{
		{"Some Author - Alpha I (epub)", "1"},
		{"Some Author - Alpha I", "1"},
		{"Some Author - Alpha V", "5"},
		{"Some Author - Alpha V (epub)", "5"},
		{"Some Author - Alpha - V", "5"},
		{"Some Author - Alpha, Book V", "5"},
		{"Some Author - Alpha Part V", "5"},
		{"Some Author - Alpha Vol. V MP3", "5"},
		{"Some Author - Alpha #V (epub)", "5"},
		{"Some Author - Alpha X", "10"},
		{"Some Author - Alpha X (retail) (epub)", "10"},
		{"Some Author - Alpha Book X", "10"},
	}
	for _, c := range kept {
		if k, reason := relevanceVerdict("Alpha: Beta Gamma", "Some Author", c.release, bc(c.pos)); !k {
			t.Errorf("%q dropped at position %s (%s)", c.release, c.pos, reason)
		}
	}
	// A different numeral, or an initial, is still no evidence of the wanted number.
	for _, c := range []struct{ release, pos string }{
		{"Some Author - Alpha V", "4"},
		{"Some Author - Alpha Book V", "6"},
		{"Some Author - Alpha - V. E. Schwab (epub)", "5"},
		{"Some Author - Alpha X Marks the Spot", "10"},
	} {
		if k, _ := relevanceVerdict("Alpha: Beta Gamma", "Some Author", c.release, bc(c.pos)); k {
			t.Errorf("%q kept at position %s", c.release, c.pos)
		}
	}
}

func TestLoneLettersAreNotNumbersWhenTheyAreInitials(t *testing.T) {
	ident := titleIdentityKeywords("Alpha")
	for _, raw := range []string{
		"Alpha V. E. Schwab", "Alpha V.E. Schwab", "Alpha V E Schwab", "Alpha V. C. Andrews MP3",
		"Alpha X. Y. Smith", "Alpha I. M. Pei", "Alpha - V. E. Schwab (epub)", "Alpha I Know What You Did",
	} {
		for _, guarded := range []bool{true, false} {
			if got := numbersAfter(raw, NormalizeRelease(raw), guarded, ident); len(got) != 0 {
				t.Errorf("%q (guarded %v) read as numbers %v", raw, guarded, got)
			}
		}
	}
	// The strict reading never reads a lone letter, even at the end of the name.
	for _, raw := range []string{"Alpha V", "Alpha Book X", "Alpha I (epub)"} {
		if got := numbersAfter(raw, NormalizeRelease(raw), true, ident); len(got) != 0 {
			t.Errorf("strict reading read %q as %v", raw, got)
		}
	}
	// The lenient reading reads them behind a marker or at the end of the name.
	want := map[string]int{"Alpha V": 5, "Alpha Book X": 10, "Alpha I (epub)": 1, "Alpha Part V": 5, "Alpha #V": 5}
	for raw, v := range want {
		if got := numbersAfter(raw, NormalizeRelease(raw), false, ident); len(got) != 1 || got[0] != v {
			t.Errorf("lenient reading of %q = %v, want [%d]", raw, got, v)
		}
	}
}

// Every series word skips a library title that carries it.
func TestEverySeriesWordSkipsALibraryTitle(t *testing.T) {
	for _, w := range []string{"Series", "Trilogy", "Saga", "Cycle", "Chronicles", "Quartet", "Quintet", "Duology", "Tetralogy"} {
		rel := "Stephen King - The Gunslinger - The Dark Tower " + w + " (epub)"
		if k, reason := relevanceVerdict("The Gunslinger", "Stephen King", rel, bookContext{otherTitles: []string{"The Dark Tower"}}); !k {
			t.Errorf("%q dropped (%s)", rel, reason)
		}
	}
	// A word that is not a series word does not skip it.
	if k, _ := relevanceVerdict("The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower Novel (epub)",
		bookContext{otherTitles: []string{"The Dark Tower"}}); k {
		t.Errorf("a non series word must not skip the library title")
	}
}

// The "of" clause and the digit after a number clause of the count guard.
func TestSubtitleNumberGuardClauses(t *testing.T) {
	bc := bookContext{}
	// "of" behind a marker: the number is a part counter, not a different book.
	// Without the clause "Book 2 of 3" would read as 2 and reject "Foo: Book 3".
	if k, reason := relevanceVerdict("Foo: Book 3", "Some Author", "Some Author - Foo Book 2 of 3 MP3", bc); !k {
		t.Errorf("Book 2 of 3 dropped (%s)", reason)
	}
	if k, reason := relevanceVerdict("Foo: Book 3", "Some Author", "Some Author - Foo Book 3 of 3 MP3", bc); !k {
		t.Errorf("Book 3 of 3 dropped (%s)", reason)
	}
	// A digit directly after a number: not read, so no different number.
	if k, reason := relevanceVerdict("Foo: Book 3", "Some Author", "Some Author - Foo 2 5 (retail) (epub)", bc); !k {
		t.Errorf("a number followed by a digit was read (%s)", reason)
	}
	// A token that only starts with a digit is not a number.
	if k, reason := relevanceVerdict("Foo: Book 3", "Some Author", "Some Author - Foo 2x (retail) (epub)", bc); !k {
		t.Errorf("2x was read as a number (%s)", reason)
	}
	// Pinned as it behaves: two digits are one number, and 33 ends the name.
	if k, _ := relevanceVerdict("Foo: Book 3", "Some Author", "Some Author - Foo 33", bc); k {
		t.Errorf("33 at the end of the name is read as a different number")
	}
}
