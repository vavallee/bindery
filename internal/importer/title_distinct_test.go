package importer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestTitlesNameDifferentWorks is the guard's table. The first block is the
// reported pair and its kind: a long shared series name with different books
// after it. The rest must NOT read as different works. Calling a real match
// different (a false positive for this guard) costs a file its match and
// leaves it unmatched, which can also make an owned book read as not owned.
// Missing a different work (a false negative) is the old behaviour: the file
// is attached to the wrong book.
func TestTitlesNameDifferentWorks(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		// Different works under one series name.
		{"reported pair", "Harry Potter en de Relieken van de Dood", "Harry Potter en de Orde van de Feniks", true},
		{"reported pair, filename casing", "Harry Potter En De Relieken Van De Dood", "Harry Potter en de Orde van de Feniks", true},
		{"reported pair, reversed", "Harry Potter en de Orde van de Feniks", "Harry Potter en de Relieken van de Dood", true},
		{"english volumes", "Harry Potter and the Chamber of Secrets", "Harry Potter and the Prisoner of Azkaban", true},
		{"german volumes", "Harry Potter und der Stein der Weisen", "Harry Potter und der Orden des Phoenix", true},
		{"cursed child", "Harry Potter en de Relieken van de Dood", "Harry Potter en het Vervloekte Kind", true},
		{"one distinct word each", "The Hunger Games Mockingjay", "The Hunger Games Catching Fire", true},
		{"non latin script, two titles", "三体", "黑暗森林", true},

		// The same work.
		{"identical", "Harry Potter en de Orde van de Feniks", "Harry Potter en de Orde van de Feniks", false},
		{"casing and punctuation", "harry potter en de orde van de feniks", "Harry Potter en de Orde van de Feniks!", false},
		{"one letter typo", "Harry Potter and the Goblet of Fyre", "Harry Potter and the Goblet of Fire", false},
		{"apostrophe dropped", "Punkd", "Punk'd", false},
		{"apostrophe dropped inside a title", "Rotten School Punkd and Skunked", "Rotten School: Punk'd and Skunked", false},
		{"accents folded", "Les Miserables", "Les Misérables", false},
		{"umlauts spelled out", "Die Hohle der Loewen", "Die Höhle der Löwen", false},
		{"spelling variant", "Colour of Magic", "Color of Magic", false},
		{"plural", "Dragon Rider", "Dragons Rider", false},
		{"leading article", "The Name of the Wind", "Name of the Wind", false},
		{"comma article form", "Name of the Wind, The", "The Name of the Wind", false},
		{"word order", "Feniks Orde", "Orde Feniks", false},
		{"subtitle dropped", "Dune Messiah", "Dune", false},
		{"subtitle added", "Mistborn", "Mistborn: The Final Empire", false},
		{"subtitle both ways round", "Mistborn: The Final Empire", "Mistborn", false},
		{"one title inside the other", "Deep Work", "Deep Work: Rules for Focused Success in a Distracted World", false},
		{"edition label", "Dune Messiah Deluxe Edition", "Dune Messiah", false},
		{"series note in brackets", "Rhythm of War Part 1", "Rhythm of War (The Stormlight Archive, Book 4)", false},
		{"square bracket tag", "Rhythm of War [Unabridged]", "Rhythm of War", false},
		{"author in the file name, series in brackets", "Rhythm of War Sanderson", "Rhythm of War (The Stormlight Archive)", false},
		{"one word inside a longer compound", "Harry Potter Kind", "Harry Potter Vervloektekind", false},
		{"philosopher against sorcerer", "Harry Potter and the Philosopher's Stone", "Harry Potter and the Sorcerer's Stone", false},
		{"sorcerer against philosopher", "Harry Potter and the Sorcerer's Stone", "Harry Potter and the Philosopher's Stone", false},
		{"abbreviated honorifics", "Dr. Jekyll and Mr. Hyde", "Doctor Jekyll and Mister Hyde", false},
		{"doctor against dr", "Doctor Sleep", "Dr. Sleep", false},
		{"saint, junior, versus, missus", "St. Elmo Jr. vs Mrs. Dalloway", "Saint Elmo Junior versus Missus Dalloway", false},
		{"ligature letters", "Æsop's Fables", "Aesop's Fables", false},
		{"slashed o", "Øya Stories", "Oya Stories", false},
		{"fi ligature", "The ﬁre Within", "The fire Within", false},
		{"equivalents do not blur real differences", "Harry Potter and the Philosopher's Stone", "Harry Potter and the Chamber of Secrets", true},
		{"filler word on each side", "Project Hail Mary Annotated", "Project Hail Mary: A Novel", false},
		{"volume numbers are not this rule's", "Defiance of the Fall 17", "Defiance of the Fall 01", false},
		{"digits on one side", "Harry Potter 07 Relieken", "Harry Potter Relieken", false},
		{"a compound word holds the other", "Weltraum Abenteuer", "Weltraumabenteuer Abenteuer", false},
		{"single word", "Dune", "Dune", false},
		{"two words, one differs by a letter", "Gone Girl", "Gone Girls", false},
		{"non latin script, subtitle without a space", "三体", "三体黑暗森林", false},
		{"non latin script, one character apart", "我的名字叫红", "我的名字叫黄", false},
		{"empty side", "", "Dune", false},
		{"nothing left but stopwords", "The Of And", "A In To", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := titlesNameDifferentWorks(tc.a, tc.b); got != tc.want {
				t.Errorf("titlesNameDifferentWorks(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestTitleWordsNear pins the closeness the guard forgives.
func TestTitleWordsNear(t *testing.T) {
	tests := []struct {
		x, y string
		want bool
	}{
		{"fire", "fyre", true},
		{"fire", "fires", true},
		{"colour", "color", true},
		{"dragon", "dragonfly", true},
		{"relieken", "dood", false},
		{"orde", "feniks", false},
		{"orde", "order", true},
		{"het", "de", false},
		{"cat", "cot", false},          // under four characters there is no slip to forgive
		{"abcdefgh", "abcdefxy", true}, // two edits allowed from eight characters
		{"abcdefg", "abcdexy", false},  // but not under eight
		{"三体", "三体二", true},
	}
	for _, tc := range tests {
		if got := titleWordsNear(tc.x, tc.y); got != tc.want {
			t.Errorf("titleWordsNear(%q, %q) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
		if got := titleWordsNear(tc.y, tc.x); got != tc.want {
			t.Errorf("titleWordsNear(%q, %q) = %v, want %v (reversed)", tc.y, tc.x, got, tc.want)
		}
	}
}

// scanOne scans a library holding one epub in the author's folder and one
// wanted ebook, and returns the path the book was given.
func scanOneEbook(t *testing.T, author, file, wanted string) string {
	t.Helper()
	e := newVolumeScanEnv(t, author)
	writeFile(t, filepath.Join(e.libDir, author, file))
	b := e.wanted(t, wanted, models.MediaTypeEbook)
	e.s.ScanLibrary(e.ctx)
	return e.get(t, b.ID).EbookFilePath
}

// TestScanLibrary_TitleGuardKeepsTheSameWorkMatching: the file is the wanted
// book however its title is spelled or trimmed. Each of these is above the
// Jaro-Winkler gate and must still reconcile.
func TestScanLibrary_TitleGuardKeepsTheSameWorkMatching(t *testing.T) {
	tests := []struct{ name, author, file, wanted string }{
		{"typo", "J. K. Rowling", "Harry Potter and the Goblet of Fyre - J. K. Rowling.epub", "Harry Potter and the Goblet of Fire"},
		{"apostrophe", "Rotten School", "Rotten School Punkd and Skunked - Rotten School.epub", "Rotten School: Punk'd and Skunked"},
		{"edition label added", "Frank Herbert", "Dune Messiah Deluxe Edition - Frank Herbert.epub", "Dune Messiah"},
		{"series note in the catalogue title", "Brandon Sanderson", "Rhythm of War - Brandon Sanderson.epub", "Rhythm of War (The Stormlight Archive, Book 4)"},
		{"accents", "Victor Hugo", "Les Miserables - Victor Hugo.epub", "Les Misérables"},
		{"short title", "George Orwell", "Animal Farm - George Orwell.epub", "Animal Farm"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := filepath.Join("", tc.author, tc.file)
			got := scanOneEbook(t, tc.author, tc.file, tc.wanted)
			if got == "" || filepath.Base(got) != filepath.Base(want) {
				t.Errorf("%q did not reconcile onto %q (got %q)", tc.file, tc.wanted, got)
			}
		})
	}
}

// TestScanLibrary_TitleGuardLeavesADifferentWorkUnmatched: the same shape as
// #3388 for ebooks. The only wanted book shares a long series name with the
// file's title, which is the next volume, and the file stays unmatched.
func TestScanLibrary_TitleGuardLeavesADifferentWorkUnmatched(t *testing.T) {
	if got := scanOneEbook(t, "J. K. Rowling", "Harry Potter and the Chamber of Secrets - J. K. Rowling.epub",
		"Harry Potter and the Prisoner of Azkaban"); got != "" {
		t.Errorf("the Chamber of Secrets file was attached to the Prisoner of Azkaban: %q", got)
	}
}

// TestFindExisting_DoesNotOfferADifferentWork is the add path of #3388. Adding
// Order of the Phoenix to a library that holds only the Deathly Hallows must
// not read as already owned: titleWordsMatch needs two shared words and the
// series name supplies them.
func TestFindExisting_DoesNotOfferADifferentWork(t *testing.T) {
	root := t.TempDir()
	hallows := filepath.Join(root, "J. K. Rowling", "Harry Potter en de Relieken van de Dood.epub")
	writeFile(t, hallows)
	snap := NewLibrarySnapshot(root, "")
	ctx := context.Background()

	if got := snap.FindExisting(ctx, "Harry Potter en de Orde van de Feniks", "J. K. Rowling", models.MediaTypeEbook); got != "" {
		t.Errorf("Orde van de Feniks found %q, which is the Relieken van de Dood", got)
	}
	if got := snap.FindExisting(ctx, "Harry Potter en de Relieken van de Dood", "J. K. Rowling", models.MediaTypeEbook); got != hallows {
		t.Errorf("the Relieken van de Dood file was not found for its own title: %q", got)
	}
}
