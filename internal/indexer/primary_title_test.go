package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// batchVerdicts runs a whole batch through the relevance filter, because part
// 2 of #3268 judges a release against what else the search returned. It maps
// each release title to "" when kept, or the drop reason.
func batchVerdicts(title, author string, releases []string, bc bookContext) map[string]string {
	in := make([]newznab.SearchResult, len(releases))
	for i, r := range releases {
		in[i] = newznab.SearchResult{Title: r}
	}
	kept, dropped := filterRelevantWithContext(in, title, author, nil, bc)
	out := make(map[string]string, len(releases))
	for _, k := range kept {
		out[k.Title] = ""
	}
	for _, d := range dropped {
		out[d.Title] = d.Reason
	}
	return out
}

// TestPrimaryTitleFallbackRejectsAnotherBook pins #3268 part 2: the main title
// before the colon no longer stands in for "Main: Subtitle" when the release
// carries a different number, another title, or is a sibling of a release that
// carries the whole title.
func TestPrimaryTitleFallbackRejectsAnotherBook(t *testing.T) {
	cases := []struct {
		name          string
		title, author string
		releases      []string
		bc            bookContext
		wantKept      []string
		wantDropped   map[string]string
	}{
		{
			name:  "High Druid 3 - Straken, wanted position 1",
			title: "High Druid of Shannara: Jarka Ruus", author: "Terry Brooks",
			releases:    []string{"Terry Brooks - The High Druid of Shannara 3 - Straken MP3"},
			bc:          bookContext{seriesPosition: "1"},
			wantDropped: map[string]string{"Terry Brooks - The High Druid of Shannara 3 - Straken MP3": reasonLaterNumber},
		},
		{
			name:  "Legion main title only, the main title is another library book",
			title: "Legion: Skin Deep", author: "Brandon Sanderson",
			releases: []string{
				"Brandon Sanderson - Legion (2012) MP3",
				"Brandon Sanderson - Legion Novella (2012) MP3",
			},
			bc: bookContext{otherTitles: []string{"Legion", "The Emperor's Soul"}},
			wantDropped: map[string]string{
				"Brandon Sanderson - Legion (2012) MP3":         reasonMainTitleOnlySequel,
				"Brandon Sanderson - Legion Novella (2012) MP3": reasonMainTitleOnlySequel,
			},
		},
		{
			name:  "Legion main title only, another result carries the full title",
			title: "Legion: Skin Deep", author: "Brandon Sanderson",
			releases: []string{
				"Brandon Sanderson - Legion (2012) MP3",
				"Brandon Sanderson - Legion - Skin Deep (2014) MP3",
			},
			wantKept:    []string{"Brandon Sanderson - Legion - Skin Deep (2014) MP3"},
			wantDropped: map[string]string{"Brandon Sanderson - Legion (2012) MP3": reasonMainTitleOnlyFullFound},
		},
		{
			name:  "The Hobbit main title only drops when the full title is in the batch",
			title: "The Hobbit: Or There and Back Again", author: "J.R.R. Tolkien",
			releases: []string{
				"J.R.R. Tolkien - The Hobbit MP3",
				"J.R.R. Tolkien - The Hobbit - Or There and Back Again (Unabridged) MP3",
			},
			wantKept:    []string{"J.R.R. Tolkien - The Hobbit - Or There and Back Again (Unabridged) MP3"},
			wantDropped: map[string]string{"J.R.R. Tolkien - The Hobbit MP3": reasonMainTitleOnlyFullFound},
		},
		{
			name:  "a numbered subtitle and a release naming a different number",
			title: "Dune: Part Two", author: "Frank Herbert",
			releases:    []string{"Frank Herbert - Dune III (Unabridged) MP3"},
			wantDropped: map[string]string{"Frank Herbert - Dune III (Unabridged) MP3": reasonMainTitleOnlySequel},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := batchVerdicts(c.title, c.author, c.releases, c.bc)
			for rel, want := range c.wantDropped {
				if got[rel] != want {
					t.Errorf("release %q: verdict %q, want drop %q", rel, got[rel], want)
				}
			}
			for _, rel := range c.wantKept {
				if v, ok := got[rel]; !ok || v != "" {
					t.Errorf("release %q: verdict %q, want kept", rel, v)
				}
			}
		})
	}
}

// primaryNegatives are releases that must keep being approved after part 2.
var primaryNegatives = []struct {
	name          string
	title, author string
	releases      []string
	bc            bookContext
	spared        string
}{
	{"Hobbit alone", "The Hobbit: Or There and Back Again", "J.R.R. Tolkien",
		[]string{"J.R.R. Tolkien - The Hobbit MP3"}, bookContext{}, "no full title in the batch and nothing names another book"},
	{"Hobbit alone, known position 1", "The Hobbit: Or There and Back Again", "J.R.R. Tolkien",
		[]string{"J.R.R. Tolkien - The Hobbit (Unabridged) MP3"}, bookContext{seriesPosition: "1", otherTitles: []string{"The Fellowship of the Ring"}}, "position 1 is not a later entry"},
	{"Dune Part One alone", "Dune: Part One", "Frank Herbert",
		[]string{"Frank Herbert - Dune (Unabridged) MP3"}, bookContext{}, "series-like subtitle numbered one, main title only is the book"},
	{"Dune Part One with the full title elsewhere", "Dune: Part One", "Frank Herbert",
		[]string{"Frank Herbert - Dune (Unabridged) MP3", "Frank Herbert - Dune Part One (Unabridged) MP3"}, bookContext{otherTitles: []string{"Dune"}}, "series-like subtitle numbered one is exempt from the batch rule and the library rule"},
	{"Dune Book 1 alone", "Dune: Book 1", "Frank Herbert",
		[]string{"Frank Herbert - Dune retail epub"}, bookContext{}, "Book 1 subtitle"},
	{"roman numeral one subtitle", "Rocky: I", "Sylvester Stallone",
		[]string{"Sylvester Stallone - Rocky retail epub"}, bookContext{}, "roman numeral I"},
	{"Mistborn dash form", "Mistborn: The Final Empire", "Brandon Sanderson",
		[]string{"Brandon Sanderson - Mistborn - The Final Empire (Unabridged) MP3"}, bookContext{seriesPosition: "1"}, "full title"},
	{"Foundation subtitle only", "Foundation: Prelude to Foundation", "Isaac Asimov",
		[]string{"Isaac Asimov - Prelude to Foundation"}, bookContext{seriesPosition: "6", otherTitles: []string{"Foundation"}}, "the release carries the distinctive subtitle"},
	{"Foundation subtitle only, with a full title elsewhere", "Foundation: Prelude to Foundation", "Isaac Asimov",
		[]string{"Isaac Asimov - Prelude to Foundation", "Isaac Asimov - Foundation - Prelude to Foundation (Unabridged) MP3"}, bookContext{}, "the subtitle only release carries the subtitle"},
	{"subtitle first with the main title", "Sapiens: A Brief History of Humankind", "Yuval Noah Harari",
		[]string{"Yuval Noah Harari - Sapiens (Unabridged) MP3"}, bookContext{}, "no full title in the batch"},
	{"number equal to the later position", "Legion: Skin Deep", "Brandon Sanderson",
		[]string{"Brandon Sanderson - Legion 2 (2014) MP3"}, bookContext{seriesPosition: "2", otherTitles: []string{"Legion"}}, "the release carries the wanted number right after the main title"},
	{"full title with series tag", "High Druid of Shannara: Jarka Ruus", "Terry Brooks",
		[]string{"Terry Brooks - The High Druid of Shannara 01 - Jarka Ruus (Unabridged) MP3"}, bookContext{seriesPosition: "1"}, "full title with the series number in the middle"},
}

func TestPrimaryTitleFallbackKeepsOwnBook(t *testing.T) {
	for _, c := range primaryNegatives {
		t.Run(c.name, func(t *testing.T) {
			got := batchVerdicts(c.title, c.author, c.releases, c.bc)
			for _, rel := range c.releases {
				if v, ok := got[rel]; !ok || v != "" {
					t.Errorf("wanted %q: release %q dropped (%s); spared by: %s", c.title, rel, v, c.spared)
				}
			}
		})
	}
}

// TestPrimaryTitleFallbackLoneMainTitleIsAccepted pins the known limit: with
// no position, no library title to compare and no full title in the batch,
// a release that names only the main title is still approved. The filter
// rejects on positive evidence only.
func TestPrimaryTitleFallbackLoneMainTitleIsAccepted(t *testing.T) {
	got := batchVerdicts("Legion: Skin Deep", "Brandon Sanderson",
		[]string{"Brandon Sanderson - Legion (2012) MP3"}, bookContext{})
	if v := got["Brandon Sanderson - Legion (2012) MP3"]; v != "" {
		t.Errorf("a lone main title release with no context should stay approved, got %q", v)
	}
}

func TestSeriesLikeSubtitle(t *testing.T) {
	cases := []struct {
		in string
		n  int
		ok bool
	}{
		{"Part One", 1, true}, {"Part 2", 2, true}, {"Book 3", 3, true}, {"Volume 4", 4, true},
		{"Episode 5", 5, true}, {"Vol. 6", 6, true}, {"II", 2, true}, {"IV", 4, true}, {"3", 3, true},
		{"Book Twelve", 12, true}, {"I", 1, true},
		{"Skin Deep", 0, false}, {"Mix", 0, false}, {"The Final Empire", 0, false}, {"Two", 0, false},
		{"Part of the Plan", 0, false}, {"", 0, false}, {"Book 1 of 2", 0, false},
	}
	for _, c := range cases {
		n, ok := seriesLikeSubtitle(c.in)
		if n != c.n || ok != c.ok {
			t.Errorf("seriesLikeSubtitle(%q) = %d,%v want %d,%v", c.in, n, ok, c.n, c.ok)
		}
	}
}
