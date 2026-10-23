package indexer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// prefixedLibrary is the catalogue of #3382 for the wanted bare title: the
// imported sibling carries the series prefix and no number, and the standalone
// duplicate is excluded, so the library listing (excluded = 0) never returns it.
func prefixedLibrary() []models.Book {
	return []models.Book{
		{ID: 3173, AuthorID: 27, Title: "Rotten School"},
		{ID: 4465, AuthorID: 27, Title: "Rotten School #12 - Battle of the Dum Diddys"},
		{ID: 4481, AuthorID: 27, Title: "Rotten School - Punk'd and skunked"},
		{ID: 9312, AuthorID: 27, Title: "Punk'd and Skunked", Excluded: true},
	}
}

// criteriaFromLibrary builds the criteria the way the interactive search and
// the scheduler do: SetLibraryContext with a reader that, like
// BookRepo.ListByAuthorAndUser, leaves excluded books out.
func criteriaFromLibrary(wanted models.Book, library []models.Book) MatchCriteria {
	var c MatchCriteria
	c.SetLibraryContext(context.Background(), wanted, nil, func(context.Context, int64) ([]models.Book, error) {
		var out []models.Book
		for _, b := range library {
			if !b.Excluded {
				out = append(out, b)
			}
		}
		return out, nil
	})
	return c
}

// TestPrefixedSiblingTitleRejected pins #3382 through the production call
// path: the unnumbered, series prefixed sibling makes its suffix a known other
// book, in either apostrophe spelling.
func TestPrefixedSiblingTitleRejected(t *testing.T) {
	crit := criteriaFromLibrary(models.Book{ID: 3173, AuthorID: 27, Title: "Rotten School"}, prefixedLibrary())
	for _, release := range []string{
		"R L Stine - [Rotten School 11] - Punk'd and Skunked (retail) (epub)",
		"R L Stine - [Rotten School 11] - Punkd and Skunked (retail) (epub)",
		"R.L.Stine.Rotten.School.11.Punkd.and.Skunked.RETAIL.EPUB-GRP",
		"R L Stine - Rotten School #11 - Punk'd and Skunked (epub)",
	} {
		kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", release, crit.context())
		if kept || reason != reasonAnotherTitle {
			t.Errorf("%q: kept=%v reason=%q, want rejected with %q", release, kept, reason, reasonAnotherTitle)
		}
	}
}

// TestPrefixedSiblingTitleKeepsLegitimate pins what must stay eligible.
func TestPrefixedSiblingTitleKeepsLegitimate(t *testing.T) {
	lib := prefixedLibrary()
	crit := criteriaFromLibrary(models.Book{ID: 3173, AuthorID: 27, Title: "Rotten School"}, lib)
	for _, release := range []string{
		"R L Stine - Rotten School (retail) (epub)",
		"R L Stine - Rotten School (Second Edition) (epub)",
		"R L Stine - Rotten School - The Dum Diddys Return (epub)",
	} {
		if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", release, crit.context()); !kept {
			t.Errorf("%q rejected: %s", release, reason)
		}
	}

	// A search for the sibling's own suffix: the standalone title is wanted.
	wantedSuffix := criteriaFromLibrary(models.Book{ID: 9312, AuthorID: 27, Title: "Punk'd and Skunked"}, lib)
	for _, release := range []string{
		"R L Stine - Punk'd and Skunked (retail) (epub)",
		"R L Stine - Punkd and Skunked (epub)",
		"R L Stine - [Rotten School 11] - Punk'd and Skunked (retail) (epub)",
	} {
		if kept, reason := relevanceVerdict("Punk'd and Skunked", "R. L. Stine", release, wantedSuffix.context()); !kept {
			t.Errorf("search for the suffix title: %q rejected: %s", release, reason)
		}
	}

	// The wanted book is the prefixed one: its own release is not another book.
	wantedPrefixed := criteriaFromLibrary(models.Book{ID: 4481, AuthorID: 27, Title: "Rotten School - Punk'd and skunked"}, lib)
	if kept, reason := relevanceVerdict("Rotten School - Punk'd and skunked", "R. L. Stine",
		"R L Stine - Rotten School - Punk'd and Skunked (epub)", wantedPrefixed.context()); !kept {
		t.Errorf("wanted prefixed title rejected: %s", reason)
	}
}

func guardAuthor(title string) string {
	if title == "Dune" {
		return "Frank Herbert"
	}
	return "R. L. Stine"
}

// TestPrefixedSiblingTitleGuards covers the false rejections to avoid.
func TestPrefixedSiblingTitleGuards(t *testing.T) {
	cases := []struct {
		name, title, release string
		others               []string
		bc                   bookContext
		kept                 bool
	}{
		{"wanted subtitle equals the sibling suffix", "Rotten School: Punk'd and Skunked", "R L Stine - Rotten School - Punk'd and Skunked (epub)", []string{"Rotten School - Punk'd and skunked"}, bookContext{}, true},
		{"wanted title inside the suffix", "Rotten School", "R L Stine - Rotten School - Welcome to Rotten School Again (epub)", []string{"Rotten School - Welcome to Rotten School Again"}, bookContext{}, true},
		{"omnibus sibling", "Rotten School", "R L Stine - Rotten School - Gross Out Games (epub)", []string{"Rotten School - Boxed Set Gross Out Games"}, bookContext{}, true},
		{"edition suffix", "Dune", "Frank Herbert - Dune - Deluxe Illustrated Edition (epub)", []string{"Dune - Deluxe Illustrated Edition"}, bookContext{}, true},
		{"no separator", "Rotten School", "R L Stine - Rotten School Punkd and Skunked (epub)", []string{"Rotten School Punkd and Skunked"}, bookContext{}, true},
		{"number after the separator is not a title", "Rotten School", "R L Stine - [Rotten School 3] - 2010 Gross Out Games (epub)", []string{"Rotten School - 2010 Gross Out Games"}, bookContext{}, true},
		{"marker word after the separator is not a title", "Rotten School", "R L Stine - [Rotten School 3] - Book Club Adventures (epub)", []string{"Rotten School - Book Club Adventures"}, bookContext{}, true},
		{"stored position switches it off", "Rotten School", "R L Stine - [Rotten School 11] - Punkd and Skunked (epub)", []string{"Rotten School - Punk'd and skunked"}, bookContext{seriesPosition: "11"}, true},
		{"rejects when none of the guards apply", "Rotten School", "R L Stine - [Rotten School 11] - Punkd and Skunked (epub)", []string{"Rotten School - Punk'd and skunked"}, bookContext{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bc := c.bc
			bc.otherTitles = c.others
			kept, reason := relevanceVerdict(c.title, guardAuthor(c.title), c.release, bc)
			if kept != c.kept {
				t.Errorf("kept=%v reason=%q, want kept=%v", kept, reason, c.kept)
			}
		})
	}
}
