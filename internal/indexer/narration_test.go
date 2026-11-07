package indexer

import (
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func TestParseNarration_EditionType(t *testing.T) {
	cases := []struct {
		name, title, book string
		authors           []string
		want              string
	}{
		{"bracketed unabridged", "Dune [Unabridged] M4B", "Dune", nil, models.EditionTypeUnabridged},
		{"paren abridged", "Dune (Abridged) MP3", "Dune", nil, models.EditionTypeAbridged},
		{"bare word after known title", "Frank Herbert - Dune Unabridged m4b", "Dune", []string{"Frank Herbert"}, models.EditionTypeUnabridged},
		{"full cast", "Dune [Full Cast] M4B", "Dune", nil, models.EditionTypeFullCast},
		{"dramatised", "Dune (Dramatised Adaptation)", "Dune", nil, models.EditionTypeDramatized},
		{"nothing stated", "Frank Herbert - Dune m4b", "Dune", nil, ""},
		// Negative controls.
		{"abridged only inside the book name", "The Abridged History of Rome - Jane Doe m4b", "The Abridged History of Rome", nil, ""},
		{"book name with abridged, bracketed unabridged wins", "The Abridged History of Rome [Unabridged] m4b", "The Abridged History of Rome", nil, models.EditionTypeUnabridged},
		{"bare word, book title not in release", "Abridged History of Rome m4b", "Something Else", nil, ""},
		{"bare word, no book title known", "Abridged History of Rome m4b", "", nil, ""},
		{"author name containing marker", "Abridged Smith - Dune m4b", "Dune", []string{"Abridged Smith"}, ""},
		{"negated", "Dune (not abridged) m4b", "Dune", nil, ""},
		{"both markers is unknown", "Dune [Unabridged] [Abridged]", "Dune", nil, ""},
		{"unabridged is not read as abridged", "Dune [Unabridged]", "Dune", nil, models.EditionTypeUnabridged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNarration(tc.title, []string{tc.book}, tc.authors).EditionType
			if got != tc.want {
				t.Fatalf("ParseNarration(%q) edition type = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestParseNarration_Narrators(t *testing.T) {
	cases := []struct {
		name, title string
		authors     []string
		want        []string
	}{
		{"read by", "Dune - Frank Herbert - Read by Scott Brick [Unabridged] M4B", nil, []string{"Scott Brick"}},
		{"narrated by to end", "Dune Narrated by Scott Brick", nil, []string{"Scott Brick"}},
		{"two narrators", "Dune (Narrated by Scott Brick & Orlagh Cassidy) m4b", nil, []string{"Scott Brick", "Orlagh Cassidy"}},
		{"tag shape", "Dune - Scott Brick (narrator) - m4b", nil, []string{"Scott Brick"}},
		// Negative controls.
		{"author credit is not a narrator", "Dune by Frank Herbert m4b", []string{"Frank Herbert"}, []string{}},
		{"bare author and name in dash form", "Frank Herbert - Scott Brick - Dune m4b", []string{"Frank Herbert"}, []string{}},
		{"single word is not a name", "Dune read by Brick m4b", nil, []string{}},
		{"digits are not a name", "Dune read by 2021 m4b", nil, []string{}},
		{"conflicting credits", "Dune read by Scott Brick (narrated by Simon Vance)", nil, []string{}},
		{"no credit", "Dune [Unabridged] m4b", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNarration(tc.title, []string{"Dune"}, tc.authors).Narrators
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseNarration(%q) narrators = %#v, want %#v", tc.title, got, tc.want)
			}
		})
	}
}

// A narrator who is also the book's author is still a narrator when the title
// says "read by", and the author name alone never becomes one.
func TestParseNarration_AuthorNarrator(t *testing.T) {
	got := ParseNarration("Coraline - Neil Gaiman - Read by Neil Gaiman m4b", []string{"Coraline"}, []string{"Neil Gaiman"}).Narrators
	if !reflect.DeepEqual(got, []string{"Neil Gaiman"}) {
		t.Fatalf("explicit read by the author: got %#v", got)
	}
	got = ParseNarration("Coraline - Neil Gaiman m4b", []string{"Coraline"}, []string{"Neil Gaiman"}).Narrators
	if len(got) != 0 {
		t.Fatalf("bare author name read as narrator: %#v", got)
	}
}

func rankTitles(c MatchCriteria, titles ...string) []string {
	rs := make([]newznab.SearchResult, len(titles))
	for i, t := range titles {
		rs[i] = newznab.SearchResult{Title: t, Size: 300 << 20}
	}
	rankResults(rs, c)
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

var audioProfileItems = []models.QualityItem{{Quality: "m4b", Allowed: true}, {Quality: "mp3", Allowed: true}}

func TestRank_NarratorPreference(t *testing.T) {
	a := "Dune - Frank Herbert - Read by Simon Vance m4b"
	b := "Dune - Frank Herbert - Read by Scott Brick m4b"
	base := MatchCriteria{Title: "Dune", Author: "Frank Herbert", MediaType: models.MediaTypeAudiobook}

	// No preference: scores equal those of the same profile with the fields
	// absent, and equal scores keep the stable order.
	plain := base
	plain.Profile = &models.QualityProfile{Items: audioProfileItems}
	ra, rb := newznab.SearchResult{Title: a, Size: 300 << 20}, newznab.SearchResult{Title: b, Size: 300 << 20}
	ranks := newProfileRanks(plain.Profile)
	if scoreResult(ra, plain, ranks) != scoreResult(rb, plain, ranks) {
		t.Fatal("unset preference made two otherwise equal releases score differently")
	}
	if got := rankTitles(plain, a, b); got[0] != a {
		t.Fatalf("with no preference the stable order must hold, got %v", got)
	}

	// Preferring Scott Brick lifts b above a. Fails before the feature: the
	// scores were equal and a stayed first.
	pref := base
	pref.Profile = &models.QualityProfile{Items: audioProfileItems, PreferredNarrators: []string{"scott brick"}}
	if got := rankTitles(pref, a, b); got[0] != b {
		t.Fatalf("preferred narrator not ranked first: %v", got)
	}
	// Earlier entries beat later ones.
	pref.Profile.PreferredNarrators = []string{"Simon Vance", "Scott Brick"}
	if got := rankTitles(pref, b, a); got[0] != a {
		t.Fatalf("first preferred narrator not ranked first: %v", got)
	}
	// Ebook searches are untouched.
	pref.MediaType = models.MediaTypeEbook
	if got := rankTitles(pref, a, b); got[0] != a {
		t.Fatalf("ebook search changed by narrator preference: %v", got)
	}
}

func TestRank_EditionPreference(t *testing.T) {
	unabr := "Dune - Frank Herbert [Unabridged] m4b"
	unknown := "Dune - Frank Herbert m4b"
	abr := "Dune - Frank Herbert [Abridged] m4b"
	base := MatchCriteria{Title: "Dune", Author: "Frank Herbert", MediaType: models.MediaTypeAudiobook,
		Profile: &models.QualityProfile{Items: audioProfileItems, EditionPreference: models.EditionPrefPreferUnabridged}}

	got := rankTitles(base, abr, unknown, unabr)
	if got[0] != unabr || got[2] != abr {
		t.Fatalf("prefer_unabridged order wrong: %v", got)
	}

	// The preference outweighs a one step format advantage; the built in
	// unabridged bonus alone does not (fails before the feature).
	unabrMP3 := "Dune - Frank Herbert [Unabridged] mp3"
	unknownM4B := "Dune - Frank Herbert m4b"
	if got := rankTitles(base, unknownM4B, unabrMP3); got[0] != unabrMP3 {
		t.Fatalf("prefer_unabridged did not outweigh format: %v", got)
	}
	off := base
	off.Profile = &models.QualityProfile{Items: audioProfileItems}
	if got := rankTitles(off, unabrMP3, unknownM4B); got[0] != unknownM4B {
		t.Fatalf("preference off must keep today's order: %v", got)
	}

	// Book named "Abridged": the name alone must not cost the release.
	book := MatchCriteria{Title: "The Abridged History of Rome", Author: "Jane Doe", MediaType: models.MediaTypeAudiobook,
		Profile: &models.QualityProfile{Items: audioProfileItems, EditionPreference: models.EditionPrefPreferUnabridged}}
	n := book.ReleaseNarration("The Abridged History of Rome - Jane Doe m4b")
	if n.EditionType != "" {
		t.Fatalf("book name read as edition type %q", n.EditionType)
	}
	if EditionTypeScore(book.Profile.EditionPreference, n.EditionType) != 0 {
		t.Fatal("unknown edition type scored")
	}
}
