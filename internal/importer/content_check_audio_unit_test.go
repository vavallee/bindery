package importer

import (
	"fmt"
	"testing"
)

// tracks builds n AudioTags values that all carry the given album, with
// numbered chapter titles, the way an audiobook rip is tagged.
func tracksWithAlbum(album string, n int) []AudioTags {
	out := make([]AudioTags, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, AudioTags{Album: album, Title: fmt.Sprintf("Chapter %02d", i)})
	}
	return out
}

// tracksWithTitle builds n AudioTags with no album and the same track title.
func tracksWithTitle(title string, n int) []AudioTags {
	out := make([]AudioTags, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, AudioTags{Title: title})
	}
	return out
}

func join(parts ...[]AudioTags) []AudioTags {
	var out []AudioTags
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mismatchCount(r audiobookContentResult, title string) int {
	for _, m := range r.mismatches {
		if m.title == title {
			return m.count
		}
	}
	return 0
}

// The #3246 acceptance cases that must be flagged (cases 1 to 4).
func TestEvaluateAudiobookContent_Flagged(t *testing.T) {
	cases := []struct {
		name    string
		wanted  string
		tags    []AudioTags
		matches int
		others  map[string]int
	}{
		{
			name:    "1 Chase, 2 own files and two other books",
			wanted:  "Chase",
			tags:    join(tracksWithAlbum("Chase", 2), tracksWithAlbum("Corralled", 9), tracksWithAlbum("Coming Home to Myself", 8)),
			matches: 2,
			others:  map[string]int{"Corralled": 9, "Coming Home to Myself": 8},
		},
		{
			name:    "2 Firm, 60 of 288 files are the book and 228 are The Runaway Jury",
			wanted:  "The Firm",
			tags:    join(tracksWithAlbum("The Firm", 60), tracksWithAlbum("The Runaway Jury", 228)),
			matches: 60,
			others:  map[string]int{"The Runaway Jury": 228},
		},
		{
			name:    "3 The End, 14 files and 2 of The Winter Sea",
			wanted:  "The End",
			tags:    join(tracksWithAlbum("The End", 14), tracksWithAlbum("The Winter Sea", 2)),
			matches: 14,
			others:  map[string]int{"The Winter Sea": 2},
		},
		{
			name:   "4a NYPD Red 3 wanted, 161 tracks across four albums",
			wanted: "NYPD Red 3",
			tags: join(tracksWithAlbum("NYPD Red 4", 83), tracksWithAlbum("Coffin Road", 33),
				tracksWithAlbum("Cold Frame", 26), tracksWithAlbum("Artful", 19)),
			matches: 0,
			others:  map[string]int{"NYPD Red 4": 83, "Coffin Road": 33, "Cold Frame": 26, "Artful": 19},
		},
		{
			name:    "4b NYPD Red 3 wanted, every track is NYPD Red 4 (volume conflict rule)",
			wanted:  "NYPD Red 3",
			tags:    tracksWithAlbum("NYPD Red 4", 161),
			matches: 0,
			others:  map[string]int{"NYPD Red 4": 161},
		},
		{
			name:    "no album, the other book is named by track titles shared by two or more files",
			wanted:  "Chase",
			tags:    join(tracksWithTitle("Chase", 3), tracksWithTitle("Corralled", 5)),
			matches: 3,
			others:  map[string]int{"Corralled": 5},
		},
		{
			name:    "two files of another book beside the real one",
			wanted:  "Chase",
			tags:    join(tracksWithAlbum("Chase", 12), tracksWithAlbum("Corralled", 2)),
			matches: 12,
			others:  map[string]int{"Corralled": 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := evaluateAudiobookContent(c.wanted, c.tags)
			if !r.flagged {
				t.Fatalf("not flagged: %+v", r)
			}
			if r.tracksRead != len(c.tags) {
				t.Errorf("tracksRead = %d, want %d", r.tracksRead, len(c.tags))
			}
			if r.matches != c.matches {
				t.Errorf("matches = %d, want %d", r.matches, c.matches)
			}
			for title, n := range c.others {
				if got := mismatchCount(r, title); got != n {
					t.Errorf("mismatch %q = %d, want %d (all: %+v)", title, got, n, r.mismatches)
				}
			}
		})
	}
}

// The #3246 acceptance cases that must import silently (cases 5 to 9), and the
// boundaries of the flag rules.
func TestEvaluateAudiobookContent_Silent(t *testing.T) {
	cases := []struct {
		name   string
		wanted string
		tags   []AudioTags
	}{
		{"5 76 chapter titles, album equals the wanted title", "The Hobbit", tracksWithAlbum("The Hobbit", 76)},
		{"5b chapter titles with no album at all", "The Hobbit", func() []AudioTags {
			out := make([]AudioTags, 0, 76)
			for i := 1; i <= 76; i++ {
				out = append(out, AudioTags{Title: fmt.Sprintf("Chapter %d", i)})
			}
			return out
		}()},
		{"6 a folder with no tags", "The Hobbit", make([]AudioTags, 12)},
		{"6b no files read at all", "The Hobbit", nil},
		{"7 album is the main title, wanted carries a subtitle and a volume", "Dominion of Blades: A LitRPG Adventure (Volume 1)",
			tracksWithAlbum("Dominion of Blades", 30)},
		{"8 unique chapter names with an album equal to the wanted title", "The Hobbit", func() []AudioTags {
			out := make([]AudioTags, 0, 20)
			for i := 0; i < 20; i++ {
				out = append(out, AudioTags{Album: "The Hobbit", Title: fmt.Sprintf("An Unexpected Party %d", i)})
			}
			return out
		}()},
		{"8b unique track titles with no album", "The Hobbit", func() []AudioTags {
			out := make([]AudioTags, 0, 20)
			for i := 0; i < 20; i++ {
				out = append(out, AudioTags{Title: fmt.Sprintf("Riddles in the Dark %d", i)})
			}
			return out
		}()},
		{"9 two excerpts as albums beside a matching folder", "The Hobbit",
			join(tracksWithAlbum("The Hobbit", 30), tracksWithAlbum("Excerpt from The Silmarillion", 2))},
		{"9b two excerpts as track titles beside a matching folder", "The Hobbit",
			join(tracksWithAlbum("The Hobbit", 30), []AudioTags{{Title: "Excerpt from Dune"}, {Title: "Excerpt from Dune"}})},
		{"9c promo and credits as albums", "The Hobbit",
			join(tracksWithAlbum("The Hobbit", 30), tracksWithAlbum("Also by J. R. R. Tolkien", 3), tracksWithAlbum("Opening Credits", 2))},
		{"one stray file of another book", "The Hobbit", join(tracksWithAlbum("The Hobbit", 30), tracksWithAlbum("Dune", 1))},
		{"every file is one other title (a translated or retitled edition)", "The Hobbit", tracksWithAlbum("Der Hobbit", 40)},
		{"two files of one other title and nothing else", "The Hobbit", tracksWithAlbum("Der Hobbit", 2)},
		{"short title is supported by a longer one", "Dune", tracksWithAlbum("Dune Messiah", 30)},
		{"series suffix on the album", "Mary, Mary", tracksWithAlbum("Mary, Mary: Alex Cross, Book 11", 20)},
		{"series suffix on the wanted title", "Mary, Mary: Alex Cross, Book 11", tracksWithAlbum("Mary, Mary", 20)},
		{"a single other file with the wanted tracks", "Chase", join(tracksWithAlbum("Chase", 5), tracksWithAlbum("Corralled", 1))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r := evaluateAudiobookContent(c.wanted, c.tags); r.flagged {
				t.Fatalf("flagged, want silent: %+v", r)
			}
		})
	}
}

func TestEvaluateAudiobookContent_FallbackNeedsTwoFiles(t *testing.T) {
	// Five different track titles, no album: nothing is a work.
	tags := []AudioTags{{Title: "Corralled"}, {Title: "Runaway"}, {Title: "Coffin Road"}, {Title: "Artful"}, {Title: "Cold Frame"}}
	if r := evaluateAudiobookContent("Chase", tags); r.flagged || len(r.mismatches) != 0 {
		t.Errorf("unique track titles counted as other works: %+v", r)
	}
	// Two files sharing a title do count.
	tags = append(tags, AudioTags{Title: "Artful"}, AudioTags{Title: "Chase"}, AudioTags{Title: "Chase"})
	r := evaluateAudiobookContent("Chase", tags)
	if !r.flagged || mismatchCount(r, "Artful") != 2 {
		t.Errorf("a track title shared by two files was not counted: %+v", r)
	}
}

func TestEvaluateAudiobookContent_AlbumBeatsTrackTitle(t *testing.T) {
	// The album names the book, so a track title that happens to name another
	// work (a chapter called "Corralled") says nothing.
	tags := make([]AudioTags, 0, 10)
	for i := 0; i < 10; i++ {
		tags = append(tags, AudioTags{Album: "Chase", Title: "Corralled"})
	}
	if r := evaluateAudiobookContent("Chase", tags); r.flagged {
		t.Errorf("track titles overrode the album: %+v", r)
	}
}

func TestEvaluateAudiobookContent_VolumeConflictRule(t *testing.T) {
	// Rule (b): at least two files carry another volume of the same stem. One
	// is not enough.
	one := join(tracksWithAlbum("NYPD Red 3", 20), tracksWithAlbum("NYPD Red 4", 1))
	if r := evaluateAudiobookContent("NYPD Red 3", one); r.flagged {
		t.Errorf("one file of another volume flagged the folder: %+v", r)
	}
	two := join(tracksWithAlbum("NYPD Red 3", 20), tracksWithAlbum("NYPD Red 4", 2))
	if r := evaluateAudiobookContent("NYPD Red 3", two); !r.flagged {
		t.Errorf("two files of another volume were not flagged: %+v", r)
	}
	// A different number on a different stem is not a conflict.
	other := tracksWithAlbum("Catch 22", 30)
	if r := evaluateAudiobookContent("Fahrenheit 451", other); r.flagged {
		t.Errorf("a different book that ends in a number was flagged as a conflict: %+v", r)
	}
	// An explicit volume marker conflicts the same way.
	explicit := tracksWithAlbum("Dominion of Blades (Volume 2)", 40)
	if r := evaluateAudiobookContent("Dominion of Blades: A LitRPG Adventure (Volume 1)", explicit); !r.flagged {
		t.Errorf("another explicit volume of the same stem was not flagged: %+v", r)
	}
}

func TestEvaluateAudiobookContent_CapsTheTitleList(t *testing.T) {
	var tags []AudioTags
	tags = append(tags, tracksWithAlbum("Chase", 5)...)
	for i := 0; i < 15; i++ {
		tags = append(tags, tracksWithAlbum(fmt.Sprintf("Other Book Number %c%c", 'A'+i, 'A'+i), 3)...)
	}
	r := evaluateAudiobookContent("Chase", tags)
	if !r.flagged {
		t.Fatal("not flagged")
	}
	data := r.data("Chase", "Author", false)
	list, _ := data["mismatches"].([]map[string]any)
	if len(list) != maxContentCheckTitles {
		t.Errorf("mismatches listed %d titles, want the cap of %d", len(list), maxContentCheckTitles)
	}
	if data["moreTitles"] != 15-maxContentCheckTitles {
		t.Errorf("moreTitles = %v, want %d", data["moreTitles"], 15-maxContentCheckTitles)
	}
	if data["tracksRead"] != len(tags) || data["matches"] != 5 || data["kind"] != "audiobook" {
		t.Errorf("data = %v", data)
	}
}

// A wanted box set or omnibus has nothing to compare.
func TestBookIsCompilation(t *testing.T) {
	yes := []string{
		"Alex Cross Box Set", "The Hobbit Omnibus", "Collected Stories of Raymond Carver",
		"Dangerous Women: An Anthology", "Books 1-3", "Alex Cross Boxed Set",
	}
	for _, s := range yes {
		if !bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = false, want true", s)
		}
	}
	no := []string{"The Hobbit", "Mary, Mary: Alex Cross, Book 11", "Dune", "Chase", "The Completely Unexpected"}
	for _, s := range no {
		if bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = true, want false", s)
		}
	}
}

// TestEvaluateAudiobookContent_NegativeCorpus lists real shapes that must import
// silently: whichever way a source dresses the title, the folder is the book.
func TestEvaluateAudiobookContent_NegativeCorpus(t *testing.T) {
	rows := []struct {
		name, wanted, album string
	}{
		{"subtitle on the book", "Dune: Deluxe Edition", "Dune"},
		{"subtitle on the album", "Dune", "Dune: Deluxe Edition"},
		{"leading article", "The Hobbit", "Hobbit"},
		{"filing form album", "The Hobbit", "Hobbit, The"},
		{"ampersand", "Pride and Prejudice", "Pride & Prejudice"},
		{"unabridged tag", "The Firm", "The Firm (Unabridged)"},
		{"bracket tag", "The Firm", "The Firm [Unabridged]"},
		{"narrator edition", "The Firm", "The Firm: A Novel"},
		{"diacritics", "Les Misérables", "Les Miserables"},
		{"long series prefix on the book", "The Wheel of Time, Book 1: The Eye of the World", "The Eye of the World"},
		{"series number prefix on the book", "The Stormlight Archive 4: Rhythm of War", "Rhythm of War"},
		{"series prefix on the album", "The Colour of Magic", "Discworld 1: The Colour of Magic"},
		{"series position suffix on the album", "Mary, Mary", "Mary, Mary: Alex Cross, Book 11"},
		{"subtitle only album", "Mistborn: The Final Empire", "The Final Empire"},
		{"subtitle only album with a book suffix", "The Lord of the Rings: The Fellowship of the Ring", "The Fellowship of the Ring, Book One"},
		{"spelled number", "Slaughterhouse-Five", "Slaughterhouse 5"},
		{"hyphen and apostrophe spelling", "The Hitch-Hiker's Guide to the Galaxy", "The Hitchhiker's Guide to the Galaxy"},
		{"ordinary title with the word complete", "The Complete Maus", "The Complete Maus"},
		{"ordinary title with the words best of", "The Best of Me", "The Best of Me"},
		{"translated title", "The Lord of the Rings", "Der Herr der Ringe"},
		{"transliterated title", "Voina i mir", "Voyna i mir"},
		{"compilation of one author's works", "The Complete Sherlock Holmes", "A Study in Scarlet"},
		{"anthology", "Dangerous Women", "Dangerous Women"},
		{"anthology whose parts are named", "Dangerous Women: An Anthology", "Some Kind of Fairy Tale"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			tags := join(tracksWithAlbum(r.album, 25), []AudioTags{{Title: "Opening Credits"}, {Title: "Excerpt from Next"}, {Title: "Excerpt from Next"}})
			if got := evaluateAudiobookContent(r.wanted, tags); got.flagged {
				t.Errorf("%q wanted, album %q was flagged: %+v", r.wanted, r.album, got)
			}
		})
	}
}
