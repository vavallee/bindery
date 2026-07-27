package metadata

import (
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
)

func book(title, author string) models.Book {
	return models.Book{Title: title, Author: &models.Author{Name: author}}
}

// TestPickEnrichmentMatchAcrossProviderSpellings covers #1647. Both sides of
// this comparison arrive over HTTP from DIFFERENT providers, so differing
// initial spacing and Unicode form is the expected case. Comparing raw
// lowercased strings meant enrichment was permanently dead for those books:
// the comparison is deterministic, so every retry failed identically and
// description, cover, rating and Hardcover genres never arrived.
func TestPickEnrichmentMatchAcrossProviderSpellings(t *testing.T) {
	cases := []struct {
		name      string
		target    models.Book
		candidate models.Book
	}{
		{
			"compact vs spaced initials",
			book("The Hobbit", "J.R.R. Tolkien"),
			book("The Hobbit", "J. R. R. Tolkien"),
		},
		{
			"last-first inversion",
			book("The Hobbit", "Tolkien, J.R.R."),
			book("The Hobbit", "J. R. R. Tolkien"),
		},
		{
			"author unicode form",
			book("Bilder deiner grossen Liebe", norm.NFC.String("Jörg Müller")),
			book("Bilder deiner grossen Liebe", norm.NFD.String("Jörg Müller")),
		},
		{
			"title unicode form",
			book(norm.NFC.String("Geräusch"), "Thomas Mann"),
			book(norm.NFD.String("Geräusch"), "Thomas Mann"),
		},
		{
			"transliterated author",
			book("Ansichten eines Clowns", "Heinrich Böll"),
			book("Ansichten eines Clowns", "Heinrich Boell"),
		},
		// The behaviour that already worked must keep working.
		{
			"subtitle containment",
			book("Der Prozess", "Franz Kafka"),
			book("Der Prozess: Roman", "Franz Kafka"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickEnrichmentMatch([]models.Book{tc.candidate}, &tc.target)
			if got == nil {
				t.Fatalf("pickEnrichmentMatch returned nil for target %q/%q against candidate %q/%q",
					tc.target.Title, tc.target.Author.Name, tc.candidate.Title, tc.candidate.Author.Name)
			}
		})
	}
}

// TestPickEnrichmentMatchStaysConservative guards the widening. A false
// positive here overwrites the user's book with data from an unrelated record,
// which is much worse than the false negative it replaced.
func TestPickEnrichmentMatchStaysConservative(t *testing.T) {
	cases := []struct {
		name      string
		target    models.Book
		candidate models.Book
	}{
		{"different author", book("The Hobbit", "J. R. R. Tolkien"), book("The Hobbit", "Terry Pratchett")},
		{"different book", book("The Hobbit", "J. R. R. Tolkien"), book("The Silmarillion", "J. R. R. Tolkien")},
		{"same surname only", book("Wuthering Heights", "Emily Brontë"), book("Wuthering Heights", "Charlotte Brontë")},
		{"candidate has no author", book("The Hobbit", "J. R. R. Tolkien"), models.Book{Title: "The Hobbit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickEnrichmentMatch([]models.Book{tc.candidate}, &tc.target); got != nil {
				t.Fatalf("pickEnrichmentMatch matched %q/%v when it should not", got.Title, got.Author)
			}
		})
	}
}

// TestPickEnrichmentMatchNoTargetAuthor pins the documented shortcut: with no
// author on the target, a title match alone is enough.
func TestPickEnrichmentMatchNoTargetAuthor(t *testing.T) {
	target := models.Book{Title: "The Hobbit"}
	if got := pickEnrichmentMatch([]models.Book{book("The Hobbit", "Anyone At All")}, &target); got == nil {
		t.Fatal("expected a title-only match when the target carries no author")
	}
	if got := pickEnrichmentMatch([]models.Book{book("Something Else", "Anyone")}, &target); got != nil {
		t.Fatal("expected no match when the titles differ")
	}
}
