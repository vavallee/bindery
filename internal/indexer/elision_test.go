package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// An apostrophe marks ELISION in French, Spanish and Italian ("L'Outsider",
// "Sac d'os", "l'isola"), and release names in those languages keep it as a
// separator ("Stephen.King.L.Outsider.2018.FRENCH"). SigWords deletes it, so
// the title yields the single token "loutsider", which no release name
// contains — the search returns zero results and says nothing.
//
// These are the real titles and release names that were measured on a live
// catalogue. The possessive cases are pinned alongside them because the
// deleted form exists FOR possessives: whichever fix is adopted, "Ender's
// Game" must keep matching "Enders.Game".
//
// The titles without an apostrophe in their elided position are here as
// controls: they must keep matching for the same reason they do today.
func TestFilterRelevantElidedTitles(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		author  string
		release string
		want    bool
	}{
		{
			name:    "single-token elision: apostrophe vs dotted release name",
			title:   "L'Outsider",
			author:  "Stephen King",
			release: "Stephen.King.L.Outsider.2018.FRENCH.[ePub]-NOTAG",
			want:    true,
		},
		{
			name:    "single-token elision with the typographic apostrophe",
			title:   "L\u2019Outsider",
			author:  "Stephen King",
			release: "Stephen.King.L.Outsider.2018.FRENCH.[ePub]-NOTAG",
			want:    true,
		},
		{
			name:    "single-token elision, release separates with a space",
			title:   "L'Institut",
			author:  "Stephen King",
			release: "Stephen King - L Institut (2019) FRENCH EPUB",
			want:    true,
		},
		{
			name:    "two-token elision",
			title:   "Sac d'os",
			author:  "Stephen King",
			release: "Stephen.King.Sac.d.os.2015.FRENCH.ePub-NOTAG",
			want:    true,
		},
		{
			name:    "possessive must keep matching the apostrophe-free convention",
			title:   "Ender's Game",
			author:  "Orson Scott Card",
			release: "Enders.Game.1985.EPUB-GRP",
			want:    true,
		},
		{
			name:    "possessive whose release DOES carry the apostrophe",
			title:   "Ender's Game",
			author:  "Orson Scott Card",
			release: "Ender's.Game.1985.EPUB-GRP",
			want:    true,
		},
		{
			name:    "control: no apostrophe, unchanged matching",
			title:   "Le Fleau",
			author:  "Stephen King",
			release: "Le.Fleau.Stephen.King.[INTEGRALE].1993.FR.[EPUB]-NoTag",
			want:    true,
		},
		{
			name:    "an elided title must not swallow an unrelated release",
			title:   "L'Outsider",
			author:  "Stephen King",
			release: "Stephen.King.The.Stand.1978.FRENCH.ePub-NOTAG",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterRelevant([]newznab.SearchResult{{Title: tc.release}}, tc.title, tc.author, nil)
			if matched := len(got) > 0; matched != tc.want {
				t.Fatalf("filterRelevant(title=%q, author=%q, release=%q) matched=%v, want %v",
					tc.title, tc.author, tc.release, matched, tc.want)
			}
		})
	}
}

// Both filter paths are LIVE, and they are reached by different callers:
// filterRelevant is called by the searcher itself (searcher.go, the ladder),
// filterRelevantDebug by SearchBookWithDebug, which the interactive search and
// auto-grab both go through (SearchBookWithOutcomes is a projection of it).
//
// A fix applied to one and not the other is invisible where it was tested and
// live where it matters. That is exactly how this bug survived its first
// attempt: the unit test passed against filterRelevant while the search panel
// kept returning zero, because the panel reads filterRelevantDebug.
func TestFilterPathsAgreeOnElision(t *testing.T) {
	cases := []struct {
		title   string
		author  string
		release string
		want    bool
	}{
		{"L'Outsider", "Stephen King", "Stephen.King.L.Outsider.2018.FRENCH.[ePub]-NOTAG", true},
		{"L\u2019Outsider", "Stephen King", "Stephen.King.L.Outsider.2018.FRENCH.[ePub]-NOTAG", true},
		{"L'Institut", "Stephen King", "Stephen King - L Institut (2019) FRENCH EPUB", true},
		{"Sac d'os", "Stephen King", "Stephen.King.Sac.d.os.2015.FRENCH.ePub-NOTAG", true},
		{"Ender's Game", "Orson Scott Card", "Enders.Game.1985.EPUB-GRP", true},
		{"L'Outsider", "Stephen King", "Stephen.King.The.Stand.1978.FRENCH.ePub-NOTAG", false},
	}

	for _, tc := range cases {
		rs := []newznab.SearchResult{{Title: tc.release}}
		plain := len(filterRelevant(rs, tc.title, tc.author, nil)) > 0
		debugged, _ := filterRelevantDebug(rs, tc.title, tc.author, nil)
		viaDebug := len(debugged) > 0

		if plain != viaDebug {
			t.Fatalf("paths diverge for title=%q release=%q: filterRelevant=%v filterRelevantDebug=%v",
				tc.title, tc.release, plain, viaDebug)
		}
		if plain != tc.want {
			t.Fatalf("title=%q release=%q: matched=%v, want %v", tc.title, tc.release, plain, tc.want)
		}
	}
}

// A colon makes the searcher consider the title PRIMARY half too
// (primaryTitle: "Dune: Messiah" -> "Dune"), and that half needs the same
// elision reading: a release routinely names the book without its subtitle,
// so the primary reading is the only one that can match it. Both filters
// carry the fallback; this pins the subtitle path in the debug filter, which
// is the one the search panel and auto-grab read.
func TestFilterRelevantMatchesElidedPrimaryTitle(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		author  string
		release string
		want    bool
	}{
		{
			name:    "elided primary title, release without the subtitle",
			title:   "L'Institut: roman",
			author:  "Stephen King",
			release: "Stephen.King.L.Institut.2019.FRENCH.[ePub]-NOTAG",
			want:    true,
		},
		{
			name:    "control: the primary reading must not swallow another book",
			title:   "L'Institut: roman",
			author:  "Stephen King",
			release: "Stephen.King.Le.Fleau.1993.FR.[EPUB]-NoTag",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := []newznab.SearchResult{{Title: tc.release}}
			plain := len(filterRelevant(rs, tc.title, tc.author, nil)) > 0
			debugged, _ := filterRelevantDebug(rs, tc.title, tc.author, nil)
			viaDebug := len(debugged) > 0

			if plain != viaDebug {
				t.Fatalf("paths diverge for title=%q release=%q: filterRelevant=%v filterRelevantDebug=%v",
					tc.title, tc.release, plain, viaDebug)
			}
			if plain != tc.want {
				t.Fatalf("title=%q release=%q: matched=%v, want %v", tc.title, tc.release, plain, tc.want)
			}
		})
	}
}
