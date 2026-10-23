package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Review round: placeholder creators, bounded title folds, non-Latin editions,
// the ebook list cap.

func TestEbookAuthorMatches_PlaceholderCreatorsSayNothing(t *testing.T) {
	names := []string{"J.K. Rowling"}
	for _, creator := range []string{
		"Unknown", "unknown", "Anonymous", "Various", "Various Authors", "calibre", "Calibre",
		"Project Gutenberg", "Unknown Author", "Penguin Group", "Scholastic Inc.", "Random House Publishing Group",
		"Hachette Book Group", "admin@example.com", "someone@publisher.example", "Unknown; Anonymous",
	} {
		if !ebookAuthorMatches(names, creator) {
			t.Errorf("creator %q was read as a different author", creator)
		}
	}
	// A real different author still disagrees, and so does a real name that only
	// shares a word with the placeholders.
	for _, creator := range []string{"Blake Crouch", "Mary Trevelyan", "Anonymous Smith Jones"} {
		if ebookAuthorMatches(names, creator) {
			t.Errorf("creator %q was read as %v", creator, names)
		}
	}
}

// The reviewer's end to end case: a title variant plus a placeholder creator
// must not block.
func TestEbookContentCheck_PlaceholderCreatorWithTitleVariantImportsInBlockMode(t *testing.T) {
	f := newContentFixture(t, "Harry Potter and the Philosopher's Stone", "J.K. Rowling")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Harry Potter and the Sorcerer's Stone", "Unknown")
	f.importDownload(t)
	f.assertImportedSilently(t)
}

func TestContentTitleSupports_BoundedFolds(t *testing.T) {
	cases := []struct {
		wanted, file string
		want         bool
	}{
		{"The Hitch-Hiker's Guide to the Galaxy", "The Hitchhiker's Guide to the Galaxy", true},
		{"The Hitchhikers Guide to the Galaxy", "The Hitch Hiker's Guide to the Galaxy", true},
		{"Dr. Jekyll and Mr. Hyde", "Doctor Jekyll and Mister Hyde", true},
		{"Doctor Sleep", "Dr Sleep", true},
		{"Mrs. Dalloway", "Missus Dalloway", true},
		{"St. Elmo", "Saint Elmo", true},
		{"Slaughterhouse-Five", "Slaughterhouse 5", true},
		{"Slaughterhouse 5", "Slaughterhouse Five", true},
		{"20,000 Leagues Under the Sea", "Twenty Thousand Leagues Under the Sea", true},
		{"Catch Twenty Two", "Catch 22", true},
		// Documented limits: these stay different.
		{"Harry Potter and the Philosopher's Stone", "Harry Potter and the Sorcerer's Stone", false},
		{"1984", "Nineteen Eighty-Four", false},
		// A fold must not make different books equal.
		{"Seven Days", "Eight Days", false},
		{"St. Elmo", "Elmo", false},
	}
	for _, c := range cases {
		if got := contentTitleSupports(c.wanted, c.file, false); got != c.want {
			t.Errorf("contentTitleSupports(%q, %q) = %v, want %v", c.wanted, c.file, got, c.want)
		}
	}
}

// Non-Latin editions of a Latin catalogued book cannot disagree.
func TestEbookDefiniteMismatch_NonLatinEditionsNeverFlag(t *testing.T) {
	rows := []struct {
		name, title string
		names       []string
		fileTitle   string
		fileBy      string
	}{
		{"Russian edition of Harry Potter", "Harry Potter and the Philosopher's Stone", []string{"J.K. Rowling"}, "Гарри Поттер и философский камень", "Дж. К. Роулинг"},
		{"Japanese edition of Harry Potter", "Harry Potter and the Philosopher's Stone", []string{"J.K. Rowling"}, "ハリー・ポッターと賢者の石", "J.K.ローリング"},
		{"Russian Crime and Punishment", "Crime and Punishment", []string{"Fyodor Dostoevsky"}, "Преступление и наказание", "Фёдор Достоевский"},
		{"Japanese Norwegian Wood", "Norwegian Wood", []string{"Haruki Murakami"}, "ノルウェイの森", "村上春樹"},
		{"Hebrew Sapiens", "Sapiens", []string{"Yuval Noah Harari"}, "ספיינס", "יובל נח הררי"},
		{"Arabic Children of Gebelawi", "Children of Gebelawi", []string{"Naguib Mahfouz"}, "أولاد حارتنا", "نجيب محفوظ"},
		{"Chinese Three-Body Problem", "The Three-Body Problem", []string{"Liu Cixin"}, "三体", "刘慈欣"},
		{"non-Latin title with a Latin but different creator", "Sapiens", []string{"Yuval Noah Harari"}, "ספיינס", "Somebody Else"},
		{"Latin title with a non-Latin creator", "Sapiens", []string{"Yuval Noah Harari"}, "Another Book", "יובל נח הררי"},
	}
	for _, r := range rows {
		if ebookDefiniteMismatch(r.title, r.names, r.fileTitle, r.fileBy) {
			t.Errorf("%s was flagged", r.name)
		}
	}
	// Non-Latin against non-Latin keeps comparing: two different Russian books by
	// two different Russian authors still disagree.
	if !ebookDefiniteMismatch("Преступление и наказание", []string{"Фёдор Достоевский"}, "Война и мир", "Лев Толстой") {
		t.Error("two different non-Latin books were not flagged")
	}
	if ebookDefiniteMismatch("Преступление и наказание", []string{"Фёдор Достоевский"}, "Преступление и наказание", "Фёдор Достоевский") {
		t.Error("the same non-Latin book was flagged")
	}
}

// An album or track title with no Latin letters, against a Latin wanted title,
// is no usable title: it never counts as another work.
func TestEvaluateAudiobookContent_NonLatinTitlesAreNotUsable(t *testing.T) {
	wanted := "Harry Potter and the Philosopher's Stone"
	tags := join(
		tracksWithAlbum(wanted, 30),
		tracksWithAlbum("Гарри Поттер и философский камень", 6),
		tracksWithAlbum("ハリー・ポッターと賢者の石", 4),
		tracksWithTitle("三体", 3),
	)
	if r := evaluateAudiobookContent(wanted, tags); r.flagged {
		t.Errorf("non-Latin titles counted as other works: %+v", r)
	}
	// Non-Latin against non-Latin is still compared.
	ru := join(tracksWithAlbum("Война и мир", 30), tracksWithAlbum("Преступление и наказание", 10))
	if r := evaluateAudiobookContent("Война и мир", ru); !r.flagged {
		t.Errorf("a different non-Latin book was not flagged: %+v", r)
	}
	// A Latin other work beside a non-Latin one still counts.
	mixed := join(tracksWithAlbum(wanted, 30), tracksWithAlbum("Dune", 6), tracksWithAlbum("Гарри Поттер", 4))
	if r := evaluateAudiobookContent(wanted, mixed); !r.flagged || mismatchCount(r, "Dune") != 6 {
		t.Errorf("a Latin other work was lost: %+v", r)
	}
}

// The ebook mismatches list is capped at 10 entries.
func TestEbookContentCheck_ListIsCapped(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "seed.epub", "Recursion", "Blake Crouch")
	data, err := os.ReadFile(filepath.Join(f.dir, "seed.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, "seed.epub")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("book_%03d.epub", i)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.importDownload(t)

	cc, ok := f.eventData(t, models.HistoryEventImportFailed)["contentCheck"].(map[string]any)
	if !ok {
		t.Fatalf("no contentCheck on the refusal: %v", f.eventData(t, models.HistoryEventImportFailed))
	}
	list, _ := cc["mismatches"].([]any)
	if len(list) != maxContentCheckTitles {
		t.Errorf("listed %d mismatches, want the cap of %d", len(list), maxContentCheckTitles)
	}
	if got := cc["filesRead"]; got != float64(15) {
		t.Errorf("filesRead = %v, want 15", got)
	}
	if got := cc["moreTitles"]; got != float64(5) {
		t.Errorf("moreTitles = %v, want 5", got)
	}
}
