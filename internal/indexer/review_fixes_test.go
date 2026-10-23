package indexer

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Tests for the review round on #3268: the series title skip, the known
// position only series number rule, the batch rule's same book and same media
// type condition, the position reader, and the fragment filter's gaps.

type seriesCase struct {
	name          string
	title, author string
	release       string
	bc            bookContext
}

// TestSeriesTitleKeepsTheRightBook: a release that names the wanted book's
// series must not be read as naming another title when the library holds a
// series named entry.
func TestSeriesTitleKeepsTheRightBook(t *testing.T) {
	rotten := bookContext{seriesPosition: "12", seriesTitle: "Rotten School", otherTitles: []string{"Rotten School"}}
	katt := bookContext{seriesPosition: "2", seriesTitle: "Katt vs. Dogg", otherTitles: []string{"Katt vs. Dogg"}}
	druid := bookContext{seriesPosition: "1", seriesTitle: "High Druid of Shannara", otherTitles: []string{"High Druid of Shannara"}}
	kingkiller := bookContext{seriesPosition: "1", seriesTitle: "The Kingkiller Chronicle", otherTitles: []string{"The Kingkiller Chronicle", "The Wise Man's Fear"}}
	wot := bookContext{seriesPosition: "2", seriesTitle: "The Wheel of Time", otherTitles: []string{"The Wheel of Time", "The Eye of the World"}}
	hobbit := bookContext{otherTitles: []string{"The Hobbit: There and Back Again"}}
	cases := []seriesCase{
		{"Rotten School dash", "Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys - Rotten School 12 (epub)", rotten},
		{"Rotten School book", "Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys (Rotten School Book 12) (epub)", rotten},
		{"Rotten School hash", "Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys (Rotten School, #12) (epub)", rotten},
		{"Katt book", "Katt Loves Dogg", "James Patterson", "James Patterson - Katt Loves Dogg (Katt vs. Dogg Book 2) (epub)", katt},
		{"Katt dash", "Katt Loves Dogg", "James Patterson", "James Patterson - Katt Loves Dogg - Katt vs Dogg 02 (epub)", katt},
		{"Jarka Ruus", "Jarka Ruus", "Terry Brooks", "Terry Brooks - Jarka Ruus - High Druid of Shannara 1 (epub)", druid},
		{"Kingkiller number", "The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - The Name of the Wind - The Kingkiller Chronicle 1 (epub)", kingkiller},
		{"Kingkiller book", "The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - The Name of the Wind (Kingkiller Chronicle Book 1)", kingkiller},
		{"Kingkiller day one", "The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - The Name of the Wind - Kingkiller Chronicle Day One", kingkiller},
		{"Wheel of Time", "The Great Hunt", "Robert Jordan", "Robert Jordan - The Great Hunt - Wheel of Time 2 (epub)", wot},
		{"Hobbit subtitle library title", "The Hobbit", "J.R.R. Tolkien", "J.R.R. Tolkien - The Hobbit - There and Back Again (Unabridged) MP3", hobbit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, c.bc)
			if !kept {
				t.Errorf("release %q dropped for %q (%s)", c.release, c.title, reason)
			}
		})
	}
}

// TestSeriesTitleSkipLimits pins what the skip does not cover. Where a skip
// needs data the filter does not have it is a documented limit, not a guess.
func TestSeriesTitleSkipLimits(t *testing.T) {
	// The wanted entry is the series itself and the library holds one of its
	// books: the filter cannot know "The Final Empire" is Mistborn's own
	// subtitle, so the release is read as naming another title.
	kept, _ := relevanceVerdict("Mistborn", "Brandon Sanderson",
		"Brandon Sanderson - Mistborn - The Final Empire (epub)",
		bookContext{otherTitles: []string{"The Final Empire"}})
	if kept {
		t.Errorf("limit changed: a library title after the wanted series entry is read as another title")
	}
	kept, _ = relevanceVerdict("The Lord of the Rings", "J.R.R. Tolkien",
		"J.R.R. Tolkien - The Lord of the Rings - The Fellowship of the Ring",
		bookContext{otherTitles: []string{"The Fellowship of the Ring"}})
	if kept {
		t.Errorf("limit changed: a member title after a series named wanted entry is read as another title")
	}
}

func TestTrailingRulesSkipTitlesContainedInTheWantedTitle(t *testing.T) {
	tr := newTrailingRules("The Hobbit: There and Back Again", bookContext{otherTitles: []string{"There and Back Again"}},
		titleIdentityKeywords("The Hobbit: There and Back Again"))
	if len(tr.others) != 0 {
		t.Errorf("a library title contained in the wanted title must not count as another title, got %d", len(tr.others))
	}
	tr = newTrailingRules("Mistborn", bookContext{seriesTitle: "The Wheel of Time", otherTitles: []string{"Wheel of Time Companion", "Wheel of Time", "Warbreaker Saga Two"}},
		titleIdentityKeywords("Mistborn"))
	for _, ot := range tr.others {
		for _, w := range ot.ident {
			if w == "wheel" {
				t.Errorf("a library title related to the series title must be skipped, kept %v", ot.ident)
			}
		}
	}
}

// TestKnownPositionNumberRule pins every clause of signal A: with the position
// known a number after the title rejects only when it ends the name, is
// followed by punctuation, or sits behind a marker word. A number followed by
// an ordinary word is a count and never rejects.
func TestKnownPositionNumberRule(t *testing.T) {
	bc := bookContext{seriesPosition: "1"}
	rejects := []string{
		"Frank Herbert - Dune 2",
		"Frank Herbert - Dune 02",
		"Frank Herbert - Dune 2 - Dune Messiah",
		"Frank Herbert - Dune 2: Dune Messiah",
		"Frank Herbert - Dune 2, Dune Messiah",
		"Frank Herbert - Dune 2. Dune Messiah",
		"Frank Herbert - Dune (2) epub",
		"Frank Herbert - Dune [2] epub",
		"Frank Herbert - Dune 2 (retail) (epub)",
		"Frank Herbert - Dune Book 2 epub",
		"Frank Herbert - Dune Vol. 2 epub",
		"Frank Herbert - Dune No 3 mp3",
		"Frank Herbert - Dune Part 2 mp3",
		"Frank Herbert - Dune #2 epub",
		"Frank Herbert - Dune Episode 2 files",
	}
	keeps := []string{
		"Frank Herbert - Dune 12 files",
		"Frank Herbert - Dune (16 parts) MP3",
		"Frank Herbert - Dune - 3 Formats (epub mobi pdf)",
		"Frank Herbert - Dune 2 vols",
		"Frank Herbert - Dune 4 mp3",
		"Frank Herbert - Dune 12 m4b",
		"Frank Herbert - Dune 3 Tapes",
		"Frank Herbert - Dune 24 bit",
		"Frank Herbert - Dune 64 kbps MP3",
		"Frank Herbert - Dune 2 CD",
		"Frank Herbert - Dune 12 hours",
		"Frank Herbert - Dune 2 of 2",
		"Frank Herbert - Dune 2 retail epub",
		"Frank Herbert - Dune Part 2 of 2 MP3",
		"Frank Herbert - Dune 1965 retail epub",
		"Frank Herbert - Dune 01 (Unabridged) MP3",
		"Frank Herbert - Dune.2.Messiah.epub",
	}
	for _, rel := range rejects {
		kept, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bc)
		if kept || reason != reasonLaterNumber {
			t.Errorf("%q: kept=%v reason=%q, want rejected as %q", rel, kept, reason, reasonLaterNumber)
		}
	}
	for _, rel := range keeps {
		if kept, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bc); !kept {
			t.Errorf("%q dropped (%s), want kept", rel, reason)
		}
	}
	// The number equal to the position, or inside a stored range, never rejects.
	for _, rel := range []string{"Frank Herbert - Dune 2", "Frank Herbert - Dune Book 2 - Messiah"} {
		if kept, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bookContext{seriesPosition: "2"}); !kept {
			t.Errorf("%q dropped at position 2 (%s)", rel, reason)
		}
		if kept, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bookContext{seriesPosition: "1-3"}); !kept {
			t.Errorf("%q dropped at range 1-3 (%s)", rel, reason)
		}
	}
	if kept, _ := relevanceVerdict("Dune", "Frank Herbert", "Frank Herbert - Dune 4", bookContext{seriesPosition: "1-3"}); kept {
		t.Errorf("a number outside the stored range must reject")
	}
}

// TestUnknownPositionNumberIsNotJudged: with no stored position a number after
// the title is never judged on its own, whatever follows it.
func TestUnknownPositionNumberIsNotJudged(t *testing.T) {
	releases := []struct{ title, author, release string }{
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Full Cast"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 02 - Dramatized MP3"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Scott Brick"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Audible"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Kindle"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Lossless"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - Tantor"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - New Edition"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - British"},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune 2 - [GRP]"},
		{"Dune", "Frank Herbert", "Frank.Herbert-Dune.2-GRP"},
		{"Naruto", "Masashi Kishimoto", "Masashi Kishimoto - Naruto 03 - Chapter name (cbz)"},
	}
	for _, c := range releases {
		if kept, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{}); !kept {
			t.Errorf("%q dropped with no position (%s)", c.release, reason)
		}
	}
}

// TestReporterExamplesNeedPositionOrLibraryTitle lists which reporter examples
// are caught only with the position or a library title: without either they
// are approved, which is the documented limit.
func TestReporterExamplesNeedPositionOrLibraryTitle(t *testing.T) {
	cases := []struct{ title, author, release string }{
		{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars 02 - Rule of Wolves.par2"},
		{"Ships of Merior", "Janny Wurts", "Janny.Wurts-[Wars.of.Light.and.Shadow.03-Ships.of.Merior.02]-Warhost.of.Vastmark.(retail).(epub"},
		{"Katt vs. Dogg", "James Patterson", "James Patterson &amp; Chris Grabenstein - [Katt vs Dogg 02] - Katt Loves Dogg (retail) (epub)"},
		{"Percy Jackson & the Olympians", "Rick Riordan", "Rick Riordan - [Percy Jackson and the Olympians 04] - The Battle of the Labyrinth (US) (retail) (epub)"},
		{"Rotten School", "R. L. Stine", "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub) (epub)"},
		{"High Druid of Shannara: Jarka Ruus", "Terry Brooks", "Terry Brooks - The High Druid of Shannara 3 - Straken MP3"},
	}
	for _, c := range cases {
		if kept, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{}); !kept {
			t.Errorf("%q dropped with no position and no library title (%s); the limit is documented as approved", c.release, reason)
		}
	}
	// With the position every one of them is rejected.
	for _, c := range cases {
		if kept, _ := relevanceVerdict(c.title, c.author, c.release, bookContext{seriesPosition: "1"}); kept {
			t.Errorf("%q kept with position 1", c.release)
		}
	}
}

// TestBatchRuleNeedsTheSameBookInTheSameMedia: a full title result counts for
// the batch rule only when it is the same media type as the main title only
// release and is the book, not a summary or study aid.
func TestBatchRuleNeedsTheSameBookInTheSameMedia(t *testing.T) {
	const (
		title  = "Sapiens: A Brief History of Humankind"
		author = "Yuval Noah Harari"
		main   = "Yuval Noah Harari - Sapiens (2014) MP3"
	)
	keptWith := []string{
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Summary and Analysis (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Study Guide (pdf)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Workbook (pdf)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Companion Guide (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Derek Perkins (epub)",
	}
	for _, other := range keptWith {
		got := batchVerdicts(title, author, []string{main, other}, bookContext{})
		if v, ok := got[main]; !ok || v != "" {
			t.Errorf("main title release dropped (%q) next to %q", v, other)
		}
	}
	// Same media, the book itself: the sibling still goes.
	for _, other := range []string{
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Unabridged) M4B",
		"Yuval Noah Harari - Sapiens - A Brief History of Humankind (2014) 64kbps MP3",
	} {
		got := batchVerdicts(title, author, []string{main, other}, bookContext{})
		if got[main] != reasonMainTitleOnlyFullFound {
			t.Errorf("main title release verdict %q next to %q, want %q", got[main], other, reasonMainTitleOnlyFullFound)
		}
		if got[other] != "" {
			t.Errorf("the full title release was dropped (%q): %q", other, got[other])
		}
	}
	// Ebook sibling of an ebook full title still goes.
	got := batchVerdicts(title, author, []string{
		"Yuval Noah Harari - Sapiens (2014) EPUB",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (2014) EPUB",
	}, bookContext{})
	if got["Yuval Noah Harari - Sapiens (2014) EPUB"] != reasonMainTitleOnlyFullFound {
		t.Errorf("ebook sibling of an ebook full title should go, got %q", got["Yuval Noah Harari - Sapiens (2014) EPUB"])
	}
	// A main title release with no format word has no media type: it is kept.
	got = batchVerdicts(title, author, []string{
		"Yuval Noah Harari - Sapiens",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Unabridged) M4B",
	}, bookContext{})
	if got["Yuval Noah Harari - Sapiens"] != "" {
		t.Errorf("a main title release of undetermined media type must be kept, got %q", got["Yuval Noah Harari - Sapiens"])
	}
}

func TestBatchRuleOtherTitles(t *testing.T) {
	cases := []struct{ title, author, main, summary string }{
		{"Atomic Habits: An Easy and Proven Way to Build Good Habits and Break Bad Ones", "James Clear",
			"James Clear - Atomic Habits (2018) MP3",
			"James Clear - Atomic Habits: An Easy and Proven Way to Build Good Habits and Break Bad Ones - Summary (epub)"},
		{"The Hobbit: Or There and Back Again", "J.R.R. Tolkien",
			"J.R.R. Tolkien - The Hobbit MP3",
			"J.R.R. Tolkien - The Hobbit: Or There and Back Again - Study Guide (pdf)"},
		{"Cosmos: A Personal Voyage", "Carl Sagan",
			"Carl Sagan - Cosmos (1980) MP3",
			"Carl Sagan - Cosmos: A Personal Voyage - Notes (pdf)"},
		{"Quiet: The Power of Introverts in a World That Can't Stop Talking", "Susan Cain",
			"Susan Cain - Quiet MP3",
			"Susan Cain - Quiet: The Power of Introverts in a World That Can't Stop Talking - Highlights (epub)"},
		{"Harry Potter: The Philosopher's Stone", "J.K. Rowling",
			"J.K. Rowling - Harry Potter (Unabridged) MP3",
			"J.K. Rowling - Harry Potter: The Philosopher's Stone - Companion (epub)"},
	}
	for _, c := range cases {
		got := batchVerdicts(c.title, c.author, []string{c.main, c.summary}, bookContext{})
		if v, ok := got[c.main]; !ok || v != "" {
			t.Errorf("%q: main title release dropped (%q) next to %q", c.title, v, c.summary)
		}
	}
}

// TestMainTitleSeriesTitleAndPositionReader: the series title skip applies to
// the "main title is another library book" check, and the number reader
// accepts the spellings a release uses for the wanted number.
func TestMainTitleSeriesTitleAndPositionReader(t *testing.T) {
	kept, reason := relevanceVerdict("Mistborn: The Final Empire", "Brandon Sanderson",
		"Brandon Sanderson - Mistborn (2006) MP3",
		bookContext{seriesTitle: "Mistborn", otherTitles: []string{"Mistborn", "The Well of Ascension"}})
	if !kept {
		t.Errorf("a library entry named like the series must not make the main title another book: %s", reason)
	}
	kept, _ = relevanceVerdict("Mistborn: The Final Empire", "Brandon Sanderson",
		"Brandon Sanderson - Mistborn (2006) MP3",
		bookContext{otherTitles: []string{"Mistborn"}})
	if kept {
		t.Errorf("without a series title the library Mistborn still makes the main title another book")
	}

	wanted := bookContext{seriesPosition: "2", otherTitles: []string{"Mistborn", "The Dark Tower"}}
	wantKept := []string{
		"Brandon Sanderson - Mistborn Book 2 MP3",
		"Brandon Sanderson - Mistborn #2 MP3",
		"Brandon Sanderson - Mistborn 02 MP3",
		"Brandon Sanderson - Mistborn Vol. 2 MP3",
		"Brandon Sanderson - Mistborn Book Two MP3",
		"Brandon Sanderson - Mistborn II MP3",
		"Brandon Sanderson - Mistborn S1 Part 2 MP3",
		"Brandon Sanderson - Mistborn Series 02 MP3",
		"Brandon Sanderson - Mistborn Season 2 MP3",
		"Brandon Sanderson - Mistborn Part Two MP3",
	}
	for _, rel := range wantKept {
		if k, reason := relevanceVerdict("Mistborn: The Well of Ascension", "Brandon Sanderson", rel, wanted); !k {
			t.Errorf("%q dropped at position 2 (%s)", rel, reason)
		}
	}
	wantDropped := []string{
		"Brandon Sanderson - Mistborn (2006) MP3",
		"Brandon Sanderson - Mistborn Book Three MP3",
		"Brandon Sanderson - Mistborn III MP3",
		"Brandon Sanderson - Mistborn ii MP3",
		"Brandon Sanderson - Mistborn two MP3",
		"Brandon Sanderson - Mistborn Book 3 MP3",
	}
	for _, rel := range wantDropped {
		if k, _ := relevanceVerdict("Mistborn: The Well of Ascension", "Brandon Sanderson", rel, wanted); k {
			t.Errorf("%q kept at position 2, want dropped", rel)
		}
	}
	if k, reason := relevanceVerdict("The Dark Tower: The Drawing of the Three", "Stephen King",
		"Stephen King - The Dark Tower II (Unabridged) MP3", wanted); !k {
		t.Errorf("The Dark Tower II dropped at position 2 (%s)", reason)
	}
	// The wanted number a numbered subtitle names is read the same way.
	if k, reason := relevanceVerdict("Dune: Part Two", "Frank Herbert", "Frank Herbert - Dune II MP3", bookContext{}); !k {
		t.Errorf("Dune II dropped for Dune: Part Two (%s)", reason)
	}
	if k, _ := relevanceVerdict("Dune: Part Two", "Frank Herbert", "Frank Herbert - Dune III MP3", bookContext{}); k {
		t.Errorf("Dune III kept for Dune: Part Two")
	}
}

// TestMainTitleWantedNumberExemption pins the "carries the wanted number right
// after the main title" exemption and its limits.
func TestMainTitleWantedNumberExemption(t *testing.T) {
	bc := bookContext{seriesPosition: "2", otherTitles: []string{"Legion"}}
	for _, rel := range []string{
		"Brandon Sanderson - Legion 2 (2014) MP3",
		"Brandon Sanderson - Legion Book 2 MP3",
		"Brandon Sanderson - Legion II MP3",
	} {
		if k, reason := relevanceVerdict("Legion: Skin Deep", "Brandon Sanderson", rel, bc); !k {
			t.Errorf("%q dropped (%s)", rel, reason)
		}
	}
	for _, rel := range []string{
		"Brandon Sanderson - Legion 3 (2014) MP3",
		"Brandon Sanderson - Legion (2012) MP3",
		"Brandon Sanderson - Legion Novella 2 MP3",
	} {
		if k, _ := relevanceVerdict("Legion: Skin Deep", "Brandon Sanderson", rel, bc); k {
			t.Errorf("%q kept, want dropped", rel)
		}
	}
}

func TestSeriesLikeSubtitleRomanRules(t *testing.T) {
	for _, in := range []string{"ii", "iv", "Mix", "MIX", "MMXX", "XL"} {
		if n, ok := seriesLikeSubtitle(in); ok {
			t.Errorf("seriesLikeSubtitle(%q) = %d,true, want false", in, n)
		}
	}
	for in, want := range map[string]int{"II": 2, "XX": 20, "XXX": 30} {
		if n, ok := seriesLikeSubtitle(in); !ok || n != want {
			t.Errorf("seriesLikeSubtitle(%q) = %d,%v want %d,true", in, n, ok, want)
		}
	}
}

// Fragment filter gaps.

func TestFragmentCounterBoundaryAndSeriesCountWords(t *testing.T) {
	if isChapterFragment("Author - Some Story 3 of 9 MP3") {
		t.Errorf("MM of 9 must be kept")
	}
	if !isChapterFragment("Author - Some Story 3 of 10 MP3") {
		t.Errorf("MM of 10 must be dropped")
	}
	if isChapterFragment("Author - Some Story 3 of 10") != true {
		t.Errorf("MM of 10 with no format word must be dropped")
	}
	for _, w := range []string{"Book", "Vol", "Volume", "Series", "No", "Tome", "Band", "Season", "Episode", "Issue", "Disc", "CD", "#", "Bk", "Number"} {
		title := "Terry Pratchett - Discworld " + w + " 03 of 41"
		if w == "#" {
			title = "Terry Pratchett - Discworld #03 of 41"
		}
		if isChapterFragment(title) {
			t.Errorf("%q is a series count and must be kept", title)
		}
	}
	// A counter with no word in front is still a fragment; this is the limit.
	if !isChapterFragment("Terry Pratchett - Discworld 03 of 41") {
		t.Errorf("a bare counter with no format word is dropped")
	}
}

func TestFragmentEbookFormatsSpare(t *testing.T) {
	for _, f := range []string{"epub", "doc", "docx", "kfx", "ebook", "epub3", "kepub", "html", "htm", "lit", "prc", "djvu", "mobi", "azw3", "pdf"} {
		title := "Author - Some Story Chapter 12 " + f
		if isChapterFragment(title) {
			t.Errorf("%q names an ebook format and must be kept", title)
		}
	}
}

func TestFragmentUnderscoreSeparators(t *testing.T) {
	for _, title := range []string{
		"Author_Title_Ch63_of_71_mp3",
		"Author_Title_Chapter_12_m4b",
		"Author_Title_3_of_12_mp3",
		"Author.Title.Ch63.of.71.mp3",
	} {
		if !isChapterFragment(title) {
			t.Errorf("%q is a fragment and must be dropped", title)
		}
	}
	if isChapterFragment("Author_Title_Part_1_of_2_mp3") {
		t.Errorf("a two part whole book must be kept")
	}
}

// Wiring helpers.

func TestSetLibraryContextWithNoRepos(t *testing.T) {
	c := MatchCriteria{Title: "x"}
	c.SetLibraryContext(context.Background(), models.Book{ID: 1, AuthorID: 2}, nil, nil)
	if c.SeriesPosition != "" || c.SeriesTitle != "" || c.AuthorTitles != nil {
		t.Errorf("nil readers must leave the context empty, got %+v", c)
	}
	boom := errors.New("boom")
	c.SetLibraryContext(context.Background(), models.Book{ID: 1, AuthorID: 2},
		func(context.Context, int64) (string, string, error) { return "", "", boom },
		func(context.Context, int64) ([]models.Book, error) { return nil, boom })
	if c.SeriesPosition != "" || c.SeriesTitle != "" || c.AuthorTitles != nil {
		t.Errorf("failing readers must leave the context empty, got %+v", c)
	}
	c.SetLibraryContext(context.Background(), models.Book{ID: 1, AuthorID: 2},
		func(context.Context, int64) (string, string, error) { return "Rotten School", "12", nil },
		func(context.Context, int64) ([]models.Book, error) {
			return []models.Book{{ID: 1, Title: "Me"}, {ID: 3, Title: "Other"}}, nil
		})
	if c.SeriesTitle != "Rotten School" || c.SeriesPosition != "12" || len(c.AuthorTitles) != 1 || c.AuthorTitles[0] != "Other" {
		t.Errorf("readers not applied, got %+v", c)
	}
}
