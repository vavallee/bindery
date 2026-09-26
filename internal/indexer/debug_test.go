package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestSearchBookWithDebugRanksByProfile: the interactive search path ranks by
// the profile riding on MatchCriteria, the same way SearchBook does (#2733).
// The book page is where users see the order, so this is the path they will
// judge the profile editor by.
func TestSearchBookWithDebugRanksByProfile(t *testing.T) {
	const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="2"/>
    <item>
      <title>Life Ascending Nick Lane epub</title>
      <guid isPermaLink="false">guid-epub</guid>
      <enclosure url="https://fake/dl/1" length="1000" type="application/x-nzb"/>
    </item>
    <item>
      <title>Life Ascending Nick Lane pdf</title>
      <guid isPermaLink="false">guid-pdf</guid>
      <enclosure url="https://fake/dl/2" length="1000" type="application/x-nzb"/>
    </item>
  </channel>
</rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	results, _ := newTestSearcher().SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
		Title:     "Life Ascending",
		Author:    "Nick Lane",
		MediaType: models.MediaTypeEbook,
		Profile: &models.QualityProfile{Name: "pdf first", Items: []models.QualityItem{
			{Quality: "pdf", Allowed: true},
			{Quality: "epub", Allowed: true},
		}},
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].GUID != "guid-pdf" {
		t.Errorf("profile puts pdf first, got order: %v", resultTitles(results))
	}
}
