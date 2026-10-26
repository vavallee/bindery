package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestFoldNumberWords(t *testing.T) {
	cases := map[string]string{
		"Path of the Deathless Book One": "Path of the Deathless Book 1",
		"Path of the Deathless BOOK ONE": "Path of the Deathless BOOK 1",
		"Series Vol. Two":                "Series Vol. 2",
		"Series Volume Twelve":           "Series Volume 12",
		"Series Part Twenty":             "Series Part 20",
		"Series.Book.Three.M4B":          "Series.Book.3.M4B",
		"Book One and Book Two":          "Book 1 and Book 2",
		"Year One":                       "Year 1",
		"Season Nine Episode Four":       "Season 9 Episode 4",
		"Arc Five":                       "Arc 5",
		// Untouched: no volume word, compound numbers, longer words, digits.
		"Number One":          "Number One",
		"The Two Towers":      "The Two Towers",
		"One Hundred Years":   "One Hundred Years",
		"Book Twenty One":     "Book Twenty One",
		"Book Twenty-Five":    "Book Twenty-Five",
		"Book Oneness":        "Book Oneness",
		"Notebook One":        "Notebook One",
		"Book 1":              "Book 1",
		"Book Twenty":         "Book 20",
		"Book Twenty Wonders": "Book 20 Wonders",
		"Partone":             "Partone",
	}
	for in, want := range cases {
		if got := FoldNumberWords(in); got != want {
			t.Errorf("FoldNumberWords(%q) = %q, want %q", in, got, want)
		}
	}
}

const emptyFeed = `<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="0"/></channel></rss>`

func feedWith(titles ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><newznab:response total="` + string(rune('0'+len(titles))) + `"/>`)
	for _, t := range titles {
		b.WriteString(`<item><title>` + t + `</title><guid>` + t + `</guid><link>http://x/` + strings.ReplaceAll(t, " ", "") + `</link></item>`)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

// strictIndexer answers like MyAnonamouse: every query word must appear in the
// release name, here a single release called "... Book 1".
func strictIndexer(t *testing.T) (*httptest.Server, func() []string) {
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
		release := "OstensibleMammal - Path of the Deathless Book 1 [M4B]"
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

// TestBookSearch_NumberWordVariant pins #3476 on the wire: every tier keeps the
// spelled number and finds nothing, then exactly one digit query finds the
// release.
func TestBookSearch_NumberWordVariant(t *testing.T) {
	srv, queries := strictIndexer(t)
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(),
		"Path of the Deathless Book One: A LitRPG Progression Epic", "OstensibleMammal", []int{3030})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1; queries %q", len(res), queries())
	}
	if trace.NumberWordQuery != "Path of the Deathless Book 1" || trace.NumberWordResults != 1 {
		t.Errorf("trace = %+v", trace)
	}
	qs := queries()
	digit := 0
	for _, q := range qs {
		if strings.Contains(q, "Book 1") {
			digit++
		}
	}
	if digit != 1 {
		t.Errorf("digit queries = %d, want exactly 1 (all: %q)", digit, qs)
	}
	for _, q := range qs[:len(qs)-1] {
		if strings.Contains(q, "Book 1") {
			t.Errorf("digit form sent before the cascade finished: %q", qs)
		}
	}
}

// TestBookSearch_NumberWordVariantCost checks the added cost is bounded: no
// extra query for a title without a number word, none when the cascade already
// found the book, and one (not a whole second cascade) when it did not.
func TestBookSearch_NumberWordVariantCost(t *testing.T) {
	srv, queries := strictIndexer(t)
	c := testNew(srv.URL, "k")

	// No number word: nothing extra.
	_, trace, _ := c.BookSearchTraced(context.Background(), "Nothing Like It", "OstensibleMammal", []int{3030})
	if trace.NumberWordQuery != "" {
		t.Errorf("variant sent for a title without a number word: %+v", trace)
	}

	// Already spelled as the release spells it: the cascade finds it, no variant.
	res, trace, _ := c.BookSearchTraced(context.Background(), "Path of the Deathless Book 1", "OstensibleMammal", []int{3030})
	if len(res) != 1 || trace.NumberWordQuery != "" {
		t.Errorf("digit title: results %d trace %+v", len(res), trace)
	}

	// Unfindable title with a number word: one extra query beyond the cascade.
	base := len(queries())
	_, _, _ = c.BookSearchTraced(context.Background(), "Missing Saga Book Three", "Nobody", []int{3030})
	spelled := len(queries()) - base
	base = len(queries())
	_, _, _ = c.BookSearchTraced(context.Background(), "Lost Saga Book 3", "Nobody", []int{3030})
	digits := len(queries()) - base
	if spelled-digits != 1 {
		t.Errorf("number word title sent %d queries, digit title %d; want exactly one more", spelled, digits)
	}
}

func TestFoldNumberWords_DottedAndUnderscoreCompounds(t *testing.T) {
	for _, s := range []string{
		"Hunt Book.Twenty.One", "Hunt Book Twenty.One", "Hunt_Book_Twenty_One",
		"Hunt.Book.Twenty-One", "Hunt Book.Twenty_Five",
	} {
		if got := FoldNumberWords(s); got != s {
			t.Errorf("FoldNumberWords(%q) = %q, want it untouched", s, got)
		}
	}
	if got := FoldNumberWords("Hunt.Book.Twenty.M4B"); got != "Hunt.Book.20.M4B" {
		t.Errorf("plain twenty not folded: %q", got)
	}
}

// A compound written with dots must not become a bogus digit query.
func TestBookSearch_CompoundNumberSendsNoVariant(t *testing.T) {
	srv, queries := strictIndexer(t)
	c := testNew(srv.URL, "k")
	for _, title := range []string{"Hunt Book Twenty.One", "Hunt Book_Twenty_One", "Hunt Book Twenty One"} {
		_, trace, err := c.BookSearchTraced(context.Background(), title, "Nobody", []int{3030})
		if err != nil || trace.NumberWordQuery != "" {
			t.Errorf("%q: trace %+v err %v", title, trace, err)
		}
	}
	for _, q := range queries() {
		if strings.Contains(q, "20") {
			t.Errorf("bogus digit query sent: %q", q)
		}
	}
}

type capBudget struct{ left int }

func (b *capBudget) Take() bool {
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}

// A variant refused by the daily cap is reported, not mistaken for an empty
// indexer, and does not fail the search.
func TestBookSearch_NumberWordVariantBudgetExhausted(t *testing.T) {
	srv, _ := strictIndexer(t)
	c := testNew(srv.URL, "k")
	ctx := WithQueryBudget(context.Background(), &capBudget{left: 3}) // the whole cascade, nothing for the variant
	res, trace, err := c.BookSearchTraced(ctx, "Path of the Deathless Book One", "OstensibleMammal", []int{3030})
	if err != nil || len(res) != 0 {
		t.Fatalf("res %d err %v", len(res), err)
	}
	if !trace.BudgetExhausted || trace.NumberWordError == "" || trace.NumberWordQuery == "" {
		t.Errorf("trace = %+v", trace)
	}
}

func TestBookSearch_NumberWordVariantErrorReported(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if strings.Contains(r.URL.Query().Get("q"), "Book 1") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(emptyFeed))
	}))
	defer srv.Close()
	c := testNew(srv.URL, "k")
	res, trace, err := c.BookSearchTraced(context.Background(), "Path of the Deathless Book One", "Someone Else", []int{3030})
	if err != nil || len(res) != 0 {
		t.Fatalf("res %d err %v", len(res), err)
	}
	if trace.NumberWordError == "" {
		t.Errorf("failure of the variant not reported: %+v", trace)
	}
}
