package indexer

import "testing"

// TestMultiBookPackMarker_ReportedRelease pins the verbatim release name from
// #2276. An automatic search for the single book "Red Rising" selected it and
// the importer then tried to place all 117 files of a four-book pack into one
// book's folder.
func TestMultiBookPackMarker_ReportedRelease(t *testing.T) {
	const release = "Red Rising Series - Books 1 - 4 by Pierce Brown [ENG / M4B MP3] [VIP]"
	got := MultiBookPackMarker(release)
	if got == "" {
		t.Fatalf("release %q was not recognised as a multi-book pack", release)
	}
	if got != "Books 1 - 4" {
		t.Errorf("marker = %q, want %q", got, "Books 1 - 4")
	}
}

func TestMultiBookPackMarker_Packs(t *testing.T) {
	for _, title := range []string{
		"Pierce Brown - Red Rising Books 1-4 [M4B]",
		"Red Rising Book 1 to 4 - Pierce Brown",
		"Red Rising Books 1 through 5",
		"Brandon Sanderson - Mistborn Vols. 1 & 2",
		"Mistborn Volumes 1-3 (Unabridged)",
		"The Expanse Box Set",
		"The Expanse Boxed Set [EPUB]",
		"The Expanse Boxset",
		"Discworld 5 Book Set",
		"Discworld 3 Volume Collection",
		"Pierce Brown - Red Rising Omnibus",
		"The Sandman Complete Series",
		"Sherlock Holmes Complete Collection [MOBI]",
	} {
		if MultiBookPackMarker(title) == "" {
			t.Errorf("%q was not recognised as a multi-book pack", title)
		}
	}
}

// TestMultiBookPackMarker_SingleBooks is the half that matters: a false
// positive here silently stops a book being grabbed at all, with no setting to
// turn it back on. Each case records why that shape was left out.
func TestMultiBookPackMarker_SingleBooks(t *testing.T) {
	for _, tc := range []struct {
		title string
		why   string
	}{
		{"Red Rising - Pierce Brown [M4B]", "the plain single-book release"},
		{"Pierce Brown - Red Rising Series - Book 1", "a book number with no range"},
		{
			"Morning Star Book III of the Red Rising Trilogy (Unabridged) Part 1-2",
			"\"part\" ranges are how one long audiobook is split, and \"trilogy\" is how single books name their series — this is a real file name from the #2275 download",
		},
		{"The Complete Sherlock Holmes", "\"complete\" alone is a single-volume edition's own name"},
		{"Ursula K. Le Guin - The Collected Stories", "a bare \"collected\"/\"collection\" is a single book"},
		{"The Best American Short Stories Anthology 2020", "an anthology is one book"},
		{"Pierce Brown - Red Rising (2014-2016 reissue)", "a bare numeric range is far more often a year span"},
		{"Red Rising Book 1 - 2020 Edition", "an edition year must not read as a range of 2020 books"},
		{"Dune Volume 1", "a single volume with no range"},
		{"The Boxer - Pierce Brown", "\"box\" must not match inside another word"},
	} {
		if got := MultiBookPackMarker(tc.title); got != "" {
			t.Errorf("%q matched %q but is a single book (%s)", tc.title, got, tc.why)
		}
	}
}

// TestMultiBookPackMarker_FoundationSearch pins the release names of one
// search for "Foundation" by Isaac Asimov (#3224). The first was auto-grabbed
// as a single book and held ten.
func TestMultiBookPackMarker_FoundationSearch(t *testing.T) {
	for _, title := range []string{
		"Foundation series by Isaac Asimov EPUB",
		"The Foundation Collection Isaac Asimov .EPUB (11 Books)",
		"Foundation Series - Isaac Asimov 1-7 (KINDLE)",
		"Isaac Asimov Books Collection(Foundation Pack)",
		"Foundation Saga eBooks",
		"Ebook Series Isaac Asimov (03 Books)",
		"Foundation series by Isaac Asimov (EPUB) 2019",
		"Foundation series by Isaac Asimov - Complete EPUB",
		"Discworld Series by Terry Pratchett [MOBI]",
		"Foundation series by Isaac Asimov: ebooks kindle",
		"Discworld Series 1-41",
		"Discworld Series 1-41 Terry Pratchett",
		"Harry Potter Series 1-7 by J.K. Rowling",
		"Harry Potter Series (1-7) by J.K. Rowling",
		"Harry Potter Series - 1-7",
		"Foundation Series by Isaac Asimov - 7 Books",
		"Foundation Series by Isaac Asimov - Full Series",
		"Foundation Series by Isaac Asimov - eBook Pack",
		"Foundation Series by Isaac Asimov - Kindle Edition",
		"Foundation Series by Isaac Asimov - Updated 2021",
		"Foundation Series by Isaac Asimov - Foundation, Foundation and Empire, Second Foundation",
		"Series by Isaac Asimov - Foundation and Empire and Second Foundation and Earth",
	} {
		if MultiBookPackMarker(title) == "" {
			t.Errorf("%q was not recognised as a multi-book pack", title)
		}
	}
}

// TestMultiBookPackMarker_FoundationSingles is the other half of the same
// search: single book releases and titles that share the pack words.
func TestMultiBookPackMarker_FoundationSingles(t *testing.T) {
	for _, tc := range []struct {
		title string
		why   string
	}{
		{"Foundation: HarperVoyager edition by Isaac Asimov, Book 1 EPUB", "the single book release from the same search"},
		{"Red Rising Series by Pierce Brown, Book 1 [M4B]", "series by with a book number is one book"},
		{"Dune Series by Frank Herbert - Vol. 2", "a volume number names one book"},
		{"A Series of Unfortunate Events: The Bad Beginning", "series of is a title"},
		{"Lemony Snicket - A Series of Unfortunate Events", "series of is a title"},
		{"The Complete Sherlock Holmes", "complete alone is one edition"},
		{"Collected Stories - Gabriel Garcia Marquez", "collected is a single book"},
		{"Isaac Asimov - Foundation Series Guide (2019)", "series in the middle of a title with no by or range"},
		{"Red Rising Series - Morning Star Part 1-2", "a part range is one split recording"},
		{"Red Rising Series - Book 1 - 2020 Edition", "an edition year is not a range"},
		{"The Foundation Series (2014-2016 reissue)", "a year span is not a range"},
		{"Foundation (1951) by Isaac Asimov (EPUB)", "a bracketed year and format"},
		{"Starter Pack Blues - Jane Doe", "pack inside a title with no bracket"},
		{"Six Books You Must Read - John Smith", "a count with books but no brackets"},
		{"The Series - Jane Doe", "series alone"},
		{"Discworld Series by Terry Pratchett - Guards! Guards!", "a title after the author is one book"},
		{"Foundation Series by Isaac Asimov - Foundation and Empire", "a title after the author is one book"},
		{"Harry Potter Series by J.K. Rowling - Harry Potter and the Goblet of Fire (EPUB)", "a title after the author is one book"},
		{"The Expanse Series by James S. A. Corey - Leviathan Wakes", "a title after the author is one book"},
		{"Mistborn Series by Brandon Sanderson: The Final Empire", "a title after a colon is one book"},
		{"Outlander Series by Diana Gabaldon - Dragonfly in Amber [MOBI]", "a title then a format tag is one book"},
		{"The Chronicles of Narnia Series by C.S. Lewis - The Lion, the Witch and the Wardrobe", "a title after the author is one book"},
		{"Series 1 - 2", "a spaced range with nothing around it"},
		{"The Chronicles of Narnia Series by C.S. Lewis, The Magician's Nephew", "one title after a comma is one book"},
		{"Foundation Series by Isaac Asimov - Foundation and Empire", "two titles joined by and is one title"},
		{"Discworld Series by Terry Pratchett - Guards! Guards! (Kindle Edition)", "a title then a format tag"},
		{"Discworld Series by Terry Pratchett: Men at Arms, Updated 2021", "one title then noise"},
		{"The Series 1 - 2 Handbook", "a range with no words between it and series is not a pack"},
		{"Foundation - Isaac Asimov [1 Book]", "a count of one is not a bundle"},
		{"The Poe Book Collection", "singular book collection is one volume"},
	} {
		if got := MultiBookPackMarker(tc.title); got != "" {
			t.Errorf("%q matched %q but is a single book (%s)", tc.title, got, tc.why)
		}
	}
}
