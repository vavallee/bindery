package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// These releases were auto-grabbed and imported as three different books.
func TestFilterRelevantConflictingBookIdentity(t *testing.T) {
	cases := []struct{ title, author, release string }{
		{"Power down", "Ben Coes", "The Power of Writing It Down by Allison Fallon EPUB"},
		{"12 Rules for Life", "Jordan B. Peterson", "Beyond Order: 12 More Rules for Life by Jordan B. Peterson EPUB"},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat - Edward Luttwak"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := filterRelevant([]newznab.SearchResult{{Title: c.release}}, c.title, c.author, nil)
			if len(got) != 0 {
				t.Fatalf("wrong release accepted: %q for %q by %s", c.release, c.title, c.author)
			}
		})
	}
}

func TestFilterRelevantIdentityCompatibility(t *testing.T) {
	cases := []struct {
		title, author, release string
		want                   bool
	}{
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat - Ben Coes", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat by Ben Coes EPUB", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat by Coes EPUB", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat EPUB", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat - Unabridged MP3", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat - narrated by John Doe", true},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat — Edward Luttwak", false},
		{"Coup d'Etat", "Ben Coes", "Coup D'Etat by Edward Luttwak EPUB", false},
		{"Coup d'Etat", "", "Coup D'Etat - Edward Luttwak", true},
		{"Power Down", "Ben Coes", "Power Down by Ben Coes EPUB", true},
		{"Power Down", "Ben Coes", "Ben Coes - Power Down EPUB", true},
		{"Power Down", "Ben Coes", "Power Down EPUB", true},
		{"Power Down", "Ben Coes", "The Power of Writing It Down by Ben Coes EPUB", false},
		{"12 Rules for Life", "Jordan B. Peterson", "12 Rules for Life by Jordan B. Peterson EPUB", true},
		{"12 Rules for Life", "Jordan B. Peterson", "Jordan B Peterson - 12 Rules for Life EPUB", true},
		{"12 Rules for Life", "Jordan B. Peterson", "12 Rules for Life EPUB", true},
		{"12 Rules for Life: An Antidote to Chaos", "Jordan B. Peterson", "12 Rules for Life EPUB", true},
		{"12 Rules for Life: An Antidote to Chaos", "Jordan B. Peterson", "12 More Rules for Life by Jordan B. Peterson EPUB", false},
		{"The Lord of the Rings", "J.R.R. Tolkien", "The Lord of the Rings EPUB", true},
		{"The Lord of the Rings", "J.R.R. Tolkien", "Lord Rings EPUB", true},
		{"The Lord of the Rings", "J.R.R. Tolkien", "The Lord of the Rings by J. R. R. Tolkien EPUB", true},
		{"Death by Black Hole", "Neil deGrasse Tyson", "Death by Black Hole EPUB", true},
	}
	for _, c := range cases {
		t.Run(c.release, func(t *testing.T) {
			got := filterRelevant(toResults(c.release), c.title, c.author, nil)
			if (len(got) == 1) != c.want {
				t.Fatalf("filterRelevant(%q, %q, %q): kept=%v, want %v", c.release, c.title, c.author, len(got) == 1, c.want)
			}
		})
	}
}
