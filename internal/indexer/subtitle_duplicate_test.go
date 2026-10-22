package indexer

import "testing"

// TestSubtitleDuplicateSiblingKept pins the review of #3384: a library row that
// is the same book written "Title: Subtitle" (a second provider's duplicate)
// must not make the release carrying that full title another book.
func TestSubtitleDuplicateSiblingKept(t *testing.T) {
	cases := []struct {
		name, title, author, sibling, release string
	}{
		{"Atomic Habits dash", "Atomic Habits", "James Clear",
			"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones",
			"James Clear - Atomic Habits - An Easy and Proven Way to Build Good Habits and Break Bad Ones (epub)"},
		{"Atomic Habits ampersand", "Atomic Habits", "James Clear",
			"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones",
			"James Clear - Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones (epub)"},
		{"Sapiens dash", "Sapiens", "Yuval Noah Harari", "Sapiens: A Brief History of Humankind",
			"Yuval Noah Harari - Sapiens - A Brief History of Humankind (epub)"},
		{"Sapiens colon", "Sapiens", "Yuval Noah Harari", "Sapiens: A Brief History of Humankind",
			"Yuval Noah Harari - Sapiens: A Brief History of Humankind (retail) (epub)"},
		{"Sapiens colon extension", "Sapiens", "Yuval Noah Harari", "Sapiens: A Brief History of Humankind",
			"Yuval Noah Harari - Sapiens: A Brief History of Humankind.epub"},
		{"Sapiens dotted", "Sapiens", "Yuval Noah Harari", "Sapiens: A Brief History of Humankind",
			"Yuval.Noah.Harari.Sapiens.A.Brief.History.of.Humankind.RETAIL.EPUB-GRP"},
		{"Frankenstein without or", "Frankenstein", "Mary Shelley", "Frankenstein; or, The Modern Prometheus",
			"Mary Shelley - Frankenstein - The Modern Prometheus (epub)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{otherTitles: []string{c.sibling}})
			if !kept {
				t.Errorf("rejected the full title of the same book: %s", reason)
			}
		})
	}
}

// TestSubtitleDuplicateStillRejectsOtherVolume pins that the allowance is for
// the release that carries the sibling's whole title only: anything between
// the wanted title and the suffix (a volume number, other words) still names
// another book.
func TestSubtitleDuplicateStillRejectsOtherVolume(t *testing.T) {
	bc := bookContext{otherTitles: []string{"Sapiens: A Brief History of Humankind"}}
	for _, release := range []string{
		"Yuval Noah Harari - Sapiens 2 - A Brief History of Humankind (epub)",
		"Yuval Noah Harari - [Sapiens 2] - A Brief History of Humankind (epub)",
		"Yuval Noah Harari - Sapiens Companion - A Brief History of Humankind (epub)",
	} {
		if kept, _ := relevanceVerdict("Sapiens", "Yuval Noah Harari", release, bc); kept {
			t.Errorf("%q kept", release)
		}
	}
}

// TestSubtitleAlsoAStandaloneBook pins that when the library ALSO holds the
// suffix as a book of its own (not excluded), a release carrying it is still
// another book for the bare wanted title, whatever order the rows come in.
func TestSubtitleAlsoAStandaloneBook(t *testing.T) {
	release := "Yuval Noah Harari - Sapiens - A Brief History of Humankind (epub)"
	for _, others := range [][]string{
		{"Sapiens: A Brief History of Humankind", "A Brief History of Humankind"},
		{"A Brief History of Humankind", "Sapiens: A Brief History of Humankind"},
	} {
		if kept, _ := relevanceVerdict("Sapiens", "Yuval Noah Harari", release, bookContext{otherTitles: others}); kept {
			t.Errorf("%v: kept", others)
		}
	}
}

// TestEditionSuffixIsNotAnotherBook pins that a sibling whose suffix describes
// an edition (a language, an ordinal, an anniversary, a format) never makes
// the release carrying that edition another book. The release has a year
// between the title and the suffix so that only the edition reading keeps it.
func TestEditionSuffixIsNotAnotherBook(t *testing.T) {
	suffixes := []string{
		"German Edition", "Edicion en Espanol", "Edición en Español", "Deutsche Ausgabe", "Norsk utgave",
		"Edizione italiana", "Tenth Anniversary Edition", "10th Anniversary Edition", "25th Anniversary",
		"Twentieth Anniversary", "Movie Tie-in", "Large Print", "A Novel", "Illustrated Edition",
		"Annotated Edition", "Unabridged", "Revised and Expanded Edition", "French Edition",
		"Spanish Language Edition", "Deluxe Edition", "Special Collectors Edition", "Deutsche Version", "Norsk Oversettelse", "Espanol Latinoamericano",
		"Second Printing", "2nd Printing", "Silver Anniversary", "Anniversary Special",
	}
	for _, s := range suffixes {
		t.Run(s, func(t *testing.T) {
			bc := bookContext{otherTitles: []string{"Sapiens - " + s}}
			release := "Yuval Noah Harari - Sapiens (2014) " + s + " (epub)"
			if kept, reason := relevanceVerdict("Sapiens", "Yuval Noah Harari", release, bc); !kept {
				t.Errorf("rejected: %s", reason)
			}
		})
	}
}

// TestSubtitleConnectingWordsAreExplicit pins that only a short list of real
// connecting words may stand between the wanted title and the suffix. Roman
// numerals, short words and numbers (also before the title) name another book.
func TestSubtitleConnectingWordsAreExplicit(t *testing.T) {
	bc := bookContext{otherTitles: []string{"Rotten School - Punk'd and skunked"}}
	for _, release := range []string{
		"R L Stine - Rotten School II - Punk'd and Skunked (epub)",
		"R L Stine - Rotten School XI Punk'd and Skunked (epub)",
		"R L Stine - Rotten School I Punk'd and Skunked (epub)",
		"R L Stine - Rotten School Mr Punk'd and Skunked (epub)",
		"R L Stine - 11 - Rotten School - Punk'd and Skunked (epub)",
	} {
		if kept, _ := relevanceVerdict("Rotten School", "R. L. Stine", release, bc); kept {
			t.Errorf("%q kept", release)
		}
	}
	// The sibling's own full title, also with a connecting word, stays kept.
	for _, release := range []string{
		"R L Stine - Rotten School - Punk'd and Skunked (epub)",
		"R L Stine - Rotten School and Punk'd and Skunked (epub)",
	} {
		if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", release, bc); !kept {
			t.Errorf("%q rejected: %s", release, reason)
		}
	}
}

// TestTwoPrefixedRowsShareOneSuffix pins that two prefixed rows with the same
// suffix (two providers, two punctuation styles) are both subtitles: the full
// title stays kept and the numbered release is still rejected.
func TestTwoPrefixedRowsShareOneSuffix(t *testing.T) {
	bc := bookContext{otherTitles: []string{"Rotten School - Punk'd and skunked", "Rotten School: Punk'd and Skunked"}}
	if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", "R L Stine - Rotten School - Punk'd and Skunked (epub)", bc); !kept {
		t.Errorf("full title rejected: %s", reason)
	}
	if kept, _ := relevanceVerdict("Rotten School", "R. L. Stine", "R L Stine - [Rotten School 11] - Punk'd and Skunked (epub)", bc); kept {
		t.Error("numbered release kept")
	}
}

// TestSetSiblingIsNotAnotherBook kills the set title skip: a release that
// really carries the box set words is not made another book by a box set
// sibling.
func TestSetSiblingIsNotAnotherBook(t *testing.T) {
	bc := bookContext{otherTitles: []string{"Rotten School - Boxed Set Gross Out Games"}}
	release := "R L Stine - [Rotten School 3] - Boxed Set Gross Out Games (epub)"
	if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", release, bc); !kept {
		t.Errorf("rejected: %s", reason)
	}
}
