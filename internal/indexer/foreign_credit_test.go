package indexer

import "testing"

// The reasons are spelled out rather than taken from the package constants so
// this file also compiles against the code before #3615.
const (
	reasonForeignAuthor = "release names a different author for this title"
	reasonRunsIntoOther = "release carries only the main title, and goes on into a different title"
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
	}
	for _, c := range cases {
		got := batchVerdicts(c.title, c.author, []string{c.release}, bookContext{})
		if v, ok := got[c.release]; !ok || v != "" {
			t.Errorf("%q for %q by %s: verdict %q, want kept", c.release, c.title, c.author, v)
		}
	}
}
