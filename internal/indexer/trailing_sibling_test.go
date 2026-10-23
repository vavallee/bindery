package indexer

import "testing"

// siblingCtx is the library of #3335: the wanted book has no stored series
// position, but the catalogue holds the numbered volume under the same author.
var siblingCtx = bookContext{otherTitles: []string{"Rotten School #12 - Battle of the Dum Diddys", "Gross Out"}}

// TestSiblingVolumeRejected pins #3335: a release naming a numbered volume
// the library already holds as its own book is not the bare wanted title.
func TestSiblingVolumeRejected(t *testing.T) {
	cases := []struct{ name, release, reason string }{
		{"issue release", "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub)", reasonLaterNumber},
		{"issue release doubled format", "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub) (epub)", reasonLaterNumber},
		{"number only", "R L Stine - Rotten School 12 (retail) (epub)", reasonLaterNumber},
		{"hash number only", "R L Stine - Rotten School #12 (epub)", reasonLaterNumber},
		{"volume title without number", "R L Stine - Rotten School - Battle of the Dum Diddys (retail) (epub)", reasonAnotherTitle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", c.release, siblingCtx)
			if kept || reason != c.reason {
				t.Errorf("kept=%v reason=%q, want rejected with %q", kept, reason, c.reason)
			}
		})
	}
}

// TestSiblingVolumeKeepsLegitimate pins the other side: nothing here may be
// rejected on account of a sibling in the library.
func TestSiblingVolumeKeepsLegitimate(t *testing.T) {
	cases := []struct {
		name, title, author, release string
		bc                           bookContext
	}{
		{"plain wanted book", "Rotten School", "R. L. Stine", "R L Stine - Rotten School (retail) (epub)", siblingCtx},
		{"trailing year", "Rotten School", "R. L. Stine", "R L Stine - Rotten School 2010 (epub)", siblingCtx},
		{"first volume number", "Rotten School", "R. L. Stine", "R L Stine - [Rotten School 01] (retail) (epub)", siblingCtx},
		{"other volume number the library lacks", "Rotten School", "R. L. Stine", "R L Stine - Rotten School 3 (retail) (epub)", siblingCtx},
		{"count not number", "Rotten School", "R. L. Stine", "R L Stine - Rotten School 12 files (epub)", siblingCtx},
		{"wanted title is the numbered one", "Rotten School 12", "R. L. Stine", "R L Stine - Rotten School 12 (retail) (epub)", siblingCtx},
		{"wanted title is the numbered one, tagged", "Rotten School 12", "R. L. Stine", "R L Stine - [Rotten School 12] (retail) (epub)", siblingCtx},
		{"Catch-22", "Catch-22", "Joseph Heller", "Joseph Heller - Catch-22 (retail) (epub)", bookContext{otherTitles: []string{"Catch-22 #2 - Closing Time"}}},
		{"1984", "1984", "George Orwell", "George Orwell - 1984 (retail) (epub)", bookContext{otherTitles: []string{"1984 #2 - Animal Farm"}}},
		{"Fahrenheit 451", "Fahrenheit 451", "Ray Bradbury", "Ray Bradbury - Fahrenheit 451 (retail) (epub)", bookContext{otherTitles: []string{"Fahrenheit 451 #2 - The Hearth and the Salamander"}}},
		{"Apollo 13", "Apollo 13", "Jim Lovell", "Jim Lovell - Apollo 13 (retail) (epub)", bookContext{otherTitles: []string{"Apollo 13 #2 - Lost Moon"}}},
		{"Oceans 11", "Ocean's 11", "Some Author", "Some Author - Ocean's 11 (retail) (epub)", bookContext{otherTitles: []string{"Ocean's 11 #2 - Twelve"}}},
		{"sibling is volume one", "Rotten School", "R. L. Stine", "R L Stine - Rotten School 01 - Getting Started Early (epub)", bookContext{otherTitles: []string{"Rotten School #1 - Getting Started Early"}}},
		{"sibling only contains the title mid name", "Rotten School", "R. L. Stine", "R L Stine - Rotten School 5 - Dum Diddys Again (epub)", bookContext{otherTitles: []string{"Return to Rotten School 5 - Dum Diddys Again"}}},
		{"group and tags", "Rotten School", "R. L. Stine", "R.L.Stine.Rotten.School.RETAIL.EPUB-GRP [epub]", siblingCtx},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, c.bc)
			if !kept {
				t.Errorf("release %q rejected: %s", c.release, reason)
			}
		})
	}
}

// TestSiblingVolumeKeepsPositionBehaviour pins that a stored position still
// decides the number, sibling or not.
func TestSiblingVolumeKeepsPositionBehaviour(t *testing.T) {
	bc := siblingCtx
	bc.seriesPosition = "12"
	if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub)", bc); !kept {
		t.Errorf("position 12 release for volume 12 rejected: %s", reason)
	}
	// A duplicate catalogue row (a second edition) for the very volume that is
	// wanted must not turn the wanted volume's own release into another title.
	if kept, reason := relevanceVerdict("Rotten School", "R. L. Stine", "Rotten School 12 - Battle of the Dum Diddys (epub)", bc); !kept {
		t.Errorf("duplicate row with stored position rejected the good release: %s", reason)
	}
	bc.seriesPosition = "3"
	if kept, _ := relevanceVerdict("Rotten School", "R. L. Stine", "R L Stine - Rotten School 4 (epub)", bc); kept {
		t.Error("position 3 should still reject number 4")
	}
}

// TestSiblingTailShape pins which library titles count as a numbered sibling.
func TestSiblingTailShape(t *testing.T) {
	wanted := []string{"rotten", "school"}
	cases := []struct {
		title string
		num   int
		tail  string
		ok    bool
	}{
		{"Rotten School #12 - Battle of the Dum Diddys", 12, "battle of the dum diddys", true},
		{"Rotten School Vol 2 - Gross Out Games", 2, "gross out games", true},
		{"Rotten School Book 3 - Gross Out Games", 3, "gross out games", true},
		{"Rotten School 2010 - Gross Out Games", 0, "", false},
		{"Rotten School - Gross Out Games", 0, "", false},
		{"Rotten School 1 - Gross Out Games", 0, "", false},
		{"Return to Rotten School 5 - Gross Out Games", 0, "", false},
	}
	for _, c := range cases {
		n, tail, ok := siblingTail(c.title, wanted)
		if n != c.num || tail != c.tail || ok != c.ok {
			t.Errorf("siblingTail(%q) = %d, %q, %v; want %d, %q, %v", c.title, n, tail, ok, c.num, c.tail, c.ok)
		}
	}
	if _, _, ok := siblingTail("Rotten School 12 - Battle of the Dum Diddys", nil); ok {
		t.Error("empty wanted identity must never match")
	}
}

// TestSiblingLimitation documents the trade off: a numbered library row whose
// tail is the only thing a plain release shares makes that release another
// title.
func TestSiblingLimitation(t *testing.T) {
	bc := bookContext{otherTitles: []string{"Dune 2 - Illustrated Edition"}}
	kept, reason := relevanceVerdict("Dune", "Frank Herbert", "Frank Herbert - Dune - Illustrated Edition (epub)", bc)
	if kept || reason != reasonAnotherTitle {
		t.Errorf("kept=%v reason=%q, want the known rejection", kept, reason)
	}
}
