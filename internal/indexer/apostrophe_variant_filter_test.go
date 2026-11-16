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

// TestFilterRelevant_ApostropheVariantResults pins that releases found through
// the apostrophe free query variants (#3895) survive the relevance filter for
// the apostrophe title, whichever way the release spells it.
func TestFilterRelevant_ApostropheVariantResults(t *testing.T) {
	for _, name := range []string{
		"Matt Dinniman - Carls Doomsday Scenario (Audiobook)",
		"Matt Dinniman - Carl Doomsday Scenario (Audiobook)",
		"Matt Dinniman - Carl s Doomsday Scenario (Audiobook)",
		"Matt Dinniman - [Dungeon Crawler Carl 02] - Carl's Doomsday Scenario (retail) (epub)",
	} {
		res := []newznab.SearchResult{{Title: name, GUID: name}}
		got, _ := filterRelevantDetailed(res, "Carl’s Doomsday Scenario", "Matt Dinniman", nil)
		if len(got) != 1 {
			t.Errorf("%q dropped by the relevance filter", name)
		}
	}
}

// apostropheAverse serves one release to queries without an apostrophe and
// nothing to queries with one, or a 429 to the former when limited is set.
func apostropheAverse(t *testing.T, release string, limited bool) *httptest.Server {
	t.Helper()
	const empty = `<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		term := q.Get("q") + q.Get("title")
		if strings.Contains(term, "'") {
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(empty))
			return
		}
		if limited {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="1"/><item><title>` + release + `</title><guid>g1</guid><enclosure url="https://fake/dl/1" length="1000" type="application/x-bittorrent"/></item></channel></rss>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSearchBookWithDebugReportsApostropheQueries keeps the extra queries
// visible in the debug view's indexer row, as the number word query is.
func TestSearchBookWithDebugReportsApostropheQueries(t *testing.T) {
	srv := apostropheAverse(t, "Matt Dinniman - Carls Doomsday Scenario [M4B]", false)
	idxs := []models.Indexer{{ID: 1, Name: "averse", URL: srv.URL, Enabled: true, Categories: []int{3030}}}
	results, dbg := newTestSearcher().SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
		Title: "Carl's Doomsday Scenario", Author: "Matt Dinniman", MediaType: models.MediaTypeAudiobook,
	})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (filters %+v)", len(results), dbg.Filters)
	}
	row := dbg.Indexers[0]
	if len(row.ApostropheQueries) != 1 || row.ApostropheQueries[0] != "Carls Doomsday Scenario" || row.ApostropheResults != 1 {
		t.Errorf("indexer debug = %+v", row)
	}
	raw, err := json.Marshal(row)
	if err != nil || !strings.Contains(string(raw), `"apostropheQueries":["Carls Doomsday Scenario"],"apostropheResults":1`) {
		t.Errorf("json = %s, err %v", raw, err)
	}
}

// TestSearchBook_ApostropheVariantRateLimitSetsCooldown pins that a 429 on a
// variant reaches the cooldown, in both search paths, instead of being read as
// a search that succeeded with nothing.
func TestSearchBook_ApostropheVariantRateLimitSetsCooldown(t *testing.T) {
	srv := apostropheAverse(t, "unused", true)
	idx := models.Indexer{ID: 7, Name: "averse", URL: srv.URL, Enabled: true, Categories: []int{3030}}
	crit := MatchCriteria{Title: "Carl's Doomsday Scenario", Author: "Matt Dinniman", MediaType: models.MediaTypeAudiobook}

	s := newTestSearcher()
	s.SearchBook(context.Background(), []models.Indexer{idx}, crit)
	if _, held := s.cooldownActive(idx); !held {
		t.Error("SearchBook: no cooldown after a rate limited apostrophe variant")
	}

	s = newTestSearcher()
	_, dbg := s.SearchBookWithDebug(context.Background(), []models.Indexer{idx}, crit)
	if _, held := s.cooldownActive(idx); !held {
		t.Error("SearchBookWithDebug: no cooldown after a rate limited apostrophe variant")
	}
	if row := dbg.Indexers[0]; row.Error == "" || row.ApostropheError == "" || len(row.ApostropheQueries) != 1 {
		t.Errorf("indexer debug = %+v", row)
	}
}
