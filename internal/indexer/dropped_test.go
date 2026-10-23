package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// droppedFeedRSS has one release of each fate: kept, dropped by the relevance
// filter (the grabbable class), dropped as non-book content, and dropped as a
// usenet fragment. The last two are not books and must never be offered.
const droppedFeedRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Frank Herbert - Dune - retail epub</title>
      <guid isPermaLink="false">kept-1</guid>
      <enclosure url="https://fake/dl/kept?apikey=SECRET" length="1000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Totally Unrelated Cookbook 2020 epub</title>
      <guid isPermaLink="false">rel-1</guid>
      <enclosure url="https://fake/dl/rel?apikey=SECRET" length="2000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Frank Herbert Dune 2021 1080p WEB-DL x264-GROUP</title>
      <guid isPermaLink="false">video-1</guid>
      <enclosure url="https://fake/dl/video" length="3000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Frank Herbert - Dune Ch63 of 71.mp3</title>
      <guid isPermaLink="false">junk-1</guid>
      <enclosure url="https://fake/dl/junk" length="4000" type="application/x-nzb"/>
    </item>
  </channel>
</rss>`

func droppedFeedSearch(t *testing.T) ([]newznab.SearchResult, *SearchDebug) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(droppedFeedRSS))
	}))
	t.Cleanup(srv.Close)
	idxs := []models.Indexer{{ID: 7, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	return newTestSearcher().SearchBookWithDebug(context.Background(), idxs,
		MatchCriteria{Title: "Dune", Author: "Frank Herbert"})
}

// A release the relevance filter removes comes back whole in Dropped, GUID and
// download URL intact, so the interactive search can grab it (#2334). Only the
// relevance drop is there: non-book and usenet junk drops are not books.
func TestSearchBookWithDebug_DroppedCarriesRelevanceReleases(t *testing.T) {
	results, dbg := droppedFeedSearch(t)

	if len(dbg.Dropped) != 1 {
		t.Fatalf("Dropped = %+v, want exactly the relevance drop", dbg.Dropped)
	}
	d := dbg.Dropped[0]
	if d.Stage != StageRelevance || d.Reason == "" {
		t.Errorf("stage/reason = %q/%q, want relevance with a reason", d.Stage, d.Reason)
	}
	if d.Result.GUID != "rel-1" {
		t.Errorf("GUID = %q, want rel-1", d.Result.GUID)
	}
	if !strings.Contains(d.Result.NZBURL, "https://fake/dl/rel") || !strings.Contains(d.Result.NZBURL, "apikey=SECRET") {
		t.Errorf("NZBURL = %q, want the raw download URL kept for the registry", d.Result.NZBURL)
	}
	if d.Result.IndexerID != 7 || d.Result.Protocol != "usenet" {
		t.Errorf("indexer id/protocol = %d/%q, want 7/usenet", d.Result.IndexerID, d.Result.Protocol)
	}

	// The side field never leaks into the main slice auto-grab reads.
	for _, r := range results {
		if r.GUID != "kept-1" {
			t.Errorf("main results hold %q, want only kept-1", r.GUID)
		}
	}
}

// Non-book drops now show in the Filter decisions panel (#2334) but are not
// offered for a grab, and neither is a usenet fragment.
func TestSearchBookWithDebug_NonBookDropIsReportedNotGrabbable(t *testing.T) {
	_, dbg := droppedFeedSearch(t)

	var sawNonBook, sawJunk bool
	for _, f := range dbg.Filters {
		switch f.Stage {
		case StageNonBook:
			sawNonBook = true
			if !strings.Contains(f.Title, "1080p") || f.IndexerName != "test" || f.Reason == "" {
				t.Errorf("non-book entry = %+v", f)
			}
		case StageUsenetJunk:
			sawJunk = true
		}
	}
	if !sawNonBook {
		t.Errorf("no non-book FilterDebug entry: %+v", dbg.Filters)
	}
	if !sawJunk {
		t.Errorf("no usenet-junk FilterDebug entry: %+v", dbg.Filters)
	}
	for _, d := range dbg.Dropped {
		if d.Stage == StageNonBook || d.Stage == StageUsenetJunk || d.Result.GUID == "video-1" || d.Result.GUID == "junk-1" {
			t.Errorf("a non-book drop is offered for a grab: %+v", d)
		}
	}
}

// The scheduler's pipeline and the interactive one keep the same releases, and
// the projection auto-grab reads (SearchBookWithOutcomes) never holds a dropped
// release.
func TestSearchBookWithOutcomes_NeverReturnsDroppedReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(droppedFeedRSS))
	}))
	defer srv.Close()
	idxs := []models.Indexer{{ID: 7, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert"}

	for name, got := range map[string][]newznab.SearchResult{
		"SearchBook": newTestSearcher().SearchBook(context.Background(), idxs, crit),
		"SearchBookWithOutcomes": func() []newznab.SearchResult {
			r, _ := newTestSearcher().SearchBookWithOutcomes(context.Background(), idxs, crit)
			return r
		}(),
	} {
		if len(got) != 1 || got[0].GUID != "kept-1" {
			t.Errorf("%s returned %d results %+v, want only kept-1", name, len(got), resultTitles(got))
		}
	}
}

// A non-book drop is reported by the shared implementation, so SearchBook's
// view of it cannot drift from the debug one.
func TestFilterNonBookContentDetailed_AgreesWithPlain(t *testing.T) {
	in := []newznab.SearchResult{
		{GUID: "a", Title: "Dune epub"},
		{GUID: "b", Title: "Dune 1080p BluRay"},
		{GUID: "c", Title: "Dune", Category: "2040"},
	}
	kept, dropped := filterNonBookContentDetailed(in)
	plain := filterNonBookContent(in)
	if len(kept) != 1 || len(plain) != 1 || kept[0].GUID != plain[0].GUID {
		t.Fatalf("kept %v plain %v", kept, plain)
	}
	if len(dropped) != 2 || dropped[0].Stage != StageNonBook || !strings.Contains(dropped[1].Reason, "2040") {
		t.Errorf("dropped = %+v", dropped)
	}
}

func TestSplitByAllowedLanguages_ReturnsDropped(t *testing.T) {
	in := []newznab.SearchResult{
		{GUID: "en", Title: "Dune ENGLISH epub"},
		{GUID: "de", Title: "Dune GERMAN epub"},
	}
	kept, dropped := SplitByAllowedLanguages(in, []string{"eng"})
	if len(kept) != 1 || kept[0].GUID != "en" || len(dropped) != 1 || dropped[0].GUID != "de" {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
	plain := FilterByAllowedLanguages(in, []string{"eng"})
	if len(plain) != 1 || plain[0].GUID != "en" {
		t.Errorf("FilterByAllowedLanguages = %v", plain)
	}
}

// Dropped carries raw download URLs, so it must never be serialized into the
// debug panel payload or GET /search/last-debug.
func TestSearchDebug_DroppedIsNotSerialized(t *testing.T) {
	_, dbg := droppedFeedSearch(t)
	if len(dbg.Dropped) == 0 {
		t.Fatal("expected a dropped release")
	}
	b, err := json.Marshal(dbg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "rel-1") || strings.Contains(string(b), "nzbUrl") {
		t.Errorf("the debug payload carries a dropped release: %s", b)
	}
}
