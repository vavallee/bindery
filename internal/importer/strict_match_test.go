package importer

import "testing"

// The delete gate is identity, not compatibility: the file's whole title must
// equal the book's. Every row that is not equal keeps the old file.
func TestStrictTitleEqual(t *testing.T) {
	for _, tc := range []struct {
		book, file string
		want       bool
	}{
		{"Dune", "Dune", true},
		{"Dune", "DUNE", true},
		{"Mary, Mary: Alex Cross, Book 11", "Mary, Mary: Alex Cross, Book 11", true},
		{"Mary, Mary", "Mary Mary", true},
		{"Ender's Game", "Ender’s Game", true},
		{"Ender's Game", "Ender&#39;s Game", true},
		{"Sense & Sensibility", "Sense &amp; Sensibility", true},
		{"Catch-22", "Catch 22", true},
		{"The Stand", "The Stand", true},
		// Reviewer repros that must keep the old file.
		{"Dune", "Dune (Messiah)", false},
		{"Dune", "Dune [Complete Collection]", false},
		{"Dune", "Dune (Books 1-6)", false},
		{"Dune", "Dune (Box Set Books 1 to 6)", false},
		{"Dune", "Dune (Dune Chronicles Box Set)", false},
		{"Dune", "Dune (Part Two)", false},
		{"Dune", "Dune [Books 1 - 3]", false},
		{"Dune", "Dune (Dune Chronicles #1-6)", false},
		{"Dune", "Dune (Books One to Six)", false},
		{"Dune", "Dune [Omnibus]", false},
		{"Alex Cross, Book 12", "Alex Cross", false},
		{"Foundation, Book 2", "Foundation", false},
		{"Naruto, Vol. 2", "Naruto", false},
		{"Magic Tree House #5", "Magic Tree House", false},
		{"The Stand", "Stand", false},
		{"Stand", "The Stand", false},
		// Sequels, packs, subtitles and number differences.
		{"Dune", "Dune Messiah", false},
		{"Dune", "Children of Dune", false},
		{"Dune", "Dune: The Graphic Novel", false},
		{"Dune", "Dune Box Set Books 1 to 6", false},
		{"Dune", "Dune: Deluxe Edition", false},
		{"Dune", "Dune : Messiah", false},
		{"Dune", "Dune: Part Two", false},
		{"Dune", "Dune 2", false},
		{"Dune, Book 1", "Dune, Book 2", false},
		{"Mary, Mary: Alex Cross, Book 11", "Mary, Mary: Alex Cross, Book 12", false},
		{"Mary, Mary: Alex Cross, Book 11", "Mary, Mary: The Complete Collection Box Set", false},
		{"Fahrenheit 451", "Fahrenheit 452", false},
		{"Dune", "Dune & Dune Messiah", false},
		{"Dune", "Dune; Dune Messiah", false},
		{"", "", false},
		// Symbols and numbers that distinguish works.
		{"C++: The Complete Reference", "C#: The Complete Reference", false},
		{"C: The Complete Reference", "C++: The Complete Reference", false},
		{"C: The Complete Reference", "C#: The Complete Reference", false},
		{"Learning C", "Learning C#", false},
		{"F# for Fun", "F for Fun", false},
		{"Naruto", "Naruto \u2461", false},
		{"Naruto", "Naruto \u00b2", false},
		{"Rocky", "Rocky \u2161", false},
		{"Final Fantasy", "Final Fantasy \u2166", false},
		{"Alex Cross", "Alex Cross \u2461", false},
		{"Foundation", "Foundation \u00b9", false},
		{"\u0412\u043e\u0439\u043d\u0430 \u0438 \u043c\u0438\u0440", "\u0412\u043e\u0439\u043d\u0430 \u0438 \u043c\u0438\u0440 \u2161", false},
		{"Vitamin", "Vitamin \u00bd", false},
		{"Plan", "Plan \u24b7", false},
		{"Dune", "Dune \u2020", false},
		{"Dune", "Dune +", false},
		{"Dune", "Dune #2", false},
		{"Book 1", "Book1", false},
		{"1984", "19 84", false},
		{"C++", "C++", true},
		{"Windows 3.1", "Windows 3 1", true},
		{"Apollo 13", "Apollo-13", true},
		{"Mr. Mercedes", "Mr Mercedes", true},
		{"Ulysses", "Ulysses!", true},
		{"Self-Help", "Self Help", true},
		{"Sophie's Choice", "Sophies Choice", true},
		{"Tom & Jerry", "Tom &amp; Jerry", true},
	} {
		if got := strictTitleEqual(tc.book, tc.file); got != tc.want {
			t.Errorf("strictTitleEqual(%q, %q) = %v, want %v", tc.book, tc.file, got, tc.want)
		}
	}
}

func TestStrictAuthorMatch(t *testing.T) {
	for _, tc := range []struct {
		names   []string
		creator string
		want    bool
	}{
		{[]string{"James Patterson"}, "James Patterson", true},
		{[]string{"James Patterson"}, "Patterson, James", true},
		{[]string{"James Patterson"}, "J. Patterson", true},
		{[]string{"James Patterson"}, "James Patterson and Maxine Paetro", true},
		{[]string{"James Patterson"}, "Patterson, James; Paetro, Maxine", true},
		{[]string{"J. R. R. Tolkien"}, "Tolkien, J. R. R.", true},
		{[]string{"J. R. R. Tolkien"}, "J.R.R. Tolkien", true},
		{[]string{"Ursula K. Le Guin"}, "Le Guin, Ursula K.", true},
		{[]string{"Ursula K. Le Guin"}, "Ursula K. Le Guin", true},
		{[]string{"James Patterson"}, "Patterson, James, 1947-", true},
		{[]string{"James Patterson"}, "James Patterson Jr.", true},
		{[]string{"Martin Luther King Jr."}, "Martin Luther King, Jr.", true},
		{[]string{"Richard Bachman", "Stephen King"}, "Stephen King", true},
		// Never a match.
		{[]string{"James Patterson"}, "Patterson", false},
		{[]string{"James Patterson"}, "Maxine Paetro", false},
		{[]string{"James Patterson"}, "James Pattersen", false},
		{[]string{"James Patterson"}, "James Smith", false},
		{[]string{"Frank Herbert"}, "Brian Herbert", false},
		{[]string{"Homer"}, "Homer", false},
		{[]string{"Various"}, "Various", false},
		{[]string{"Various Authors"}, "Various Authors", false},
		{[]string{"Various Various"}, "Various Various", false},
		{[]string{"Unknown Author"}, "Unknown Author", false},
		{[]string{"Anonymous"}, "Anonymous", false},
		{[]string{"James Patterson"}, "Unknown", false},
		{[]string{"James Patterson"}, "", false},
	} {
		if got := strictAuthorMatch(tc.names, tc.creator); got != tc.want {
			t.Errorf("strictAuthorMatch(%v, %q) = %v, want %v", tc.names, tc.creator, got, tc.want)
		}
	}
}
