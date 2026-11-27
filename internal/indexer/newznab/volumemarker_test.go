package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// spelledOutIndexer answers like MyAnonamouse in #4176: every query word must
// appear in the release name, and the release spells the marker "Volume".
func spelledOutIndexer(t *testing.T) (*httptest.Server, func() []string) {
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
		release := "Under the Oak Tree: Volume 3 by Suji Kim [ENG / M4B]"
		have := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(release), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			have[w] = true
		}
		// Whole words only, as MyAnonamouse does: "vol" is not "volume".
		for _, word := range strings.FieldsFunc(strings.ToLower(term), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if !have[word] {
				w.Write([]byte(emptyFeed))
				return
			}
		}
		w.Write([]byte(feedWith(release)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), queries...) }
}

func TestSpellOutVolumeMarkers(t *testing.T) {
	cases := map[string]string{
		"Under the Oak Tree, Vol. 1":  "Under the Oak Tree, Volume 1",
		"Under the Oak Tree , Vol. 4": "Under the Oak Tree , Volume 4",
		"Tree Vol 2":                  "Tree Volume 2",
		"Tree vol.3":                  "Tree Volume 3",
		"Tree VOL. 12":                "Tree Volume 12",
		"Saga Bk 2":                   "Saga Book 2",
		"Saga Bk. 2":                  "Saga Book 2",
		"Saga Pt 3":                   "Saga Part 3",
		"Saga Pt. 3":                  "Saga Part 3",
		"Saga Vol. 1 and Pt 2":        "Saga Volume 1 and Part 2",
		// No change: no marker, already spelled out, or a word that merely
		// starts or ends with the letters.
		"Dune":           "",
		"Tree Volume 1":  "",
		"Tree Volumes 1": "",
		"Volcano 1":      "",
		"Volcano Vol":    "",
		"Volk 2":         "",
		"Revol 2":        "",
		"Vol. One":       "",
		"Vol":            "",
		"Chapter 2":      "",
		"Concept 2":      "",
		"Kept 3":         "",
		"Pt":             "",
		"Pt Cruiser":     "",
		"Bk Bank":        "",
		"Bookkeeper 2":   "",
		"Rapt 4":         "",
		"Hubbub 2":       "",
		"Tree Vol. X":    "",
	}
	for in, want := range cases {
		if got := spellOutVolumeMarkers(in); got != want {
			t.Errorf("spellOutVolumeMarkers(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBookSearch_VolumeMarkerVariant pins #4176 on the wire: every tier keeps
// "Vol." and finds nothing, then exactly one spelled out query finds it.
func TestBookSearch_VolumeMarkerVariant(t *testing.T) {
	srv, queries := spelledOutIndexer(t)
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(),
		"Under the Oak Tree (Novel), Vol. 3", "Suji Kim", []int{7020})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1; queries %q", len(res), queries())
	}
	if trace.VolumeMarkerQuery != "Under the Oak Tree, Volume 3" || trace.VolumeMarkerResults != 1 {
		t.Errorf("trace = %+v", trace)
	}
	qs := queries()
	n := 0
	for _, q := range qs {
		if strings.Contains(q, "Volume 3") {
			n++
		}
	}
	if n != 1 || !strings.Contains(qs[len(qs)-1], "Volume 3") {
		t.Errorf("want exactly one spelled out query, sent last; got %q", qs)
	}
}

// The extra query is only for searches that failed and titles that need it.
func TestBookSearch_VolumeMarkerVariantCost(t *testing.T) {
	srv, queries := spelledOutIndexer(t)
	c := testNew(srv.URL, "k")

	// Already spelled out: the cascade finds it, nothing extra.
	res, trace, _ := c.BookSearchTraced(context.Background(), "Under the Oak Tree, Volume 3", "Suji Kim", []int{7020})
	if len(res) != 1 || trace.VolumeMarkerQuery != "" {
		t.Errorf("spelled out title: results %d trace %+v", len(res), trace)
	}
	// No marker at all.
	_, trace, _ = c.BookSearchTraced(context.Background(), "Volcano Nights", "Nobody", []int{7020})
	if trace.VolumeMarkerQuery != "" {
		t.Errorf("variant sent for a title without a marker: %+v", trace)
	}
	// Unfindable abbreviated title: one query beyond the cascade, no more.
	base := len(queries())
	_, trace, _ = c.BookSearchTraced(context.Background(), "Missing Saga Vol. 9", "Nobody", []int{7020})
	if trace.VolumeMarkerQuery != "Missing Saga Volume 9" {
		t.Errorf("trace = %+v", trace)
	}
	var cascade int
	for _, q := range queries()[base:] {
		if !strings.Contains(q, "Volume") {
			cascade++
		}
	}
	if got := len(queries()) - base; got != cascade+1 {
		t.Errorf("sent %d queries, want the cascade's %d plus one", got, cascade)
	}
}

// The spelled out query is a normal query: the daily cap refuses it.
func TestBookSearch_VolumeMarkerVariantBudget(t *testing.T) {
	// Count the cascade first so the cap lands exactly after it.
	srv0, q0 := spelledOutIndexer(t)
	_, _, _ = testNew(srv0.URL, "k").BookSearchTraced(context.Background(), "Under the Oak Tree, Vol. 3", "Suji Kim", []int{7020})
	budget := len(q0()) - 1
	srv, queries := spelledOutIndexer(t)
	c := testNew(srv.URL, "k")
	ctx := WithQueryBudget(context.Background(), newCountingBudget(budget))
	res, trace, err := c.BookSearchTraced(ctx, "Under the Oak Tree, Vol. 3", "Suji Kim", []int{7020})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 || !trace.BudgetExhausted || trace.VolumeMarkerError == "" {
		t.Errorf("results %d trace %+v queries %q", len(res), trace, queries())
	}
	if len(queries()) > budget {
		t.Errorf("sent %d queries past a budget of %d: %q", len(queries()), budget, queries())
	}
}

// TestBookSearch_NoStraySpaceBeforeComma pins the reporter's side note in
// #4176: removing "(Novel)" must not leave "Tree , Vol. 4" in the query.
func TestBookSearch_NoStraySpaceBeforeComma(t *testing.T) {
	srv, queries := spelledOutIndexer(t)
	c := testNew(srv.URL, "k")
	_, _, _ = c.BookSearchTraced(context.Background(), "Under the Oak Tree (Novel), Vol. 4", "Suji Kim", []int{7020})
	for _, q := range queries() {
		if strings.Contains(q, " ,") {
			t.Errorf("stray space before comma in query %q", q)
		}
	}
}
