package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// Regression for the relevance fuzzy-match collisions observed in production:
// the keyword fallback accepted releases that merely contained every title word
// in any order, with no author check, pulling in wrong books.
func TestFilterRelevantReorderedKeywordCollisions(t *testing.T) {
	cases := []struct {
		title   string
		author  string
		release string
		want    bool // true = kept (relevant), false = dropped
	}{
		// Junk: title words present but REORDERED, wrong author -> drop.
		{"Locked Doors", "Blake Crouch", "Becki Willis - Keep Your Doors Locked (epub)", false},
		{"Body of Secrets", "James Bamford", "Chris.van.Tulleken-Secrets.of.the.Human.Body.epub", false},
		{"Flash Boys", "Michael Lewis", "Ted Neill - Zombies, Frat Boys, Monster Flash Mobs.epub", false},
		{"Summer Frost", "Blake Crouch", "Hailey Frost - Summer Haze and Tokyo Craze.epub", false},
		// Legit: in-order stop-word title (no author), author-named, surname-only -> keep.
		{"The Lord of the Rings", "J.R.R. Tolkien", "The.Lord.of.the.Rings.Fellowship.epub", true},
		{"Body of Secrets", "James Bamford", "James.Bamford.-.Body.of.Secrets.epub", true},
		{"Locked Doors", "Blake Crouch", "Blake.Crouch.-.Locked.Doors.epub", true},
	}
	for _, c := range cases {
		got := filterRelevant([]newznab.SearchResult{{Title: c.release}}, c.title, c.author, nil)
		pass := len(got) == 1
		if pass != c.want {
			t.Errorf("filterRelevant(%q | title=%q author=%q): kept=%v, want %v", c.release, c.title, c.author, pass, c.want)
		}
	}
}
