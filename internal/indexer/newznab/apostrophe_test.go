package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// apostropheAverseIndexer answers like the indexers in #3895: any query that
// contains an apostrophe returns nothing, the same words without one find the
// release.
func apostropheAverseIndexer(t *testing.T, release string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		term := q.Get("q")
		if term == "" {
			term = q.Get("title")
		}
		mu.Lock()
		queries = append(queries, term)
		mu.Unlock()
		if strings.Contains(term, "'") {
			w.Write([]byte(emptyFeed))
			return
		}
		for _, word := range strings.Fields(strings.ToLower(term)) {
			if !strings.Contains(strings.ToLower(release), word) {
				w.Write([]byte(emptyFeed))
				return
			}
		}
		w.Write([]byte(feedWith(release)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), queries...) }
}

func TestApostropheQueryVariants(t *testing.T) {
	cases := map[string][]string{
		"Carl’s Doomsday Scenario": {"Carls Doomsday Scenario", "Carl Doomsday Scenario"},
		"L'Outsider":               {"LOutsider", "L Outsider"},
		"Rock 'n' Roll":            {"Rock n Roll"},
		"Dune":                     nil,
		"Ender's Game: Extra":      {"Enders Game", "Ender Game"},
	}
	for in, want := range cases {
		got := apostropheQueryVariants(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("apostropheQueryVariants(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBookSearch_ApostropheVariant pins #3895: every cascade tier keeps the
// apostrophe and finds nothing, then the removed spelling finds the release.
func TestBookSearch_ApostropheVariant(t *testing.T) {
	srv, queries := apostropheAverseIndexer(t, "Matt Dinniman - Carls Doomsday Scenario (Audiobook)")
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(), "Carl’s Doomsday Scenario", "Matt Dinniman", []int{3030})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1; queries %q", len(res), queries())
	}
	if len(trace.ApostropheQueries) != 1 || trace.ApostropheQueries[0] != "Carls Doomsday Scenario" || trace.ApostropheResults != 1 {
		t.Errorf("trace = %+v", trace)
	}
}

// The space spelling is tried when removal finds nothing ("Carl Doomsday
// Scenario" is how some trackers name it), and nothing extra is sent when the
// cascade already found the book or the title has no apostrophe.
func TestBookSearch_ApostropheVariantSpaceAndCost(t *testing.T) {
	srv, queries := apostropheAverseIndexer(t, "Carl Doomsday Scenario epub")
	c := testNew(srv.URL, "k")
	res, trace, _ := c.BookSearchTraced(context.Background(), "Carl's Doomsday Scenario", "Matt Dinniman", []int{7020})
	if len(res) != 1 || len(trace.ApostropheQueries) != 2 {
		t.Errorf("space spelling: results %d trace %+v queries %q", len(res), trace, queries())
	}

	base := len(queries())
	_, trace, _ = c.BookSearchTraced(context.Background(), "Carls Doomsday Scenario", "Matt Dinniman", []int{7020})
	if len(trace.ApostropheQueries) != 0 {
		t.Errorf("variant sent for a title without an apostrophe: %+v", trace)
	}
	if len(queries()) == base {
		t.Error("expected the cascade to have queried")
	}
}
