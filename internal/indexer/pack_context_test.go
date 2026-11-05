package indexer

import "testing"

// Release names and library titles from #3247.
var (
	dominionOfBlades = PackContext{Title: "Dominion of Blades: A LitRPG Adventure (Volume 1)"}
	grishamChamber   = PackContext{
		Title:        "Chamber",
		AuthorTitles: []string{"The Firm", "The Appeal", "The Pelican Brief"},
	}
	macomberCarolinesChild = PackContext{
		Title:        "Caroline's Child",
		SeriesTitle:  "Heart of Texas",
		AuthorTitles: []string{"Lonesome Cowboy", "Texas Two-Step", "Dr. Texas"},
	}
)

func TestMultiBookPackMarkerFor_ThreadPacks(t *testing.T) {
	for _, tc := range []struct {
		release string
		want    PackContext
		marker  string
	}{
		// The range directly after the wanted book's core title. Grabbed and
		// imported for book 1; the folder then held books 1 and 2.
		{"Dominion of Blades 1-2", dominionOfBlades, "Dominion of Blades 1-2"},
		{"Dominion.of.Blades.1-2.M4B", dominionOfBlades, "Dominion.of.Blades.1-2"},
		// kennethp1015's two releases, both auto-grabbed and imported as the
		// single wanted book.
		{
			"John Grisham - Three Classic Thrillers (The Firm; The Appeal; The Chamber) (v5.0) (epub)",
			grishamChamber, "The Firm; The Appeal; The Chamber",
		},
		{
			"Debbie Macomber - [Heart of Texas 01-03] - Lonesome Cowboy; Texas Two-Step; Caroline's Child (v5.0) (epub)",
			macomberCarolinesChild, "Heart of Texas 01-03",
		},
		// The same series range without the title list, and without padding.
		{"Debbie Macomber - [Heart of Texas 01-03] (epub)", macomberCarolinesChild, "Heart of Texas 01-03"},
		{"Debbie Macomber - Heart of Texas 1-3 (epub)", macomberCarolinesChild, "Heart of Texas 1-3"},
		// The title list alone, with no series range to find first.
		{
			"Debbie Macomber - Lonesome Cowboy; Texas Two-Step; Caroline's Child (v5.0) (epub)",
			macomberCarolinesChild, "Lonesome Cowboy; Texas Two-Step; Caroline's Child",
		},
	} {
		if got := MultiBookPackMarkerFor(tc.release, tc.want); got != tc.marker {
			t.Errorf("MultiBookPackMarkerFor(%q) = %q, want %q", tc.release, got, tc.marker)
		}
	}
}

func TestMultiBookPackMarkerFor_SingleBooks(t *testing.T) {
	for _, tc := range []struct {
		release string
		want    PackContext
		why     string
	}{
		{"Dominion of Blades - Matt Dinniman (M4B)", dominionOfBlades, "the plain single book"},
		{"Dominion of Blades 1 - A LitRPG Adventure", dominionOfBlades, "a series position, then a spaced dash and the subtitle"},
		{"Dominion of Blades Book 1", dominionOfBlades, "a book number with no range"},
		{"Dominion of Blades 1", dominionOfBlades, "a bare series number"},
		{"Dominion of Blades 01-49 (Unabridged)", dominionOfBlades, "zero padded after the title is a track count"},
		{"Dominion of Blades 2019-2020", dominionOfBlades, "a year span after the title"},
		{"Dominion of Blades 2-1", dominionOfBlades, "a falling range"},
		{"Dominion of Blades 0-2", dominionOfBlades, "a range from zero"},
		{"Dominion of Blades 1 - 2020 Edition", dominionOfBlades, "an edition year"},
		{"Dominion of Blades Part 1-2", dominionOfBlades, "a part range is one split recording"},
		{"Blades of Dominion 1-2", dominionOfBlades, "another title that only shares words"},
		{"Lonesome Cowboy 1-3", PackContext{Title: "Caroline's Child"}, "a range after another book's title is not anchored on the wanted book"},
		{"Dune 01-49 (Unabridged)", PackContext{Title: "Dune", SeriesTitle: "Dune"}, "a series named like the book does not lift the padding rule"},
		{"Reunion in Death - Nora Roberts; J. D. Robb", PackContext{Title: "Reunion in Death", AuthorTitles: []string{"Naked in Death", "Glory in Death"}}, "the semicolon separates two author names"},
		{"The Chamber; A Novel - John Grisham", grishamChamber, "one library title in a list is one book"},
		{"The Firm; The Appeal; The Chamber", PackContext{Title: "Chamber"}, "with no other library titles there is nothing to check the items against"},
		{"Heart of Texas Book 3 - Caroline's Child", macomberCarolinesChild, "a series position, not a range"},
	} {
		if got := MultiBookPackMarkerFor(tc.release, tc.want); got != "" {
			t.Errorf("%q matched %q but is a single book (%s)", tc.release, got, tc.why)
		}
	}
}

// TestMultiBookPackMarkerFor_KeepsContextFreeMarkers checks the context-free
// markers still apply with a context.
func TestMultiBookPackMarkerFor_KeepsContextFreeMarkers(t *testing.T) {
	const release = "Red Rising Series - Books 1 - 4 by Pierce Brown [ENG / M4B MP3] [VIP]"
	if got := MultiBookPackMarkerFor(release, PackContext{Title: "Red Rising"}); got != "Books 1 - 4" {
		t.Errorf("marker = %q, want %q", got, "Books 1 - 4")
	}
}
