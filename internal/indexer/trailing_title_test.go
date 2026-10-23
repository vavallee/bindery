package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// relevanceVerdict runs one release through the production relevance filter
// and returns whether it was kept and, when dropped, why.
func relevanceVerdict(title, author, release string, bc bookContext) (kept bool, reason string) {
	k, d := filterRelevantWithContext([]newznab.SearchResult{{Title: release}}, title, author, nil, bc)
	if len(k) == 1 {
		return true, ""
	}
	if len(d) == 1 {
		return false, d[0].Reason
	}
	return false, "no verdict"
}

// TestTrailingTitleRejectsAnotherBook pins #3268 part 1: every release the
// reporters showed being approved for the wrong book is now dropped, with a
// reason the interactive search panel can show.
func TestTrailingTitleRejectsAnotherBook(t *testing.T) {
	cases := []struct {
		name                   string
		title, author, release string
		bc                     bookContext
		reason                 string
	}{
		{
			name:  "King of Scars 02 names Rule of Wolves, library title",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh Bardugo - King of Scars 02 - Rule of Wolves.par2",
			bc:      bookContext{otherTitles: []string{"Rule of Wolves"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "King of Scars 02, wanted position 1",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh Bardugo - King of Scars 02 - Rule of Wolves.par2",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "King of Scars names Rule of Wolves with no number, known library title",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh Bardugo - King of Scars - Rule of Wolves (retail) (epub)",
			bc:      bookContext{otherTitles: []string{"Rule of Wolves", "Ruin and Rising"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Ships of Merior, scene style bracketed series counter, library title",
			title: "Ships of Merior", author: "Janny Wurts",
			release: "Janny.Wurts-[Wars.of.Light.and.Shadow.03-Ships.of.Merior.02]-Warhost.of.Vastmark.(retail).(epub",
			bc:      bookContext{otherTitles: []string{"Warhost of Vastmark"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Ships of Merior, wanted position 3",
			title: "Ships of Merior", author: "Janny Wurts",
			release: "Janny.Wurts-[Wars.of.Light.and.Shadow.03-Ships.of.Merior.02]-Warhost.of.Vastmark.(retail).(epub",
			bc:      bookContext{seriesPosition: "3"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "Katt vs Dogg 02, HTML escaped ampersand, library title",
			title: "Katt vs. Dogg", author: "James Patterson",
			release: "James Patterson &amp; Chris Grabenstein - [Katt vs Dogg 02] - Katt Loves Dogg (retail) (epub)",
			bc:      bookContext{otherTitles: []string{"Katt Loves Dogg"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Katt vs Dogg 02, position and library title",
			title: "Katt vs. Dogg", author: "James Patterson",
			release: "James Patterson & Chris Grabenstein - [Katt vs Dogg 02] - Katt Loves Dogg (retail) (epub)",
			bc:      bookContext{seriesPosition: "1", seriesTitle: "Katt vs. Dogg", otherTitles: []string{"Katt Loves Dogg"}},
			reason:  reasonLaterNumber,
		},
		{
			name:  "Katt vs Dogg 02, the series title does not hide another book of the series",
			title: "Katt vs. Dogg", author: "James Patterson",
			release: "James Patterson & Chris Grabenstein - Katt vs Dogg - Katt Loves Dogg (retail) (epub)",
			bc:      bookContext{seriesTitle: "Katt vs. Dogg", otherTitles: []string{"Katt Loves Dogg"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Percy Jackson series entry, book 4, library title",
			title: "Percy Jackson & the Olympians", author: "Rick Riordan",
			release: "Rick Riordan - [Percy Jackson and the Olympians 04] - The Battle of the Labyrinth (US) (retail) (epub)",
			bc:      bookContext{otherTitles: []string{"The Battle of the Labyrinth"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Percy Jackson series entry, wanted position 1",
			title: "Percy Jackson & the Olympians", author: "Rick Riordan",
			release: "Rick Riordan - [Percy Jackson and the Olympians 04] - The Battle of the Labyrinth (US) (retail) (epub)",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "Rotten School 12, library title",
			title: "Rotten School", author: "R. L. Stine",
			release: "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub) (epub)",
			bc:      bookContext{otherTitles: []string{"Battle of the Dum Diddys"}},
			reason:  reasonAnotherTitle,
		},
		{
			name:  "Rotten School 12, position known",
			title: "Rotten School", author: "R. L. Stine",
			release: "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub) (epub)",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "Rotten School 12 followed by a bracket, position known",
			title: "Rotten School", author: "R. L. Stine",
			release: "R L Stine - Rotten School 12 (retail) (epub)",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "underscore separated release, position known",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh_Bardugo_-_King_of_Scars_02_-_Rule_of_Wolves_(retail)_(epub)",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "digit directly before a dash and a word, position known",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh.Bardugo.King.of.Scars.02-Rule.of.Wolves.EPUB",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
		{
			name:  "Book marker then number, position known",
			title: "King of Scars", author: "Leigh Bardugo",
			release: "Leigh Bardugo - King of Scars Book 2 - Rule of Wolves (Unabridged) MP3",
			bc:      bookContext{seriesPosition: "1"},
			reason:  reasonLaterNumber,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, c.bc)
			if kept {
				t.Fatalf("release %q was kept for %q, want dropped with %q", c.release, c.title, c.reason)
			}
			if reason != c.reason {
				t.Errorf("reason = %q, want %q", reason, c.reason)
			}
		})
	}
}

// trailingNegative is one release that must still be approved for its own book.
type trailingNegative struct {
	title, author, release string
	bc                     bookContext
	spared                 string // the rule that spares it, for the reader
}

// trailingNegativeCorpus is the negative corpus for #3268 part 1: realistic
// release names that are the wanted book and must stay approved. They hold
// before the fix and after it.
var trailingNegativeCorpus = []trailingNegative{
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars (Unabridged) MP3", bookContext{}, "qualifier words after the title"},
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars 01 (2019) M4B", bookContext{seriesPosition: "1"}, "N is 1, below the threshold of 2"},
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - Nikolai Duology 01 - King of Scars (retail) (epub)", bookContext{}, "series prefix sits BEFORE the title"},
	{"King of Scars", "Leigh Bardugo", "King.of.Scars.by.Leigh.Bardugo.EPUB", bookContext{}, "nothing after the title but the author"},
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - [Nikolai Duology 01] - King of Scars (retail) (epub)", bookContext{seriesPosition: "1", otherTitles: []string{"Rule of Wolves"}}, "bracketed series tag before the title"},
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars (US) (retail) (epub)", bookContext{otherTitles: []string{"Rule of Wolves"}}, "(US) retail tags"},
	{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars (UK) [ENG] M4B", bookContext{}, "(UK) and [ENG] tags"},
	{"Rule of Wolves", "Leigh Bardugo", "Leigh Bardugo - King of Scars 02 - Rule of Wolves.epub", bookContext{seriesPosition: "2", otherTitles: []string{"King of Scars"}}, "the other title sits BEFORE the wanted one, a series prefix"},
	{"Dune", "Frank Herbert", "Frank Herbert - Dune (Unabridged) 64kbps MP3", bookContext{otherTitles: []string{"Dune Messiah", "Children of Dune"}}, "qualifier and bitrate"},
	{"Dune", "Frank Herbert", "Frank Herbert - Dune 1965 retail epub", bookContext{otherTitles: []string{"Dune Messiah"}}, "a four digit year is not a series number"},
	{"Dune", "Frank Herbert", "Frank Herbert - Dune - Narrated by Scott Brick [M4B]", bookContext{otherTitles: []string{"Dune Messiah"}}, "narrator credit"},
	{"Dune", "Frank Herbert", "Frank Herbert - Dune Chronicles 01 - Dune (retail) (epub)", bookContext{seriesPosition: "1"}, "series prefix before the title"},
	{"Dune Messiah", "Frank Herbert", "Frank Herbert - Dune Messiah (Unabridged) MP3", bookContext{seriesPosition: "2", otherTitles: []string{"Dune"}}, "the shorter title is a prefix of the wanted one, not in the remainder"},
	{"Mistborn: The Final Empire", "Brandon Sanderson", "Brandon Sanderson - Mistborn - The Final Empire (Unabridged) MP3", bookContext{seriesPosition: "1"}, "full title carries the subtitle"},
	{"Mistborn: The Final Empire", "Brandon Sanderson", "Brandon Sanderson - [Mistborn 01] - The Final Empire (retail) (epub)", bookContext{seriesPosition: "1"}, "series counter before the subtitle"},
	{"The Hobbit: Or There and Back Again", "J.R.R. Tolkien", "J.R.R. Tolkien - The Hobbit MP3", bookContext{}, "primary title reading, nothing after it"},
	{"Foundation", "Isaac Asimov", "Isaac Asimov - Foundation (Unabridged) MP3", bookContext{seriesPosition: "1", otherTitles: []string{"Foundation and Empire", "Second Foundation"}}, "qualifier only"},
	{"Foundation and Empire", "Isaac Asimov", "Isaac Asimov - Foundation Series 02 - Foundation and Empire (1952) retail epub", bookContext{seriesPosition: "2", otherTitles: []string{"Foundation"}}, "series prefix before the title"},
	{"Harry Potter and the Chamber of Secrets", "J.K. Rowling", "J.K. Rowling - Harry Potter 2 - Harry Potter and the Chamber of Secrets (Unabridged) MP3", bookContext{seriesPosition: "2"}, "series number before the title"},
	{"A Game of Thrones", "George R.R. Martin", "George R.R. Martin - A Game of Thrones 01 (Unabridged) 64kbps MP3", bookContext{seriesPosition: "1"}, "N is 1"},
	{"A Clash of Kings", "George R.R. Martin", "George R.R. Martin - A Song of Ice and Fire 02 - A Clash of Kings (retail) (epub)", bookContext{seriesPosition: "2", otherTitles: []string{"A Game of Thrones", "A Storm of Swords"}}, "known title before the wanted one"},
	{"Katt vs. Dogg", "James Patterson", "James Patterson & Chris Grabenstein - [Katt vs Dogg 01] - Katt vs Dogg (retail) (epub)", bookContext{seriesPosition: "1"}, "N is 1, the book itself repeated"},
	{"Percy Jackson & the Olympians Box Set", "Rick Riordan", "Rick Riordan - Percy Jackson and the Olympians Box Set - The Lightning Thief, The Sea of Monsters, The Titan's Curse, The Battle of the Labyrinth, The Last Olympian (retail) (epub)", bookContext{otherTitles: []string{"The Lightning Thief", "The Sea of Monsters", "The Titan's Curse", "The Battle of the Labyrinth", "The Last Olympian"}}, "the wanted entry IS the set, so member titles are expected"},
	{"Percy Jackson and the Olympians", "Rick Riordan", "Rick Riordan - [Percy Jackson and the Olympians 04] - The Battle of the Labyrinth (US) (retail) (epub)", bookContext{seriesPosition: "4", otherTitles: []string{"The Lightning Thief"}}, "N equals the wanted position and no library title follows"},
	{"Rotten School", "R. L. Stine", "R L Stine - Rotten School (retail) (epub)", bookContext{}, "nothing after the title"},
	{"High Druid of Shannara: Jarka Ruus", "Terry Brooks", "Terry Brooks - High Druid of Shannara 01 - Jarka Ruus (Unabridged) MP3", bookContext{seriesPosition: "1"}, "full title with the series number in the middle"},
	{"High Druid of Shannara: Jarka Ruus", "Terry Brooks", "Terry Brooks - Jarka Ruus (High Druid of Shannara 1) (retail) (epub)", bookContext{seriesPosition: "1"}, "subtitle first then the series tag"},
	{"Legion: Skin Deep", "Brandon Sanderson", "Brandon Sanderson - Legion - Skin Deep (2014) MP3", bookContext{seriesPosition: "2", otherTitles: []string{"Legion"}}, "full title"},
	{"Legion: Skin Deep", "Brandon Sanderson", "Brandon Sanderson - [Legion 02] - Legion- Skin Deep (mobi)", bookContext{seriesPosition: "2", otherTitles: []string{"Legion"}}, "full title after the series counter"},
	{"The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - The Name of the Wind (Unabridged) (2007) 128kbps MP3 Narrated by Nick Podehl", bookContext{seriesPosition: "1"}, "year, bitrate and narrator"},
	{"The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - [Kingkiller Chronicle 01] - The Name of the Wind (retail) (epub)", bookContext{seriesPosition: "1", otherTitles: []string{"The Wise Man's Fear"}}, "bracketed series tag before the title"},
	{"The Name of the Wind", "Patrick Rothfuss", "The.Name.of.the.Wind.Patrick.Rothfuss.RETAIL.EPUB-BitBook", bookContext{seriesPosition: "1", otherTitles: []string{"The Wise Man's Fear"}}, "scene name with release group"},
	{"The Way of Kings", "Brandon Sanderson", "Brandon Sanderson - The Way of Kings 01 (Unabridged) M4B", bookContext{seriesPosition: "1", otherTitles: []string{"Words of Radiance"}}, "N is 1"},
	{"The Way of Kings", "Brandon Sanderson", "Brandon Sanderson - The Way of Kings Part 1 of 2 (Unabridged) MP3", bookContext{seriesPosition: "1"}, "Part 1 of 2 is a qualifier"},
	{"Words of Radiance", "Brandon Sanderson", "Brandon Sanderson - Words of Radiance Part 2 of 2 (Unabridged) MP3", bookContext{}, "Part N of M counter is out of scope for the number rule"},
	{"Words of Radiance", "Brandon Sanderson", "Brandon Sanderson - Words of Radiance, Book 2 of the Stormlight Archive (retail) (epub)", bookContext{}, "Book N of a series name, followed by of"},
	{"Words of Radiance", "Brandon Sanderson", "Brandon Sanderson - Words of Radiance #2 (Unabridged) 2014 MP3", bookContext{seriesPosition: "2"}, "N equals the wanted position"},
	{"Words of Radiance", "Brandon Sanderson", "Brandon Sanderson - Words of Radiance #2 (Unabridged) 2014 MP3", bookContext{}, "N of 2 or more with no dash and no title words after it, position unknown"},
	{"The Eye of the World", "Robert Jordan", "Robert Jordan - The Wheel of Time 01 - The Eye of the World [64kbps MP3] Narrated by Kate Reading & Michael Kramer", bookContext{seriesPosition: "1"}, "series prefix, bitrate, narrators"},
	{"The Eye of the World", "Robert Jordan", "Robert Jordan - The Eye of the World - Wheel of Time Book 1 - 2013 retail epub", bookContext{seriesPosition: "1", otherTitles: []string{"The Great Hunt"}}, "Book 1 equal to the wanted position"},
	{"The Wishsong of Shannara", "Terry Brooks", "Terry Brooks - The Wishsong of Shannara Book 3 (retail) (epub)", bookContext{seriesPosition: "3"}, "Book 3 equal to the wanted position"},
	{"The Wishsong of Shannara", "Terry Brooks", "Terry Brooks - The Wishsong of Shannara 3 (retail) (epub)", bookContext{}, "bare number, no dash, no words after"},
	{"Ender's Game", "Orson Scott Card", "Orson Scott Card - Ender's Game 2 CD (Unabridged) MP3", bookContext{}, "a disc count is not a series number"},
	{"Ender's Game", "Orson Scott Card", "Orson Scott Card - Ender's Game 64 kbps MP3", bookContext{}, "a bitrate is not a series number"},
	{"Ender's Game", "Orson Scott Card", "Orson Scott Card - Ender's Game 12 hours (Unabridged) MP3", bookContext{}, "a running time is not a series number"},
	{"Fahrenheit 451", "Ray Bradbury", "Ray Bradbury - Fahrenheit 451 (Unabridged) MP3", bookContext{}, "a number inside the wanted title"},
	{"Catch-22", "Joseph Heller", "Joseph Heller - Catch 22 - Unabridged MP3", bookContext{}, "a number that ends the wanted title"},
	{"1984", "George Orwell", "George Orwell - 1984 (Unabridged) MP3", bookContext{}, "a title that is a number"},
	{"The Martian", "Andy Weir", "Andy Weir - The Martian (Unabridged) (2013) MP3 Narrated by R. C. Bray - repack", bookContext{otherTitles: []string{"Artemis", "Project Hail Mary"}}, "year, narrator and repack tag"},
	{"The Martian", "Andy Weir", "Andy Weir - The Martian #1 retail epub", bookContext{}, "#1"},
	{"Project Hail Mary", "Andy Weir", "Andy Weir - Project Hail Mary - 2021 - Ray Porter - M4B [ENG]", bookContext{otherTitles: []string{"The Martian", "Artemis"}}, "year, narrator, format and language tag"},
	{"Guards! Guards!", "Terry Pratchett", "Terry Pratchett - Discworld 08 - Guards! Guards! (retail) (epub)", bookContext{seriesPosition: "8", otherTitles: []string{"Small Gods", "Men at Arms"}}, "series prefix before the title"},
	{"The Stand", "Stephen King", "Stephen King - The Stand (Complete & Uncut) (Unabridged) MP3", bookContext{}, "edition words"},
	{"The Stand", "Stephen King", "Stephen King - The Stand - REPACK - retail epub", bookContext{}, "repack and retail tags"},
	{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "Yuval Noah Harari - Sapiens - A Brief History of Humankind (Unabridged) MP3", bookContext{}, "subtitle present"},
	{"Elantris", "Brandon Sanderson", "Brandon Sanderson - Elantris (Unabridged) Read by Jack Garrett MP3", bookContext{}, "read by credit"},
	{"Elantris", "Brandon Sanderson", "Brandon Sanderson - Elantris 2005 EPUB-RETAIL", bookContext{}, "year"},
}

func TestTrailingTitleNegativeCorpusStaysApproved(t *testing.T) {
	if len(trailingNegativeCorpus) < 40 {
		t.Fatalf("negative corpus has %d entries, want at least 40", len(trailingNegativeCorpus))
	}
	for _, c := range trailingNegativeCorpus {
		t.Run(c.release, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, c.bc)
			if !kept {
				t.Errorf("wanted %q: release %q dropped (%s); spared by: %s", c.title, c.release, reason, c.spared)
			}
		})
	}
}

// TestTrailingTitleNegativesNeedNoContext pins that the corpus does not
// depend on the library context: with no position and no other titles every
// release is still approved.
func TestTrailingTitleNegativesNeedNoContext(t *testing.T) {
	for _, c := range trailingNegativeCorpus {
		t.Run(c.release, func(t *testing.T) {
			kept, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{})
			if !kept {
				t.Errorf("wanted %q: release %q dropped with no context (%s)", c.title, c.release, reason)
			}
		})
	}
}
