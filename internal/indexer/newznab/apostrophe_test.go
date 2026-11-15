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
		"Carl’s Doomsday Scenario":     {"Carls Doomsday Scenario", "Carl Doomsday Scenario"},
		"L'Outsider":                   {"LOutsider", "L Outsider"},
		"Rock 'n' Roll":                {"Rock n Roll"},
		"Dune":                         nil,
		"Ender's Game: Extra":          {"Enders Game", "Ender Game"},
		"James' Book":                  {"James Book"},
		"'Salem's Lot":                 {"Salems Lot", "Salem Lot"},
		"Don't Look Now":               {"Dont Look Now", "Don t Look Now"},
		"It's a Wonderful Life":        {"Its a Wonderful Life", "It a Wonderful Life"},
		"Carl's Buch (German Edition)": {"Carls Buch", "Carl Buch"},
		// Too little left to be a query: under three letters, or one word of
		// three or fewer.
		"It's": nil,
		"O'":   nil,
		"I'm":  nil,
		// U+02BC and the backtick are apostrophes too.
		"Oʼahu Nights":             {"Oahu Nights", "O ahu Nights"},
		"Carl`s Doomsday Scenario": {"Carls Doomsday Scenario", "Carl Doomsday Scenario"},
		// The possessive rule reads Unicode letters: this "s" starts a word, it
		// does not end one ("L é" was the bug), and "Lsé" is too short to send.
		"L'sé":        {"L sé"},
		"L'sélection": {"Lsélection", "L sélection"},
		"Zoë's Tale":  {"Zoës Tale", "Zoë Tale"},
		"'s Nachts":   {"s Nachts"},
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

// scriptedIndexer answers each query with whatever respond says: a release
// name, "" for an empty feed, or a status code written as "!429".
func scriptedIndexer(t *testing.T, respond func(term string) string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		term := q.Get("q")
		if term == "" {
			term = q.Get("title")
		}
		mu.Lock()
		queries = append(queries, term)
		mu.Unlock()
		switch out := respond(term); {
		case out == "!429":
			w.WriteHeader(http.StatusTooManyRequests)
		case out == "!500":
			w.WriteHeader(http.StatusInternalServerError)
		case out == "":
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(emptyFeed))
		default:
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(feedWith(out)))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), queries...) }
}

// A result the number word variant found and kept is relevant, even though it
// does not carry the spelled number the title has. The apostrophe step must
// not run, let alone replace it with what a looser spelling returns.
func TestBookSearch_ApostropheVariantKeepsNumberWordResult(t *testing.T) {
	const good = "Some Author - Carl's Quest Book 3 [M4B]"
	srv, queries := scriptedIndexer(t, func(term string) string {
		switch term {
		case "Carl's Quest Book 3":
			return good
		case "Carl Quest Book Three", "Carls Quest Book Three":
			return "Carl Quest Book Three and Other Fan Stories"
		}
		return ""
	})
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(), "Carl's Quest Book Three", "Some Author", []int{3030})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != good {
		t.Errorf("results = %+v, want the number word result kept; queries %q", res, queries())
	}
	if len(trace.ApostropheQueries) != 0 {
		t.Errorf("apostrophe variants sent after a relevant result: %q", trace.ApostropheQueries)
	}
}

// After the number word variant is rate limited the indexer has said stop, so
// no apostrophe variant follows it.
func TestBookSearch_ApostropheVariantNotSentAfterRateLimit(t *testing.T) {
	srv, queries := scriptedIndexer(t, func(term string) string {
		if strings.Contains(term, "Book 3") {
			return "!429"
		}
		return ""
	})
	c := testNew(srv.URL, "k")
	_, trace, _ := c.BookSearchTraced(context.Background(), "Carl's Quest Book Three", "Some Author", []int{3030})
	if trace.NumberWordError == "" {
		t.Fatalf("number word variant did not fail as scripted: %+v", trace)
	}
	if len(trace.ApostropheQueries) != 0 {
		t.Errorf("apostrophe variants sent after a rate limit: %q", trace.ApostropheQueries)
	}
	if got := queries(); !strings.Contains(got[len(got)-1], "Book 3") {
		t.Errorf("queries continued after the rate limit: %q", got)
	}
}

// A variant that is itself rate limited ends the search with that error, as a
// rate limited tier does, so the caller sets its cooldown. Nothing follows it.
func TestBookSearch_ApostropheVariantRateLimitSurfaces(t *testing.T) {
	srv, queries := scriptedIndexer(t, func(term string) string {
		if !strings.Contains(term, "'") {
			return "!429"
		}
		return ""
	})
	c := testNew(srv.URL, "k")
	_, trace, err := c.BookSearchTraced(context.Background(), "Carl's Doomsday Scenario", "Matt Dinniman", []int{3030})
	if !IsRateLimitError(err) {
		t.Errorf("err = %v, want the variant's rate limit", err)
	}
	if len(trace.ApostropheQueries) != 1 {
		t.Errorf("variants sent = %q, want one", trace.ApostropheQueries)
	}
	if got := queries(); got[len(got)-1] != "Carls Doomsday Scenario" {
		t.Errorf("queries continued after the rate limit: %q", got)
	}
}

// Any other failure of a variant is reported on the trace, ends the step, and
// leaves the search standing.
func TestBookSearch_ApostropheVariantSoftErrorReported(t *testing.T) {
	srv, queries := scriptedIndexer(t, func(term string) string {
		if !strings.Contains(term, "'") {
			return "!500"
		}
		return ""
	})
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(), "Carl's Doomsday Scenario", "Matt Dinniman", []int{3030})
	if err != nil || len(res) != 0 {
		t.Fatalf("res %d err %v", len(res), err)
	}
	if trace.ApostropheError == "" || len(trace.ApostropheQueries) != 1 {
		t.Errorf("trace = %+v, queries %q", trace, queries())
	}
}

// The daily cap refusing a variant is flagged for the caller and is not an
// error of the search, the same as for the number word variant.
func TestBookSearch_ApostropheVariantBudgetExhausted(t *testing.T) {
	srv, _ := apostropheAverseIndexer(t, "Matt Dinniman - Carls Doomsday Scenario (Audiobook)")
	c := testNew(srv.URL, "k")
	ctx := WithQueryBudget(context.Background(), &capBudget{left: 4}) // the whole cascade, nothing for a variant
	res, trace, err := c.BookSearchTraced(ctx, "Carl's Doomsday Scenario", "Matt Dinniman", []int{3030})
	if err != nil || len(res) != 0 {
		t.Fatalf("res %d err %v", len(res), err)
	}
	if !trace.BudgetExhausted || trace.ApostropheError == "" || len(trace.ApostropheQueries) != 1 {
		t.Errorf("trace = %+v", trace)
	}
}

// An apostrophe title that stays unfound costs the four tiers plus the two
// spellings, six queries, all of them counted by the daily budget.
func TestBookSearch_ApostropheVariantQueryCeiling(t *testing.T) {
	srv, queries := scriptedIndexer(t, func(string) string { return "" })
	c := testNew(srv.URL, "k")
	budget := &capBudget{left: 100}
	ctx := WithQueryBudget(context.Background(), budget)
	if _, _, err := c.BookSearchTraced(ctx, "Carl's Doomsday Scenario", "Matt Dinniman", []int{3030}); err != nil {
		t.Fatal(err)
	}
	if got := queries(); len(got) != 6 || 100-budget.left != 6 {
		t.Errorf("sent %d queries, budget took %d, want 6 and 6: %q", len(got), 100-budget.left, got)
	}
}
