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
		// "Main Other Title - Author", the commonest way a same author
		// sequel is named (third review of #3640).
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Silent Storm - Troy Denning", reasonRunsIntoOther},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Silent Storm - Troy Denning M4B", reasonRunsIntoOther},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Silent Storm - Troy Denning [ENG / M4B]", reasonRunsIntoOther},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo.Silent.Storm-Troy.Denning", reasonRunsIntoOther},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Shadows Of Reach - Troy Denning", reasonRunsIntoOther},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo Oblivion Master Chief - Troy Denning", reasonRunsIntoOther},
		{"Halo: The Fall of Reach", "Eric Nylund", "Halo First Strike - Eric Nylund", reasonRunsIntoOther},
		{"Halo: The Fall of Reach", "Eric Nylund", "Halo Ghosts Of Onyx - Eric Nylund", reasonRunsIntoOther},
		{"Mistborn: The Final Empire", "Brandon Sanderson", "Mistborn Secret History - Brandon Sanderson", reasonRunsIntoOther},
		{"Mistborn: The Final Empire", "Brandon Sanderson", "Mistborn The Hero Of Ages - Brandon Sanderson", reasonRunsIntoOther},
		{"Red Rising: Golden Son", "Pierce Brown", "Red Rising Morning Star - Pierce Brown", reasonRunsIntoOther},
		{"Red Rising: Golden Son", "Pierce Brown", "Red Rising Iron Gold - Pierce Brown", reasonRunsIntoOther},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan",
			"Camp Half-Blood Confidential Demigod Files - Rick Riordan", reasonRunsIntoOther},
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

		// Furniture, a series abbreviation or the numerals spelled out,
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
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein 1818 Text by Mary Shelley"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein Norton Critical Edition by Mary Shelley"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "Frankenstein Penguin Classics - Mary Shelley"},
		{"Dracula: Annotated", "Bram Stoker", "Dracula Oxford Worlds Classics by Bram Stoker"},
		{"Der Schwarm: Roman", "Frank Schätzing", "Der Schwarm Ungekürzte Lesung - Frank Schätzing"},
		{"L'Étranger: Roman", "Albert Camus", "L'Étranger Folio - Albert Camus"},
		{"1984: A Novel", "George Orwell", "1984 Nineteen Eighty-Four by George Orwell"},
		{"1984: A Novel", "George Orwell", "1984 Nineteen Eighty Four - George Orwell"},
	}
	// Known limits, deliberately not asserted either way. Automatic search
	// drops these; they stay visible and grabbable in interactive search.
	//   - The same book under a subtitle the library does not hold, closed by
	//     a credit to the author: "Atomic Habits Tiny Changes Remarkable
	//     Results by James Clear EPUB" for the US subtitle. It has exactly the
	//     shape of "Camp Half-Blood Confidential by Rick Riordan".
	//   - A narrator or co author before "- <author>" with no role word:
	//     "Project Hail Mary Ray Porter - Andy Weir", "Educated Julia Whelan -
	//     Tara Westover", "Good Omens Terry Pratchett - Neil Gaiman". A name
	//     has the same shape as "Halo Silent Storm - Troy Denning", which is
	//     another book.
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

// corpusCase is one release judged against one wanted book, with the library
// context the scheduler would supply.
type corpusCase struct {
	title, author, series, pos, release string
}

func corpusVerdict(c corpusCase) (string, bool) {
	got := batchVerdicts(c.title, c.author, []string{c.release}, bookContext{seriesTitle: c.series, seriesPosition: c.pos})
	v, ok := got[c.release]
	return v, ok
}

// TestReviewCorpusKeeps holds the releases from the review rounds of #3640
// that are the wanted book and that the relevance filter keeps, so a later
// change to the run on rule or the credit reading cannot drop one silently.
// Releases the filter already drops for reasons outside #3615 (a one word main
// title with no author, for example) are not listed, and neither are other
// books the filter keeps: those are the known limits listed at
// TestReviewCorpusRejects.
func TestReviewCorpusKeeps(t *testing.T) {
	cases := []corpusCase{
		// A "by" credit to someone else is not read as a conflict when the
		// release names the wanted author too (fifth review of #3640).
		{"Lord of the Rings", "J.R.R. Tolkien", "", "", "The Lord of the Rings J.R.R. Tolkien by Phil Dragash"},
		{"The Lord of the Rings", "J.R.R. Tolkien", "", "", "The Lord of the Rings J.R.R. Tolkien by Phil Dragash"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice Jane Austen by Rosamund Pike"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice (Jane Austen) by Rosamund Pike"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride and Prejudice Jane Austen by Rosamund Pike"},
		// A role word before a contributor's name with no "by".
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated Ray Porter - Andy Weir"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Translated Hazel E. Barnes-Jean-Paul Sartre"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens A Brief History of Humankind EPUB"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens by Yuval Noah Harari EPUB"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens.Yuval.Noah.Harari.2015.RETAIL.EPUB"},
		{"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones", "James Clear", "", "", "Atomic Habits James Clear EPUB"},
		{"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones", "James Clear", "", "", "Atomic Habits Unabridged M4B"},
		{"The Body: A Guide for Occupants", "Bill Bryson", "", "", "The Body Bill Bryson EPUB"},
		{"The Body: A Guide for Occupants", "Bill Bryson", "", "", "The Body: A Guide For Occupants by Bill Bryson"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Unabridged M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Retail EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary A Novel EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Read by Ray Porter M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Narrated by Ray Porter M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Andy Weir 2021"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Weir Andy EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Movie Tie-In Edition EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Deluxe EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Large Print"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Unabridged Audiobook"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Complete M4B"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary German EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Hörbuch"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Collectors Edition"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary US Edition"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary UK Edition"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Ebook"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Repack"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Novel EPUB"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Mobi Azw3"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary MP3 64kbps"},
		{"Educated: A Memoir", "Tara Westover", "", "", "Educated Tara Westover EPUB"},
		{"Outliers: The Story of Success", "Malcolm Gladwell", "", "", "Outliers Malcolm Gladwell"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "", "", "Good Omens by Terry Pratchett and Neil Gaiman EPUB"},
		{"Good Omens", "Neil Gaiman", "", "", "The Good Omens by Terry Pratchett & Neil Gaiman"},
		{"The Talisman", "Peter Straub", "", "", "Talisman by Stephen King & Peter Straub"},
		{"The Talisman", "Peter Straub", "", "", "Talisman by Stephen King and Peter Straub"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo", "", "Halo Book 33 Troy Denning M4B"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo", "", "Halo 33 Troy Denning M4B"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Fireteam Noble EPUB"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Denning Troy EPUB"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "", "Last Ride ExForce 20 M4B"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "", "Last Ride ExFor 20 R.C. Bray M4B"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "", "Last Ride RC Bray M4B"},
		{"Dune: Deluxe Edition", "Frank Herbert", "Dune", "", "Dune Frank Herbert EPUB"},
		{"Frankenstein: Or, The Modern Prometheus", "Mary Shelley", "", "", "Frankenstein Mary Wollstonecraft Shelley EPUB"},
		{"Fahrenheit 451: A Novel", "Ray Bradbury", "", "", "Fahrenheit 451 Ray Bradbury EPUB"},
		{"Fahrenheit 451: A Novel", "Ray Bradbury", "", "", "Fahrenheit 451 60th Anniversary Edition"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Frank Schätzing EPUB"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Frank Schaetzing EPUB"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Hörbuch Ungekürzt"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Ungekuerzt MP3"},
		{"L'Étranger: Roman", "Albert Camus", "", "", "L'Étranger Albert Camus EPUB"},
		{"Cien años de soledad: Novela", "Gabriel García Márquez", "", "", "Cien años de soledad Gabriel Garcia Marquez EPUB"},
		{"Last Ride", "Craig Alanson", "", "", "The Last Ride by Craig Alanson"},
		{"Smoke and Mirrors", "J. A. Jance", "", "", "Smoke & Mirrors by JA Jance"},
		{"Smoke and Mirrors", "J. A. Jance", "", "", "Smoke & Mirrors by Judith A. Jance"},
		{"Smoke and Mirrors", "J. A. Jance", "", "", "Smoke & Mirrors by Jance, J.A."},
		{"Smoke and Mirrors", "J. A. Jance", "", "", "Smoke & Mirrors Ali Reynolds 20 by J.A. Jance"},
		{"Smoke and Mirrors", "J. A. Jance", "", "", "Smoke & Mirrors (Ali Reynolds 20) by J A Jance"},
		{"Wizard of Earthsea", "Ursula K. Le Guin", "", "", "A Wizard of Earthsea by Ursula K. Le Guin"},
		{"Wizard of Earthsea", "Ursula K. Le Guin", "", "", "A Wizard of Earthsea by Ursula Le Guin"},
		{"Wizard of Earthsea", "Ursula K. Le Guin", "", "", "A Wizard of Earthsea by Le Guin"},
		{"Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit by J.R.R. Tolkien"},
		{"Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit by JRR Tolkien"},
		{"Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit by J. R. R. Tolkien"},
		{"Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit by Tolkien"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "Tolkien, J.R.R. - The Hobbit by Andy Serkis"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "Middle-earth 0 - The Hobbit by J.R.R. Tolkien"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit (Illustrated by Alan Lee) by J.R.R. Tolkien"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit Foreword by Christopher Tolkien"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "The Hobbit Edited by Christopher Tolkien"},
		{"Lord of the Rings", "J.R.R. Tolkien", "", "", "The Lord of the Rings by J.R.R. Tolkien"},
		{"Martian", "Andy Weir", "", "", "The Martian by Andy Weir Read by R.C. Bray"},
		{"Stand by Me", "Stephen King", "", "", "The Stand by Me by Stephen King"},
		{"The Body", "Stephen King", "", "", "Stand by Me (The Body) by Stephen King"},
		{"Death by Black Hole", "Neil deGrasse Tyson", "", "", "Death by Black Hole by Neil deGrasse Tyson"},
		{"Death by Black Hole", "Neil deGrasse Tyson", "", "", "Death by Black Hole by Neil Degrasse Tyson"},
		{"Death by Black Hole: And Other Cosmic Quandaries", "Neil deGrasse Tyson", "", "", "Death by Black Hole by Neil deGrasse Tyson"},
		{"Killing Floor", "Lee Child", "", "", "Jack Reacher 01 - Killing Floor by Lee Child"},
		{"Killing Floor", "Lee Child", "", "", "The Killing Floor by Lee Child"},
		{"Us", "David Nicholls", "", "", "Us by David Nicholls"},
		{"Emma", "Jane Austen", "", "", "Emma by Jane Austen"},
		{"Emma", "Jane Austen", "", "", "The Emma by Jane Austen"},
		{"Dune", "Frank Herbert", "", "", "The Dune by Frank Herbert"},
		{"Dune", "Frank Herbert", "", "", "Frank Herbert Dune by Scott Brick"},
		{"Dune", "Frank Herbert", "", "", "Herbert, Frank - Dune by Scott Brick"},
		{"Dune", "Frank Herbert", "", "", "Dune Chronicles 1 Dune by Frank Herbert"},
		{"Fahrenheit 451", "Ray Bradbury", "", "", "Fahrenheit 451 by Ray Bradbury"},
		{"1984", "George Orwell", "", "", "1984 by George Orwell"},
		{"11/22/63", "Stephen King", "", "", "11-22-63 by Stephen King"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice by Jane Austen"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice by Austen"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice by Austen Jane"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Pride & Prejudice by Jane Austen, Annotated by David Shapard"},
		{"Pride and Prejudice", "Jane Austen", "", "", "Jane Austen Complete Works: Pride & Prejudice by Jane Austen"},
		{"Girl with the Dragon Tattoo", "Stieg Larsson", "", "", "The Girl with the Dragon Tattoo by Stieg Larsson, Reg Keeland (Translator)"},
		{"Verloren", "Max Mustermann", "", "", "Der Verloren von Max Mustermann"},
		{"Hound of the Baskervilles", "Arthur Conan Doyle", "", "", "The Hound of the Baskervilles by Sir Arthur Conan Doyle"},
		{"Hound of the Baskervilles", "Arthur Conan Doyle", "", "", "The Hound of the Baskervilles by Doyle, Arthur Conan"},
		{"Best American Short Stories 2020", "Curtis Sittenfeld", "", "", "The Best American Short Stories 2020 by Curtis Sittenfeld, Heidi Pitlor"},
		{"Dangerous Women", "George R.R. Martin", "", "", "The Dangerous Women by George R. R. Martin & Gardner Dozois"},
		{"Dangerous Women", "George R.R. Martin", "", "", "The Dangerous Women by Gardner Dozois and George R.R. Martin"},
		{"Cuckoo's Calling", "Robert Galbraith", "", "", "The Cuckoo's Calling by Robert Galbraith (J.K. Rowling)"},
		{"Wheel of Time", "Robert Jordan", "", "", "The Wheel of Time by Robert Jordan & Brandon Sanderson"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "Hobbit by J.R.R. Tolkien"},
		{"The Hobbit", "J.R.R. Tolkien", "", "", "Hobbit by Tolkien"},
		{"The Martian", "Andy Weir", "", "", "Martian by Andy Weir"},
		{"The Lord of the Rings", "J.R.R. Tolkien", "", "", "Lord of the Rings by Tolkien JRR"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.2021.KEPUB-GRP"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary [Audible] M4B"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens Harari Yuval Noah AZW3"},
		{"Thinking in Systems: A Primer", "Donella Meadows", "", "", "Thinking in Systems Donella H Meadows"},
		{"Thinking in Systems: A Primer", "Donella H. Meadows", "", "", "Thinking in Systems Meadows Donella"},
		{"Born a Crime: Stories from a South African Childhood", "Trevor Noah", "", "", "Born a Crime Trevor Noah Audible"},
		{"Born a Crime: Stories from a South African Childhood", "Trevor Noah", "", "", "Born.a.Crime.Trevor.Noah.Retail.Audible.M4B"},
		{"Jack Reacher: Killing Floor", "Lee Child", "", "", "Jack Reacher Book Twenty One by Lee Child"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo FireNoble by Troy Denning"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated Ray Porter - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Recorded Books - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Brilliance Audio - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Macmillan Audio by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Ungekürzt - Andy Weir"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Ungekürzt by Frank Schätzing"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Gekürzte Lesung - Frank Schätzing"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Hörspiel - Frank Schätzing"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride Ex Force 20 by Craig Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride ExForce 20 by Craig Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "", "", "Last Ride ExForce 20 by Craig Alanson"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Retail-GROUP"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Retail.EPUB-BitBook"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.2021-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Unabridged-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Re-Read - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Re-Read-Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary EN-Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Andy-Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Read-by-Ray-Porter-Andy-Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project-Hail-Mary-Andy-Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.WEB-DL-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Movie-Tie-In-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Sci-Fi-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Sci-Fi Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Science.Fiction-Andy.Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Hugo.Nominee-Andy.Weir"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Jean-Paul Sartre EPUB"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being.and.Nothingness-Jean-Paul.Sartre"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness - Jean-Paul Sartre"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Hazel Barnes Jean-Paul Sartre"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Routledge Classics - Jean-Paul Sartre"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Sartre Jean-Paul"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Translated Hazel E. Barnes-Jean-Paul Sartre"},
		{"We Were Eight Years in Power: An American Tragedy", "Ta-Nehisi Coates", "", "", "We Were Eight Years in Power Ta-Nehisi Coates"},
		{"We Were Eight Years in Power: An American Tragedy", "Ta-Nehisi Coates", "", "", "We.Were.Eight.Years.in.Power-Ta-Nehisi.Coates"},
		{"We Were Eight Years in Power: An American Tragedy", "Ta-Nehisi Coates", "", "", "We Were Eight Years in Power Coates Ta-Nehisi"},
		{"We Were Eight Years in Power: An American Tragedy", "Ta-Nehisi Coates", "", "", "We Were Eight Years in Power Unabridged-Ta-Nehisi Coates"},
		{"Some Book: A Novel", "Mary-Jo Smith", "", "", "Some Book Smith Mary-Jo"},
		{"Some Book: A Novel", "Mary-Jo Smith", "", "", "Some Book Mary-Jo Smith EPUB"},
		{"Some Book: A Novel", "Mary-Jo Smith", "", "", "Some.Book-Mary-Jo.Smith"},
		{"Educated: A Memoir", "Tara Westover", "", "", "Educated Kobna Holdbrook-Smith Tara Westover"},
		{"Educated: A Memoir", "Tara Westover", "", "", "Educated.Unabridged-Tara.Westover"},
		{"Educated: A Memoir", "Tara Westover", "", "", "Educated Westover-Tara"},
		{"Harry Potter: Half-Blood Prince", "J.K. Rowling", "", "", "Harry Potter Half-Blood Prince - J.K. Rowling"},
		{"Harry Potter: Half-Blood Prince", "J.K. Rowling", "", "", "Harry.Potter.Half-Blood.Prince-J.K.Rowling"},
		{"Harry Potter: Half-Blood Prince", "J.K. Rowling", "", "", "Harry.Potter.Half.Blood.Prince-JK.Rowling"},
		{"Harry Potter: Half-Blood Prince", "J.K. Rowling", "", "", "Harry Potter-J.K. Rowling"},
		{"Harry Potter: Half-Blood Prince", "J.K. Rowling", "", "", "Harry.Potter.Box-Set-J.K.Rowling"},
		{"Spider-Man: Far From Home", "Steve Behling", "", "", "Spider-Man-Steve Behling"},
		{"Spider-Man: Far From Home", "Steve Behling", "", "", "Spider-Man.Far.From.Home-Steve.Behling"},
		{"Spider-Man: Far From Home", "Steve Behling", "", "", "Spider-Man Junior Novel - Steve Behling"},
		{"Spider-Man: Far From Home", "Steve Behling", "", "", "Spider-Man.Junior.Novel-Steve.Behling"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo.Fireteam.Noble-Troy.Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo-Troy.Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo.Retail-Troy.Denning"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last.Ride.ExForce.20-Craig.Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride ExFor 20 by Craig Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride EF 20 by Craig Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride Force 20 by Craig Alanson"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride ExpForce 20 by Craig Alanson"},
		{"Death by Black Hole: And Other Cosmic Quandaries", "Neil deGrasse Tyson", "", "", "Death by Black Hole - Neil deGrasse Tyson EPUB"},
		{"Death by Black Hole: And Other Cosmic Quandaries", "Neil deGrasse Tyson", "", "", "Death by Black Hole Unabridged by Neil deGrasse Tyson"},
		{"Mistborn: The Well of Ascension", "Brandon Sanderson", "Mistborn", "2", "Mistborn Book Two by Brandon Sanderson"},
		{"Mistborn: The Well of Ascension", "Brandon Sanderson", "Mistborn", "2", "Mistborn Book 2 by Brandon Sanderson"},
		{"Mistborn: The Final Empire", "Brandon Sanderson", "Mistborn", "1", "Mistborn Book One by Brandon Sanderson"},
		{"Mistborn: The Well of Ascension", "Brandon Sanderson", "", "", "Mistborn Book Two by Brandon Sanderson"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Unabridged by Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Collector's Edition - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Collectors Edition - Andy Weir"},
		{"Berserk: Volume 2", "Kentaro Miura", "Berserk", "2", "Berserk Volume 2 by Kentaro Miura"},
		{"Berserk: Volume 2", "Kentaro Miura", "", "", "Berserk Volume 2 by Kentaro Miura"},
		{"Berserk: Volume 2", "Kentaro Miura", "", "", "Berserk Vol 2 by Kentaro Miura"},
		{"Berserk: Volume 2", "Kentaro Miura", "", "", "Berserk Vol. 02 - Kentaro Miura"},
		{"Berserk: Volume 2", "Kentaro Miura", "", "", "Berserk Deluxe Edition Volume 2 by Kentaro Miura"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Fireteam Noble by Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "Halo", "", "Halo Book 33 by Troy Denning"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary Unabridged by Andy Weir read by Ray Porter"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project.Hail.Mary.Retail-Andy.Weir"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "", "", "Good Omens by Terry Pratchett and Neil Gaiman"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Neil Gaiman", "", "", "Good Omens Terry Pratchett & Neil Gaiman"},
		{"Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch", "Terry Pratchett", "", "", "Good Omens by Neil Gaiman and Terry Pratchett"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Taschenbuch von Frank Schätzing"},
		{"Der Schwarm: Roman", "Frank Schätzing", "", "", "Der Schwarm Hörbuch by Frank Schätzing"},
		{"Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones", "James Clear", "", "", "Atomic Habits - Tiny Changes, Remarkable Results - James Clear"},
		{"Last Ride: Expeditionary Force, Book 20", "Craig Alanson", "Expeditionary Force", "20", "Last Ride ExForce 20 - Craig Alanson - RC Bray"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens Illustrated Edition by Yuval Noah Harari"},
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens Harari - Yuval Noah Harari"},
		{"Educated: A Memoir", "Tara Westover", "", "", "Educated Memoir by Tara Westover"},
		{"Dune: Deluxe Edition", "Frank Herbert", "Dune", "1", "Dune 60th Anniversary Edition by Frank Herbert"},
		{"The Body: A Guide for Occupants", "Bill Bryson", "", "", "The Body Bryson Unabridged by Bill Bryson"},
		{"Thinking in Systems: A Primer", "Donella H. Meadows", "", "", "Thinking in Systems Meadows - Donella H. Meadows"},
		// Several narrators, or a name of three words, after a role word
		// (sixth review of #3640).
		{"The Way of Kings: The Stormlight Archive, Book 1", "Brandon Sanderson", "", "", "The Way of Kings narrated by Kate Reading and Michael Kramer - Brandon Sanderson"},
		{"The Way of Kings: The Stormlight Archive, Book 1", "Brandon Sanderson", "", "", "The Way of Kings narrated by Michael Kramer, Kate Reading - Brandon Sanderson"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated by Ray Porter and Jane Doe - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated by Ray Porter, Jane Doe - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated by Mary Beth Hurt - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated Mary Beth Hurt - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary narrated by Mary Jo Smith Jones - Andy Weir"},
		{"Project Hail Mary: A Novel", "Andy Weir", "", "", "Project Hail Mary translated by Hans Peter Mueller - Andy Weir"},
	}
	for _, c := range cases {
		if v, ok := corpusVerdict(c); !ok || v != "" {
			t.Errorf("%q for %q by %s: verdict %q, want kept", c.release, c.title, c.author, v)
		}
	}
}

// TestReviewCorpusRejects holds every release from the review rounds of #3640
// that is another book (a same author sequel, another title before the
// author, an abbreviation that is really a word) and must stay dropped.
//
// Known limits, deliberately not asserted either way. Automatic search drops
// these although each is, or may be, the wanted book; they stay visible and
// grabbable in interactive search:
//   - Atomic Habits Tiny Changes Remarkable Results by James Clear EPUB  (Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones, James Clear)
//   - The Good Omens by Terry Pratchett  (Good Omens, Neil Gaiman)
//   - Smoke & Mirrors by Karen Ziemba M4B  (Smoke and Mirrors, J. A. Jance)
//   - A Wizard of Earthsea by Ursula K LeGuin  (Wizard of Earthsea, Ursula K. Le Guin)
//   - A Wizard of Earthsea by Rob Inglis  (Wizard of Earthsea, Ursula K. Le Guin)
//   - A Wizard of Earthsea by Kobna Holdbrook-Smith  (Wizard of Earthsea, Ursula K. Le Guin)
//   - The Lord of the Rings by Phil Dragash  (Lord of the Rings, J.R.R. Tolkien)
//   - The Lord of the Rings Complete by Rob Inglis  (Lord of the Rings, J.R.R. Tolkien)
//   - Pride and Prejudice by Rosamund Pike  (Pride & Prejudice, Jane Austen)
//   - Pride & Prejudice by Rosamund Pike  (Pride and Prejudice, Jane Austen)
//   - Pride & Prejudice by Penguin Classics  (Pride and Prejudice, Jane Austen)
//   - Pride & Prejudice by Wordsworth Editions  (Pride and Prejudice, Jane Austen)
//   - The Old Man and the Sea by Donald Sutherland  (Old Man and the Sea, Ernest Hemingway)
//   - The Girl with the Dragon Tattoo by Simon Vance  (Girl with the Dragon Tattoo, Stieg Larsson)
//   - The Hound of the Baskervilles by A. Conan Doyle  (Hound of the Baskervilles, Arthur Conan Doyle)
//   - The Hound of the Baskervilles by Conan Doyle  (Hound of the Baskervilles, Arthur Conan Doyle)
//   - The Best American Short Stories 2020 by Heidi Pitlor  (Best American Short Stories 2020, Curtis Sittenfeld)
//   - The Cuckoo's Calling by J.K. Rowling  (Cuckoo's Calling, Robert Galbraith)
//   - The Wheel of Time by Kate Reading and Michael Kramer  (Wheel of Time, Robert Jordan)
//   - Hound of the Baskervilles by Conan Doyle  (The Hound of the Baskervilles, Arthur Conan Doyle)
//   - Project Hail Mary Ray Porter by Andy Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Project Hail Mary ray porter - andy weir  (Project Hail Mary: A Novel, Andy Weir)
//   - project.hail.mary.ray.porter-andy.weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Project Hail Mary R. C. Bray Ray Porter - Andy Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Project Hail Mary Ray Porter, Jane Doe - Andy Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Project Hail Mary Audiobook Unabridged Ray Porter Narrator - Andy Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Educated julia whelan - Tara Westover  (Educated: A Memoir, Tara Westover)
//   - Educated Penguin Random House by Tara Westover  (Educated: A Memoir, Tara Westover)
//   - Last Ride EF20 by Craig Alanson  (Last Ride: Expeditionary Force, Book 20, Craig Alanson)
//   - Project.Hail.Mary.Ray.Porter-Andy.Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Educated.Julia.Whelan-Tara.Westover  (Educated: A Memoir, Tara Westover)
//   - Project Hail Mary Ray Porter - Andy Weir  (Project Hail Mary: A Novel, Andy Weir)
//   - Good Omens Terry Pratchett - Neil Gaiman  (Good Omens: The Nice and Accurate Prophecies of Agnes Nutter, Witch, Neil Gaiman)
//   - Atomic Habits Tiny Changes Remarkable Results - James Clear  (Atomic Habits: An Easy & Proven Way to Build Good Habits & Break Bad Ones, James Clear)
//   - Educated Julia Whelan - Tara Westover  (Educated: A Memoir, Tara Westover)
//   - Dune Chronicles by Frank Herbert  (Dune: Deluxe Edition, Frank Herbert)
//   - Frankenstein Annotated for Scientists by Mary Shelley  (Frankenstein: Or, The Modern Prometheus, Mary Shelley)
//
// Known limits the other way, also not asserted: other books the filter
// keeps. A role word straight before a title of up to three words reads the
// title as the contributor's name, the same shape as "narrated Ray Porter" and
// "narrated Mary Beth Hurt"; a release with
// no credit after its extra words, or with the author before them, is never
// read by the run on rule; a full title keyword match never reaches it; and a
// "by" credit that also names the wanted author is not a conflict:
//   - Halo Narrated Silent Storm - Troy Denning  (Halo: Fireteam Noble)
//   - Halo Unabridged Narrated Silent Storm - Troy Denning  (Halo: Fireteam Noble)
//   - Halo narrated Silent Storm Reborn - Troy Denning, Halo narrated Silent Storm Part One - Troy Denning  (Halo: Fireteam Noble)
//   - Halo.Translated.Silent.Storm-Troy.Denning  (Halo: Fireteam Noble)
//   - Halo.Oblivion.Troy.Denning.M4B, Halo Oblivion Troy Denning M4B  (Halo: Fireteam Noble)
//   - Being and Nothingness Nausea - Sartre  (surname only credit)
//   - Catch Twenty Two by Joseph Heller, Catch Twenty-Two - Joseph Heller  (Catch: A Novel)
//   - Halo First by Eric Nylund  (Halo: The Fall of Reach)
//   - Halo Thirty by Troy Denning  (Halo: Fireteam Noble)
//   - Jack Reacher Sixty One by Lee Child  (Jack Reacher: Killing Floor)
//   - Dune The Graphic Novel by Frank Herbert, Mistborn Graphic Novel by Brandon Sanderson  (main title readings, now dropped, still not pinned)
//   - Project Hail Mary The Graphic Novel by Andy Weir  (Project Hail Mary: A Novel, a full title keyword match)
//   - Sapiens Yuval Noah Harari Graphic History Vol 1  (author before the extra words; main keeps it too)
//   - The Last Ride by Nick Louth Craig Alanson, Smoke & Mirrors by E A Copen - J. A. Jance fans  (the credit names the wanted author)
func TestReviewCorpusRejects(t *testing.T) {
	cases := []corpusCase{
		// A role word skips the contributor's name and reading goes on
		// (fifth review of #3640).
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Silent Storm narrated by Jen Taylor - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Silent Storm Narrated Jen Taylor - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Narrated by Jen Taylor Silent Storm - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Narrated Jen Taylor Silent Storm by Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Read by Jen Taylor - Silent Storm - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Illustrated Silent Storm - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Presented Silent Storm by Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Annotated Silent Storm by Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo You Are Not Alone - Troy Denning"},
		{"Halo: The Fall of Reach", "Eric Nylund", "", "", "Halo Shared Destiny - Eric Nylund"},
		// A function word ends the name, so a title of three words or more
		// after a role word is still read (sixth review of #3640).
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo narrated Shadows of Reach - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo read Point of Light - Troy Denning"},
		// "Graphic" is not furniture: a graphic adaptation is another book.
		{"Sapiens: A Brief History of Humankind", "Yuval Noah Harari", "", "", "Sapiens A Graphic History by Yuval Noah Harari"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Oblivion Master Chief Story - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Oblivion A Master Chief Story - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo: Oblivion: A Master Chief Story - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Oblivion - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Silent Storm by Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Shadows of Reach - Troy Denning"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo: Silent Storm - Troy Denning [ENG / M4B]"},
		{"Halo: The Fall of Reach", "Eric Nylund", "", "", "Halo First Strike by Eric Nylund"},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan", "", "", "Camp Half-Blood Confidential - Rick Riordan"},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan", "", "", "Camp Half-Blood Confidential Demigod Files - Rick Riordan"},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan", "", "", "Camp Half-Blood Confidential by Rick Riordan"},
		{"Mistborn: The Final Empire", "Brandon Sanderson", "", "", "Mistborn The Well of Ascension - Brandon Sanderson"},
		{"Red Rising: Golden Son", "Pierce Brown", "", "", "Red Rising Dark Age - Pierce Brown"},
		{"Red Rising: Golden Son", "Pierce Brown", "", "", "Red Rising Light Bringer - Pierce Brown"},
		{"Jack Reacher: Killing Floor", "Lee Child", "", "", "Jack Reacher Sixty One Hours by Lee Child"},
		{"Star Wars: The Rise of Skywalker", "Rae Carson", "", "", "Star Wars Risk by Rae Carson"},
		{"Red Rising: Morning Star", "Pierce Brown", "", "", "Red Rising Most by Pierce Brown"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Fino by Troy Denning"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Nausea - Jean-Paul Sartre"},
		{"Being and Nothingness: An Essay on Phenomenological Ontology", "Jean-Paul Sartre", "", "", "Being and Nothingness Nausea by Jean-Paul Sartre"},
		{"We Were Eight Years in Power: An American Tragedy", "Ta-Nehisi Coates", "", "", "We Were Eight Years in Power Between the World and Me - Ta-Nehisi Coates"},
		{"Some Book: A Novel", "Mary-Jo Smith", "", "", "Some Book Other Title - Mary-Jo Smith"},
		{"Spider-Man: Far From Home", "Steve Behling", "", "", "Spider-Man Homecoming-Steve Behling"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Silent Storm-Troy Denning"},
		{"Mistborn: The Final Empire", "Brandon Sanderson", "Mistborn", "1", "Mistborn Book Two by Brandon Sanderson"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo: Oblivion: A Master Chief Story by Troy Denning [ENG / M4B] [VIP]"},
		{"Camp Half-Blood: From the World of Percy Jackson: The Rite of Aphrodite", "Rick Riordan", "", "", "Camp Half-Blood Confidential by Rick Riordan [ENG / MP3] [VIP]"},
		{"Halo: Fireteam Noble", "Troy Denning", "", "", "Halo Oblivion - Troy Denning M4B"},
		{"Dune: Deluxe Edition", "Frank Herbert", "Dune", "1", "Dune Messiah by Frank Herbert"},
		{"Harry Potter: The Illustrated Collection", "J.K. Rowling", "", "", "Harry Potter Box Set by J.K. Rowling"},
	}
	for _, c := range cases {
		if v, ok := corpusVerdict(c); !ok || v == "" {
			t.Errorf("%q for %q by %s: kept, want dropped", c.release, c.title, c.author)
		}
	}
}
