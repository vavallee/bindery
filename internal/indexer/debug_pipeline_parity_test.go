package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// videoFeedRSS mixes legitimate book releases with the #1591 population: video
// releases sharing title words, and a result the indexer itself filed under a
// non-book category.
const videoFeedRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="5"/>
    <item>
      <title>Frank Herbert - Dune - retail epub</title>
      <guid isPermaLink="false">g1</guid>
      <enclosure url="https://fake/dl/1" length="1000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Frank Herbert Dune 2021 1080p WEB-DL x264-GROUP</title>
      <guid isPermaLink="false">g2</guid>
      <enclosure url="https://fake/dl/2" length="1000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Frank Herbert Dune S01E02 720p HDTV x264</title>
      <guid isPermaLink="false">g3</guid>
      <enclosure url="https://fake/dl/3" length="1000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Frank Herbert - Dune (Unabridged) m4b</title>
      <guid isPermaLink="false">g4</guid>
      <enclosure url="https://fake/dl/4" length="1000" type="application/x-nzb"/>
    </item>
  </channel>
</rss>`

// TestSearchPipelinesAgreeOnNonBookContent is the regression test for #1644.
//
// SearchBookWithDebug omitted filterNonBookContent, and it is the ONLY searcher
// entrypoint the API uses (internal/api/indexers.go calls it for single-format
// searches and for both legs of a media_type=both fan-out), while the scheduler
// uses SearchBook. So the #1591 video guard applied to automated grabs but not
// to interactive search, and the UI surfaced 1080p/x264/WEB-DL releases the
// grab path would have dropped.
//
// This drives BOTH REAL entrypoints against the same fake indexer rather than
// re-implementing their stage lists, so a future divergence in either pipeline
// fails here.
//
// The video entries deliberately name the author as well as the title, so they
// SURVIVE relevance filtering and only filterNonBookContent can remove them.
// That is the #1591 population ("movie/TV releases sharing a few title words"),
// and without it the parity assertion passes vacuously because relevance
// happens to drop the junk anyway.
func TestSearchPipelinesAgreeOnNonBookContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(videoFeedRSS))
	}))
	defer srv.Close()

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert"}

	plain := newTestSearcher().SearchBook(context.Background(), idxs, crit)
	debugged, dbg := newTestSearcher().SearchBookWithDebug(context.Background(), idxs, crit)

	plainTitles := resultTitles(plain)
	debugTitles := resultTitles(debugged)

	if len(plainTitles) != len(debugTitles) {
		t.Fatalf("pipelines disagree:\n  SearchBook          kept %d %v\n  SearchBookWithDebug kept %d %v",
			len(plainTitles), plainTitles, len(debugTitles), debugTitles)
	}
	for i := range plainTitles {
		if plainTitles[i] != debugTitles[i] {
			t.Errorf("pipelines disagree at %d: %q vs %q", i, plainTitles[i], debugTitles[i])
		}
	}

	// Guard against both pipelines being equally broken: the video releases must
	// actually be gone, and the book releases must actually survive.
	for _, title := range plainTitles {
		if videoMarkerRe.MatchString(title) {
			t.Errorf("video release survived filtering: %q", title)
		}
	}
	if len(plainTitles) == 0 {
		t.Fatal("expected the legitimate book releases to survive")
	}

	// The debug counter must be populated, since it is what makes the stage
	// visible in the UI's search-details panel.
	if dbg == nil {
		t.Fatal("expected non-nil debug info")
	}
	if dbg.Pipeline.AfterNonBookContent != len(debugTitles) {
		t.Errorf("AfterNonBookContent = %d, want %d (the count entering relevance)",
			dbg.Pipeline.AfterNonBookContent, len(debugTitles))
	}
	if dbg.Pipeline.AfterNonBookContent >= dbg.Pipeline.AfterUsenetJunk {
		t.Errorf("expected the non-book stage to drop results: afterUsenetJunk=%d afterNonBookContent=%d",
			dbg.Pipeline.AfterUsenetJunk, dbg.Pipeline.AfterNonBookContent)
	}
}
