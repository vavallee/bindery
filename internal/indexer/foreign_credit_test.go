package indexer

import "testing"

// The reasons are spelled out rather than taken from the package constants so
// this file also compiles against the code before #3615.
const (
	reasonForeignAuthor = "release names a different author for this title"
	reasonRunsIntoOther = "release carries only the main title, and goes on into a different title by the same author"
)

// TestAutoGrabRejectsOtherBooksForUnreleasedTitles pins #3615 with the release
// names the scheduled sweep grabbed. Every wanted book was unreleased, so the
// sweep took the closest release it could find, and the relevance filter
// passed each one:
//
//   - a "by" credit naming another author was read only when the release
//     opened with the wanted title word for word, so a leading article ("The
//     Last Ride"), an ampersand ("Smoke & Mirrors") or stop words between the
//     title words ("The Mirror in the Mirror") kept it from being read;
//   - a release carrying only the main title before the colon was accepted
//     even when it went straight on into a different title by the same author
//     ("Halo: Oblivion", "Camp Half-Blood Confidential").
func TestAutoGrabRejectsOtherBooksForUnreleasedTitles(t *testing.T) {
	cases := []struct {
		title, author, release, want string
	}{
		{"Mirror Mirror", "Jim Butcher",
			"The Mirror in the Mirror: A Labyrinth by Michael Ende [ENG / EPUB]", reasonForeignAuthor},
		{"Smoke and Mirrors", "J. A. Jance",
			"Smoke & Mirrors by E A Copen [ENG / M4B]", reasonForeignAuthor},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson",
			"The Last Ride by Nick Louth [ENG / M4B] [VIP]", reasonForeignAuthor},
		{"Halo: Fireteam Noble", "Troy Denning",
			"Halo: Oblivion: A Master Chief Story by Troy Denning [ENG / M4B] [VIP]", reasonRunsIntoOther},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan",
			"Camp Half-Blood Confidential by Rick Riordan [ENG / MP3] [VIP]", reasonRunsIntoOther},
		// The same two with the credit after a dash.
		{"Halo: Fireteam Noble", "Troy Denning",
			"Halo: Oblivion: A Master Chief Story - Troy Denning [ENG / M4B]", reasonRunsIntoOther},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan",
			"Camp Half-Blood Confidential - Rick Riordan [ENG / MP3]", reasonRunsIntoOther},
	}
	for _, c := range cases {
		got := batchVerdicts(c.title, c.author, []string{c.release}, bookContext{})
		if got[c.release] != c.want {
			t.Errorf("%q for %q by %s: verdict %q, want drop %q", c.release, c.title, c.author, got[c.release], c.want)
		}
	}
}

// TestAutoGrabKeepsTheBookItself is the other side of #3615: the same wanted
// titles still accept the releases that are the book, including a "by" inside
// the title, a narrator credit, the author before the title, and a main title
// release followed by the author, the series, a number or release furniture.
func TestAutoGrabKeepsTheBookItself(t *testing.T) {
	cases := []struct {
		title, author, release string
	}{
		{"Mirror Mirror", "Jim Butcher", "Mirror Mirror by Jim Butcher [ENG / EPUB]"},
		{"Mirror Mirror", "Jim Butcher", "The Mirror Mirror by Jim Butcher EPUB"},
		{"Mirror Mirror", "Jim Butcher", "Jim Butcher - Mirror Mirror (Dresden Files 18) read by James Marsters M4B"},
		{"Mirror Mirror", "Jim Butcher", "Jim Butcher - Mirror Mirror by James Marsters M4B"},
		{"Mirror Mirror", "Jim Butcher", "Mirror Mirror - Jim Butcher (2027) EPUB"},
		{"Smoke and Mirrors", "J. A. Jance", "Smoke & Mirrors by J. A. Jance [ENG / M4B]"},
		{"Smoke and Mirrors", "J. A. Jance", "Smoke and Mirrors by Jance [ENG / M4B]"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "The Last Ride by Craig Alanson [ENG / M4B]"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Last Ride Expeditionary Force 20 Craig Alanson M4B"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo: Fireteam Noble by Troy Denning [ENG / M4B]"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo by Troy Denning [ENG / M4B]"},
		{"Halo: Fireteam Noble", "Troy Denning", "Troy Denning - Halo (2026) EPUB"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo.Troy.Denning.2026.RETAIL.EPUB"},
		{"Stand by Me", "Ben E. King", "Stand by Me by Ben E. King EPUB"},
		{"Death by Chocolate", "Sarah Graves", "Death by Chocolate by Sarah Graves EPUB"},
		{"The Final Empire", "Brandon Sanderson", "The Final Empire translated by Someone Else EPUB"},
		{"The Way of Kings", "Brandon Sanderson", "The Way of Kings, Book 1 narrated by Michael Kramer"},

		// A main title followed by words no allow list holds is still the book
		// unless a credit to the author closes the run (review of #3640).
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Audible M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Libation M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Chapterized M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project.Hail.Mary.Repack.EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Kepub"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Fixed EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Signed EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary First Edition"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Dolby Atmos"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Full Cast"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Graphic Audio M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary GraphicAudio M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Dramatized Adaptation M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Ray Porter M4B"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "Good Omens Terry Pratchett Neil Gaiman EPUB"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "Good Omens Pratchett Gaiman EPUB"},
		{"Der Schwarm: Roman", "Frank Schätzing", "Der Schwarm von Frank Schätzing"},
		{"Der Schwarm: Roman", "Frank Schätzing", "Der Schwarm Taschenbuch"},
		{"L'Étranger: Roman", "Albert Camus", "L'Étranger par Albert Camus EPUB"},
		{"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones", "James Clear", "Atomic Habits: Tiny Changes, Remarkable Results EPUB"},
		{"1984: A Novel", "George Orwell", "1984 Nineteen Eighty Four George Orwell"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Unabridged Audiobook by Troy Denning M4B"},

		// Furniture, a narrator or co author, or the numerals spelled out,
		// between the main title and a credit to the wanted author (second
		// review of #3640).
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Audible by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Audible - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Repack by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Kepub - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Chapterized by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary GraphicAudio - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Dramatized Adaptation by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Full Cast by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Signed First Edition by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project Hail Mary Ray Porter - Andy Weir"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein 1818 Text by Mary Shelley"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein Norton Critical Edition by Mary Shelley"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein Penguin Classics - Mary Shelley"},
		{"Dracula: Annotated", "Bram Stoker", "Dracula Oxford Worlds Classics by Bram Stoker"},
		{"Der Schwarm: Roman", "Frank Schätzing", "Der Schwarm Ungekürzte Lesung - Frank Schätzing"},
		{"L'Étranger: Roman", "Albert Camus", "L'Étranger Folio - Albert Camus"},
		{"Educated: A Memoir", "Tara Westover", "Educated Julia Whelan - Tara Westover"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "Good Omens Terry Pratchett - Neil Gaiman"},
		{"1984: A Novel", "George Orwell", "1984 Nineteen Eighty-Four by George Orwell"},
		{"1984: A Novel", "George Orwell", "1984 Nineteen Eighty Four - George Orwell"},
	}
	// Known limit, deliberately not asserted either way: the same book under a
	// subtitle the library does not hold, closed by a credit to the author,
	// "Atomic Habits Tiny Changes Remarkable Results by James Clear EPUB" for
	// the US subtitle. It has exactly the shape of "Camp Half-Blood
	// Confidential by Rick Riordan", and no string rule tells the two apart.
	series := bookContext{seriesTitle: "Expeditionary Force", seriesPosition: "20"}
	for _, release := range []string{
		"Last Ride ExForce 20 M4B",
		"Last Ride ExFor 20 R.C. Bray M4B",
		"Last Ride RC Bray M4B",
		"Last Ride ExForce 20 by Craig Alanson",
		"Last Ride ExForce 20 - Craig Alanson - RC Bray",
	} {
		title := "Last Ride: Expeditionary Force, Book 20"
		got := batchVerdicts(title, "Craig Alanson", []string{release}, series)
		if v, ok := got[release]; !ok || v != "" {
			t.Errorf("%q for %q: verdict %q, want kept", release, title, v)
		}
	}
	for _, c := range cases {
		got := batchVerdicts(c.title, c.author, []string{c.release}, bookContext{})
		if v, ok := got[c.release]; !ok || v != "" {
			t.Errorf("%q for %q by %s: verdict %q, want kept", c.release, c.title, c.author, v)
		}
	}
}
