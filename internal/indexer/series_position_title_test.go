package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// A book stored as "Series N: Title" was only ever matched on the whole name or
// on the series half, so a release naming the book itself was dropped. This
// release is the one from the Discord report.
func TestFilterRelevant_SeriesPositionTitle(t *testing.T) {
	const title, author = "Feral Mage 4: The Mining Company Contract", "Chase Kilgore"
	cases := []struct {
		release string
		want    bool
	}{
		{"The Mining Company Contract by Chase Kilgore [ENG / EPUB] [VIP]", true},
		{"Chase Kilgore - The Mining Company Contract (epub)", true},
		// Already matched before this change, and still does.
		{"Chase Kilgore - Feral Mage 4 - The Mining Company Contract (epub)", true},
		// The author is still required.
		{"The Mining Company Contract by Someone Else [ENG / EPUB]", false},
		{"The Mining Company Contract [ENG / EPUB]", true},
		// A different book by the same author.
		{"The Silver Company Contract by Chase Kilgore [ENG / EPUB]", false},
		// Words present but split by words the title does not have.
		{"The Mining Company Breach of Contract by Chase Kilgore", false},
		{"Wulfe Untamed: Feral Warriors, Book 8 by Pamela Palmer [ENG / MP3]", false},
	}
	for _, f := range relevanceFilters {
		for _, c := range cases {
			t.Run(f.name+"/"+c.release, func(t *testing.T) {
				got := f.fn([]newznab.SearchResult{{Title: c.release}}, title, author, nil)
				if (len(got) == 1) != c.want {
					t.Fatalf("%s kept=%v, want %v for %q", f.name, len(got) == 1, c.want, c.release)
				}
			})
		}
	}
}

// A one word book title after a series position is below the floor, so the
// fallback reading does not open up every release by the author that contains
// that word.
func TestFilterRelevant_SeriesPositionTitleNeedsTwoWords(t *testing.T) {
	got := filterRelevant([]newznab.SearchResult{{Title: "Wintersteel Anthology by Will Wight"}},
		"Cradle Book 8: Wintersteel", "Will Wight", nil)
	for _, r := range got {
		t.Errorf("kept %q through a one word series position title", r.Title)
	}
}

// Drives the production entry points so the reading is proven on the paths
// that search and grab, not only on the filter function.
func TestSeriesPositionTitle_ReachesProductionEntrypoints(t *testing.T) {
	const right = "The Mining Company Contract by Chase Kilgore [ENG / EPUB] [VIP]"
	const wrong = "Wulfe Untamed: Feral Warriors, Book 8 by Pamela Palmer [ENG / MP3]"
	feed := feedOf(right, wrong)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Feral Mage 4: The Mining Company Contract", Author: "Chase Kilgore"}
	ctx := context.Background()
	debugged, _ := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	outcomes, _ := newTestSearcher().SearchBookWithOutcomes(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBookWithDebug":    resultTitles(debugged),
		"SearchBookWithOutcomes": resultTitles(outcomes),
	} {
		if len(got) != 1 || got[0] != right {
			t.Errorf("%s kept %v, want only %q", name, got, right)
		}
	}
}

// An unnumbered first book can carry its series as the main title and its
// distinguishing book title as a subtitle. A release named only for the
// series plus a later volume used to pass the primary-title fallback and win
// search-on-add on format quality alone (#4076).
func TestSearchBook_UnnumberedFirstVolumeRejectsNumberedSequels(t *testing.T) {
	const title = "Mage Tank: A LitRPG Adventure"
	const author = "Cornman"
	const exact = "Mage Tank: A LitRPG Adventure by Cornman [ENG / M4B] [VIP]"
	feed := feedOf(exact,
		"Mage Tank 2 by Cornman [ENG / M4B] [VIP]",
		"Mage Tank 3 by Cornman [ENG / M4B] [VIP]",
		"Mage Tank 4 by Cornman [ENG / M4B] [VIP]",
		"Mage Tank 5 by Cornman [ENG / M4B] [VIP]",
		"Mage Tank Book 3 by Cornman [ENG / M4B] [VIP]",
		"Mage Tank Vol. 4 by Cornman [ENG / M4B] [VIP]",
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	crit := MatchCriteria{Title: title, Author: author, MediaType: models.MediaTypeAudiobook, SeriesTitle: "Mage Tank", SeriesPosition: "1"}
	ctx := context.Background()
	results := newTestSearcher().SearchBook(ctx, idxs, crit)
	debugged, _ := newTestSearcher().SearchBookWithDebug(ctx, idxs, crit)
	outcomes, _ := newTestSearcher().SearchBookWithOutcomes(ctx, idxs, crit)
	for name, got := range map[string][]string{
		"SearchBook":             resultTitles(results),
		"SearchBookWithDebug":    resultTitles(debugged),
		"SearchBookWithOutcomes": resultTitles(outcomes),
	} {
		if len(got) != 1 || got[0] != exact {
			t.Errorf("%s returned %v, want only the exact book-1 release %q", name, got, exact)
		}
	}
}

func TestFilterRelevant_UnnumberedFirstVolumeKeepsSubtitleAndFormatVariants(t *testing.T) {
	const title = "Mage Tank: A LitRPG Adventure"
	const author = "Cornman"
	for _, release := range []string{
		"Mage Tank: A LitRPG Adventure by Cornman [ENG / M4B] [VIP]",
		"Mage Tank - A LitRPG Adventure - Cornman [Unabridged M4B]",
		"Mage Tank Book 1: A LitRPG Adventure by Cornman [ENG / M4B]",
		"Mage Tank Vol. 1: A LitRPG Adventure by Cornman [ENG / M4B]",
	} {
		got, _ := filterRelevantWithContext(toResults(release), title, author, nil, bookContext{})
		if len(got) != 1 {
			t.Errorf("valid subtitle/format variant %q was rejected", release)
		}
	}
}
