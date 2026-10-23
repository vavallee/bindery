package importer

import (
	"slices"
	"testing"
)

func TestContentTitleForms(t *testing.T) {
	cases := []struct {
		title string
		want  []string // each must be among the forms
	}{
		{"Mary, Mary: Alex Cross, Book 11", []string{"mary mary alex cross book 11", "mary mary"}},
		{"Mary, Mary", []string{"mary mary"}},
		{"Dominion of Blades: A LitRPG Adventure (Volume 1)", []string{"dominion of blades"}},
		{"The Stormlight Archive 4: Rhythm of War", []string{"rhythm of war"}},
		{"The Wheel of Time, Book 1: The Eye of the World", []string{"eye of the world"}},
		{"Foundation & Empire", []string{"foundation and empire"}},
		{"Les Misérables", []string{"les miserables"}},
		{"Racketeer, The", []string{"racketeer"}},
		{"The Racketeer", []string{"racketeer"}},
		{"Dune [Unabridged]", []string{"dune"}},
		{"Müller's Reise", []string{"mullers reise", "muellers reise"}},
	}
	for _, c := range cases {
		forms := contentTitleForms(c.title)
		for _, w := range c.want {
			if !slices.Contains(forms, w) {
				t.Errorf("contentTitleForms(%q) = %q, want it to include %q", c.title, forms, w)
			}
		}
	}
}

func TestContentTitleForms_NeverEmptyForms(t *testing.T) {
	for _, title := range []string{"", "   ", ": A Novel", "()", "..."} {
		for _, f := range contentTitleForms(title) {
			if f == "" {
				t.Errorf("contentTitleForms(%q) returned an empty form", title)
			}
		}
	}
}

func TestContentTitleSupports(t *testing.T) {
	cases := []struct {
		name         string
		wanted, file string
		reverse      bool
		want         bool
	}{
		{"equal", "The Racketeer", "The Racketeer", false, true},
		{"article and case", "The Racketeer", "racketeer", false, true},
		{"series suffix on the book", "Mary, Mary: Alex Cross, Book 11", "Mary, Mary", false, true},
		{"series suffix on the file", "Mary, Mary", "Mary, Mary: Alex Cross, Book 11", false, true},
		{"ampersand", "Foundation and Empire", "Foundation & Empire", false, true},
		{"diacritics", "Les Misérables", "Les Miserables", false, true},
		{"short title contained", "Dune", "Dune Messiah", false, true},
		{"book title inside a longer file title", "Wizard and Glass", "The Dark Tower IV: Wizard and Glass", false, true},
		{"series prefix then title", "The Dark Tower IV: Wizard and Glass", "Wizard and Glass", false, true},
		{"parenthesised volume", "Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades", false, true},
		{"different books", "The Firm", "The Runaway Jury", false, false},
		{"other volume", "NYPD Red 3", "NYPD Red 4", false, false},
		{"longer wanted is not inside shorter file", "Dune Messiah", "Dune", false, false},
		{"words must be whole", "Red", "Redemption", false, false},
		{"file inside wanted, only when asked", "The Dark Tower: Wizard and Glass: A Novel", "Wizard and Glass", false, false},
		{"file inside wanted when asked", "The Dark Tower: Wizard and Glass: A Novel", "Wizard and Glass", true, true},
		{"one word file title never rides reverse containment", "It Ends with Us", "It", true, false},
		{"empty file title supports nothing", "The Firm", "", false, false},
	}
	for _, c := range cases {
		if got := contentTitleSupports(c.wanted, c.file, c.reverse); got != c.want {
			t.Errorf("%s: contentTitleSupports(%q, %q, %v) = %v, want %v", c.name, c.wanted, c.file, c.reverse, got, c.want)
		}
	}
}

func TestGenericTrackTitle(t *testing.T) {
	yes := []string{
		"Chapter 12", "chapter 1", "Track 03", "Track 3 of 40", "Disc 2", "CD 1", "Part 4", "Section 1",
		"Chapter Twelve", "Part IV", "12", "003", "1984", "Prologue", "Epilogue", "Introduction", "Intro", "Credits",
		"Opening Credits", "End Credits", "Closing Credits", "Foreword", "Afterword", "Preface",
		"Acknowledgments", "Acknowledgements", "About the Author", "Title Page", "Copyright",
	}
	for _, s := range yes {
		if !genericTrackTitle(s) {
			t.Errorf("genericTrackTitle(%q) = false, want true", s)
		}
	}
	no := []string{"Chapter and Verse", "The 12 Chairs", "Part of Your World", "Corralled", "The Firm", ""}
	for _, s := range no {
		if genericTrackTitle(s) {
			t.Errorf("genericTrackTitle(%q) = true, want false", s)
		}
	}
}

func TestPromoTrackTitle(t *testing.T) {
	yes := []string{
		"Excerpt from Run", "Excerpt: The Next Book", "Preview of Chase", "Sample", "Bonus Interview with the Author",
		"Bonus Material", "Also by James Patterson", "Teaser", "Trailer", "Sampler", "An Interview with Patterson",
	}
	for _, s := range yes {
		if !promoTrackTitle(s) {
			t.Errorf("promoTrackTitle(%q) = false, want true", s)
		}
	}
	no := []string{"The Firm", "Chase", "Coming Home to Myself", "Examples of Grace", ""}
	for _, s := range no {
		if promoTrackTitle(s) {
			t.Errorf("promoTrackTitle(%q) = true, want false", s)
		}
	}
}

func TestOtherVolumeOfSameWork(t *testing.T) {
	cases := []struct {
		wanted, file string
		want         bool
	}{
		{"NYPD Red 3", "NYPD Red 4", true},
		{"NYPD Red 3", "NYPD Red 3", false},
		{"NYPD Red 3", "NYPD Red", false},
		{"Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades (Volume 2)", true},
		{"Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades", false},
		{"Alex Cross, Book 11", "Alex Cross, Book 12", true},
		{"Mary, Mary: Alex Cross, Book 11", "Mary, Mary: Alex Cross, Book 12", true},
		{"Mary, Mary: Alex Cross, Book 11", "Cross Fire: Alex Cross, Book 12", false},
		{"The Primal Hunter 3", "The Primal Hunter 17: A LitRPG Adventure", true},
		{"The Firm", "The Runaway Jury", false},
		{"Fahrenheit 451", "Catch 22", false},
		{"1984", "2001", false},
		{"Dune", "Dune 2", false},
		{"", "NYPD Red 4", false},
	}
	for _, c := range cases {
		if got := otherVolumeOfSameWork(c.wanted, c.file); got != c.want {
			t.Errorf("otherVolumeOfSameWork(%q, %q) = %v, want %v", c.wanted, c.file, got, c.want)
		}
	}
}
