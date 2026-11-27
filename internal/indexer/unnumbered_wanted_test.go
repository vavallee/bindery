package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// A wanted title that numbers the book itself, with no stored position, names
// its own volume: a later number after it is another book (#4173).
func TestFilterRelevant_TitleNamesItsVolumeRejectsLaterVolume(t *testing.T) {
	cases := []struct{ title, author, release string }{
		{"Corruption Wielder: Book 1", "Aaron Shih", "Aaron Shih - Corruption Wielder 04 (epub)"},
		{"The Anime Trope System: Stone vs. Viper, Book 2", "Alvin Atwater", "Alvin Atwater - The Anime Trope System- Stone vs. Viper 15 (epub)"},
	}
	for _, c := range cases {
		t.Run(c.release, func(t *testing.T) {
			got, _ := filterRelevantWithContext(toResults(c.release), c.title, c.author, nil, bookContext{})
			if len(got) != 0 {
				t.Errorf("kept %q for %q", c.release, c.title)
			}
		})
	}
}

func TestFilterRelevant_TitleNamesItsVolumeKeepsItsOwn(t *testing.T) {
	cases := []struct{ title, author, release string }{
		{"Corruption Wielder: Book 1", "Aaron Shih", "Aaron Shih - Corruption Wielder 01 (epub)"},
		{"Corruption Wielder: Book 1", "Aaron Shih", "Aaron Shih - Corruption Wielder (epub)"},
		{"Corruption Wielder: Book 1", "Aaron Shih", "Aaron Shih - Corruption Wielder - Book 1 (epub)"},
		{"Corruption Wielder: Book 1", "Aaron Shih", "Aaron Shih - Corruption Wielder 2023 (epub)"},
		{"The Anime Trope System: Stone vs. Viper, Book 2", "Alvin Atwater", "Alvin Atwater - The Anime Trope System- Stone vs. Viper 2 (epub)"},
		{"The Anime Trope System: Stone vs. Viper, Book 2", "Alvin Atwater", "Alvin Atwater - The Anime Trope System- Stone vs. Viper (epub)"},
		{"The Anime Trope System: Stone vs. Viper, Book 2", "Alvin Atwater", "Alvin Atwater - The Anime Trope System- Stone vs. Viper, Book 2 (epub)"},
		{"The Anime Trope System: Stone vs. Viper, Book 2", "Alvin Atwater", "Alvin Atwater - The Anime Trope System- Stone vs. Viper 320kbps (epub)"},
	}
	for _, c := range cases {
		t.Run(c.release, func(t *testing.T) {
			got, reasons := filterRelevantWithContext(toResults(c.release), c.title, c.author, nil, bookContext{})
			if len(got) != 1 {
				t.Errorf("dropped %q for %q: %v", c.release, c.title, reasons)
			}
		})
	}
}

// A plain title with no stored position may be any volume, so the filter keeps
// a numbered release; the rank puts it behind an unnumbered or "1" one.
func TestRank_UnnumberedWantedPrefersUnnumberedRelease(t *testing.T) {
	cases := []struct{ title, author, later, first string }{
		{"Darling of Fate", "Sean Dunning", "Sean Dunning - Darling of Fate 02 (epub)", "Sean Dunning - Darling of Fate (epub)"},
		{"Darling of Fate", "Sean Dunning", "Sean Dunning - Darling of Fate 02 (epub)", "Sean Dunning - Darling of Fate 01 (epub)"},
		{"Dungeon Tour Guide", "Aaron Shih", "Dungeon Tour Guide 2 by Aaron Shih EPUB", "Dungeon Tour Guide by Aaron Shih EPUB"},
		{"Dungeon Tour Guide", "Aaron Shih", "Dungeon Tour Guide Book Four by Aaron Shih EPUB", "Dungeon Tour Guide Book 1 by Aaron Shih EPUB"},
	}
	for _, c := range cases {
		t.Run(c.later, func(t *testing.T) {
			crit := MatchCriteria{Title: c.title, Author: c.author, MediaType: models.MediaTypeEbook}
			kept, _ := filterRelevantWithContext(toResults(c.later), c.title, c.author, nil, crit.context())
			if len(kept) != 1 {
				t.Fatalf("the filter must keep %q alone: the book may really be that volume", c.later)
			}
			// The later volume has more grabs, which must not outweigh it.
			rs := []newznab.SearchResult{{Title: c.later, Grabs: 100}, {Title: c.first}}
			rankResults(rs, crit)
			if rs[0].Title != c.first {
				t.Errorf("ranked %q first, want %q", rs[0].Title, c.first)
			}
		})
	}
}

func TestNamesLaterVolume_KeepsOrdinaryReleases(t *testing.T) {
	cases := []struct {
		title, release string
		pos            string
		want           bool
	}{
		{"Fahrenheit 451", "Ray Bradbury - Fahrenheit 451 (epub)", "", false},
		{"Fahrenheit 451", "Ray Bradbury - Fahrenheit 451 02 (epub)", "", true},
		{"1984", "George Orwell - 1984 (epub)", "", false},
		{"Catch-22", "Joseph Heller - Catch-22 (epub)", "", false},
		{"11/22/63", "Stephen King - 11/22/63 (epub)", "", false},
		{"Project Hail Mary", "Andy Weir - Project Hail Mary (epub)", "", false},
		{"Ocean's Eleven", "Ocean's Eleven (epub)", "", false},
		{"The 7 1/2 Deaths of Evelyn Hardcastle", "Stuart Turton - The 7 1/2 Deaths of Evelyn Hardcastle (epub)", "", false},
		{"Fourth Wing", "Rebecca Yarros - Fourth Wing (epub)", "", false},
		{"The Book of Three", "Lloyd Alexander - The Book of Three (epub)", "", false},
		{"Henry V", "William Shakespeare - Henry V (epub)", "", false},
		{"Title", "Author - Title 1 (epub)", "", false},
		{"Title", "Author - Title 01 (epub)", "", false},
		{"Title", "Author - Title Book 1 (epub)", "", false},
		{"Title", "Author - Title (epub)", "", false},
		{"Title", "Author - Title 2023 (epub)", "", false},
		{"Title", "Author - Title (epub 3)", "", false},
		{"Title", "Author - Title epub 3", "", false},
		{"Title", "Author - Title 320kbps (epub)", "", false},
		{"Title", "Author - Title 2 CD (mp3)", "", false},
		{"Title", "Author - Title Part 2 of 2 (epub)", "", false},
		{"Title", "Author - Title 24 bit flac", "", false},
		// A stored position is the filter's business, not the rank's.
		{"Title", "Author - Title 02 (epub)", "3", false},
		// A title that numbers itself is the filter's business too.
		{"Title: Book 2", "Author - Title 02 (epub)", "", false},
		{"Title", "Author - Title 02 (epub)", "", true},
		{"Title", "Author - Title Book Four (epub)", "", true},
		{"Title", "Author - Title Vol. 4 (epub)", "", true},
	}
	for _, c := range cases {
		t.Run(c.title+"/"+c.release+"/"+c.pos, func(t *testing.T) {
			got := namesLaterVolume(c.release, MatchCriteria{Title: c.title, SeriesPosition: c.pos})
			if got != c.want {
				t.Errorf("namesLaterVolume = %v, want %v", got, c.want)
			}
		})
	}
}

// Box sets and anthologies keep the verdict the existing rules give them.
func TestNamesLaterVolume_SetsAreNotPenalised(t *testing.T) {
	for _, c := range []struct{ title, release string }{
		{"Series Box Set", "Author - Series Box Set Books 1-3 (epub)"},
		{"Series Trilogy", "Author - Series Trilogy 2 (epub)"},
	} {
		if namesLaterVolume(c.release, MatchCriteria{Title: c.title}) {
			t.Errorf("penalised %q for set %q", c.release, c.title)
		}
	}
}
