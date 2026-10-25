package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// TestNumberWordTitleMatchesDigitRelease pins #3476: a title that spells its
// position out is matched by a release that uses the digit, and the other way
// round, while a different position never matches.
func TestNumberWordTitleMatchesDigitRelease(t *testing.T) {
	const author = "OstensibleMammal"
	cases := []struct {
		title, release string
		keep           bool
	}{
		{"Path of the Deathless Book One: A LitRPG Progression Epic", "OstensibleMammal - Path of the Deathless Book 1 [M4B]", true},
		{"Path of the Deathless Book One: A LitRPG Progression Epic", "Path.of.the.Deathless.Book.1.AUDIOBOOK-GRP", true},
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book One (m4b)", true},
		{"Path of the Deathless Book 1", "OstensibleMammal - Path of the Deathless Book One (m4b)", true},
		{"Path of the Deathless Book Two", "OstensibleMammal - Path of the Deathless Book 2 (m4b)", true},
		{"Path of the Deathless Book Ten", "OstensibleMammal - Path of the Deathless Book 10 (m4b)", true},
		// A different work or position must not get through.
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book 10 (m4b)", false},
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book 11 (m4b)", false},
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book 2 (m4b)", false},
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book Two (m4b)", false},
		{"Path of the Deathless Book One", "OstensibleMammal - Path of the Deathless Book 21 (m4b)", false},
		{"Path of the Deathless Book Two", "OstensibleMammal - Path of the Deathless Book 12 (m4b)", false},
	}
	for _, tc := range cases {
		kept, reason := relevanceVerdict(tc.title, author, tc.release, bookContext{})
		if kept != tc.keep {
			t.Errorf("%q vs %q: kept=%v (%s), want %v", tc.title, tc.release, kept, reason, tc.keep)
		}
	}
}

// TestNumberWordTitleFragmentParity holds the number word reading to the digit
// reading on every release shape the fragment rules judge: a "Book One" title
// must treat "Book 1 of 2", a lone part, and a whole set exactly as "Book 1"
// does. The fold must not let through anything the digit title would drop.
func TestNumberWordTitleFragmentParity(t *testing.T) {
	releases := []string{
		"OstensibleMammal - Path of the Deathless Book 1 of 2 MP3",
		"OstensibleMammal - Path of the Deathless Book 1 Part 2 of 5 MP3",
		"OstensibleMammal - Path of the Deathless Book 1 Ch12 of 71 MP3",
		"OstensibleMammal - Path of the Deathless Book 1 Complete M4B",
		"OstensibleMammal - Path of the Deathless Book 1-3 Box Set",
		"OstensibleMammal - Path of the Deathless Books 1-3",
		"OstensibleMammal - Path of the Deathless Book 1 EPUB",
		"OstensibleMammal - Path of the Deathless Book 1 - Sequel Title",
		"OstensibleMammal - Path of the Deathless Book 10 M4B",
		"OstensibleMammal - Path of the Deathless Book 12 M4B",
		"OstensibleMammal - Path of the Deathless Book 100 M4B",
	}
	for _, rel := range releases {
		wantKept, wantReason := relevanceVerdict("Path of the Deathless Book 1", "OstensibleMammal", rel, bookContext{})
		gotKept, gotReason := relevanceVerdict("Path of the Deathless Book One", "OstensibleMammal", rel, bookContext{})
		if gotKept != wantKept || gotReason != wantReason {
			t.Errorf("%q: Book One kept=%v (%s), Book 1 kept=%v (%s)", rel, gotKept, gotReason, wantKept, wantReason)
		}
	}
}

// TestNumberWordFoldReturnsOriginalRelease checks the kept and dropped lists
// carry the release as the indexer named it, not the folded copy the judgement
// read.
func TestNumberWordFoldReturnsOriginalRelease(t *testing.T) {
	in := []newznab.SearchResult{
		{Title: "OstensibleMammal - Path of the Deathless Book One (m4b)", GUID: "a"},
		{Title: "OstensibleMammal - Path of the Deathless Book Two (m4b)", GUID: "b"},
	}
	kept, dropped := filterRelevantKeepDropped(in, "Path of the Deathless Book 1", "OstensibleMammal", nil, bookContext{})
	if len(kept) != 1 || kept[0].Title != in[0].Title {
		t.Fatalf("kept = %+v", kept)
	}
	if len(dropped) != 1 || dropped[0].Result.Title != in[1].Title {
		t.Fatalf("dropped = %+v", dropped)
	}
}

// TestSearchBookWithDebugReportsNumberWordQuery keeps the added query visible
// (#3455): the indexer row names the digit query and how many results it
// returned, and a search that found its book reports nothing.
func TestSearchBookWithDebugReportsNumberWordQuery(t *testing.T) {
	const release = "OstensibleMammal - Path of the Deathless Book 1 [M4B]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		term := q.Get("q") + q.Get("title")
		if !strings.Contains(term, "Book 1") {
			w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="1"/><item><title>` + release + `</title><guid>g1</guid><enclosure url="https://fake/dl/1" length="1000" type="application/x-bittorrent"/></item></channel></rss>`))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 1, Name: "strict", URL: srv.URL, Enabled: true, Categories: []int{3030}}}

	results, dbg := newTestSearcher().SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
		Title: "Path of the Deathless Book One: A LitRPG Progression Epic", Author: "OstensibleMammal", MediaType: models.MediaTypeAudiobook,
	})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (filters %+v)", len(results), dbg.Filters)
	}
	if len(dbg.Indexers) != 1 || dbg.Indexers[0].NumberWordQuery != "Path of the Deathless Book 1" || dbg.Indexers[0].NumberWordResults != 1 {
		t.Errorf("indexer debug = %+v", dbg.Indexers)
	}

	_, dbg = newTestSearcher().SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
		Title: "Path of the Deathless Book 1", Author: "OstensibleMammal", MediaType: models.MediaTypeAudiobook,
	})
	if dbg.Indexers[0].NumberWordQuery != "" {
		t.Errorf("a digit title sent the variant: %+v", dbg.Indexers[0])
	}
}

// A compound number written with dots or underscores is book 21, not 20, and
// must not be read as the position of "Hunt Book 20".
func TestNumberWordCompoundReleaseNotKeptAsBook20(t *testing.T) {
	for _, rel := range []string{
		"Author - Hunt Book.Twenty.One (m4b)",
		"Author - Hunt Book_Twenty_One (m4b)",
		"Author - Hunt Book Twenty One (m4b)",
		"Author - Hunt Book Twenty-One (m4b)",
	} {
		for _, title := range []string{"Hunt Book 20", "Hunt Book Twenty"} {
			if kept, _ := relevanceVerdict(title, "Author", rel, bookContext{}); kept {
				t.Errorf("%q kept for %q", rel, title)
			}
		}
	}
}
