package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

const subtitleTitle = "Path to Transcendence, Book 1: Will of Mana"

// subtitleIndexer answers like MyAnonamouse in #4183: every query word must
// appear in a release name, and the catalogue holds the given releases.
func subtitleIndexer(t *testing.T, releases ...string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
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
		var hits []string
		for _, rel := range releases {
			have := map[string]bool{}
			for _, w := range words(rel) {
				have[w] = true
			}
			ok := true
			for _, w := range words(term) {
				if !have[w] {
					ok = false
					break
				}
			}
			if ok {
				hits = append(hits, rel)
			}
		}
		if len(hits) == 0 {
			w.Write([]byte(emptyFeed))
			return
		}
		w.Write([]byte(feedWith(hits...)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), queries...) }
}

func countContaining(qs []string, sub string) int {
	n := 0
	for _, q := range qs {
		if strings.Contains(q, sub) {
			n++
		}
	}
	return n
}

// The reporter's case: the release is named by the subtitle alone.
func TestBookSearch_SubtitleOnlyVariant(t *testing.T) {
	srv, queries := subtitleIndexer(t, "Will of Mana by Ryan Arelius [ENG / M4B]")
	res, trace, err := testNew(srv.URL, "k").BookSearchTraced(context.Background(), subtitleTitle, "Ryan Arelius", []int{3030})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != "Will of Mana by Ryan Arelius [ENG / M4B]" {
		t.Fatalf("results = %+v; queries %q", res, queries())
	}
	if trace.SubtitleQuery != "Arelius Will of Mana" || trace.SubtitleResults != 1 || trace.SubtitleError != "" {
		t.Errorf("trace = %+v", trace)
	}
	qs := queries()
	if countContaining(qs, "Will of Mana") != 1 || qs[len(qs)-1] != "Arelius Will of Mana" {
		t.Errorf("want exactly one subtitle query, sent last; got %q", qs)
	}
}

// The #3268 regression guard: a different book by a different author whose
// title equals the subtitle is returned by the indexer and must be rejected.
func TestBookSearch_SubtitleOnlyRejectsOtherAuthor(t *testing.T) {
	// A loose indexer that answers the subtitle query with everything that
	// holds "Will of Mana", whoever wrote it (most indexers rank, not require).
	var mu sync.Mutex
	var sent []string
	loose := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, r.URL.Query().Get("q")+r.URL.Query().Get("title"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.RawQuery, "Will") {
			w.Write([]byte(feedWith("Will of Mana by Jane Doe [ENG / M4B]", "Will of Mana", "Arelius Dragon Mana by Ryan Arelius", "Will of Mana by Ryan Arelius [ENG / M4B]")))
			return
		}
		w.Write([]byte(emptyFeed))
	}))
	t.Cleanup(loose.Close)
	res, trace, _ := testNew(loose.URL, "k").BookSearchTraced(context.Background(), subtitleTitle, "Ryan Arelius", []int{3030})
	if len(res) != 1 || res[0].Title != "Will of Mana by Ryan Arelius [ENG / M4B]" {
		t.Errorf("kept %+v, want only the release naming the author; sent %q", res, sent)
	}
	if trace.SubtitleQuery == "" || trace.SubtitleResults != 1 {
		t.Errorf("trace = %+v", trace)
	}

	// Only wrong-author releases come back: nothing is accepted.
	only := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.RawQuery, "Will") {
			w.Write([]byte(feedWith("Will of Mana by Jane Doe [ENG / M4B]")))
			return
		}
		w.Write([]byte(emptyFeed))
	}))
	t.Cleanup(only.Close)
	res, trace, _ = testNew(only.URL, "k").BookSearchTraced(context.Background(), subtitleTitle, "Ryan Arelius", []int{3030})
	if len(res) != 0 || trace.SubtitleResults != 0 || trace.SubtitleQuery == "" {
		t.Errorf("another author's book with the subtitle as its title was accepted: %+v %+v", res, trace)
	}
}

func mustPlan(t *testing.T, title, author string) subtitleOnlyPlan {
	t.Helper()
	p, ok := subtitleOnlyQuery(title, author)
	if !ok {
		t.Fatalf("no plan for %q by %q", title, author)
	}
	return p
}

func TestSubtitleOnlyAccepts(t *testing.T) {
	p := mustPlan(t, subtitleTitle, "Ryan Arelius")
	cases := []struct {
		title, author string
		want          bool
	}{
		// Names that are the book.
		{"Will of Mana by Ryan Arelius [ENG / M4B]", "", true},
		{"Will.of.Mana.Ryan.Arelius.M4B", "", true},
		{"Arelius, Ryan - Will of Mana (epub)", "", true},
		{"Ryan Arelius - Will of Mana", "", true},
		{"Will of Mana", "Ryan Arelius", true}, // author attribute
		{"Will of Mana by Ryan Arelius (2026) [English / MP3 / 64 kbps]", "", true},
		{"Arelius Will of Mana", "", true},
		{"Will of Mana Arelius", "", true},
		{"Will_of_Mana-Arelius-ENG-M4B", "", true},
		{"Ryan_Arelius-Will_of_Mana-ENG-M4B", "", true},

		// Audiobook credits and bracketed notes after the subtitle.
		{"Will of Mana by Ryan Arelius, narrated by Travis Baldree [M4B]", "", true},
		{"Will of Mana by Ryan Arelius read by Travis Baldree", "", true},
		{"Ryan Arelius - Will of Mana [Audible] [M4B]", "", true},
		{"Will of Mana by Ryan Arelius [Podium Audio] [M4B]", "", true},
		{"Will of Mana by Ryan Arelius & Jane Doe [M4B]", "", true},
		{"Will of Mana by Ryan Arelius and Jane Doe", "", true},
		{"Will of Mana - Ryan Arelius [LitRPG] [M4B]", "", true},
		{"Will of Mana by Ryan Arelius [Complete] [M4B]", "", true},
		{"Ryan Arelius - Will of Mana (Audiobook) [12h 30m]", "", true},
		{"Will of Mana by Ryan Arelius [M4B] [Aud. Book]", "", true},
		{"Will of Mana by Ryan Arelius [Dragon Saga]", "", true},
		// Bracketed notes that name a position, another book or language.
		{"Will of Mana by Ryan Arelius [Vol. 1]", "", false},
		{"Will of Mana by Ryan Arelius [Books 1-3]", "", false},
		{"Will of Mana by Ryan Arelius [Box Set]", "", false},
		{"Will of Mana by Ryan Arelius (Other Saga, Book One)", "", false},
		{"Will of Mana by Ryan Arelius [Trilogy]", "", false},
		{"Will of Mana by Ryan Arelius [FR] [M4B]", "", false},
		{"Will of Mana by Ryan Arelius [German / M4B]", "", false},
		// Unbracketed extras and credits that carry a number or a stray word.
		{"Will of Mana by Ryan Arelius narrated by Travis Baldree Book 2", "", false},
		{"Will of Mana by Ryan Arelius Unofficial Guide", "", false},
		{"Will of Mana by Jane Doe narrated by Travis Baldree", "", false},
		{"Will of Mana by Jane Doe & Bob Roe [M4B]", "", false},

		// Another author, another title, another book.
		{"Will of Mana by Jane Doe", "", false},
		{"Will of Mana", "", false},
		{"Will of Mana by Jane Doe", "Jane Doe", false},
		{"Mana of Will by Ryan Arelius", "", false},
		{"Will and Mana by Ryan Arelius", "", false},
		{"Will of the Mana by Ryan Arelius", "", false},
		{"Will of Mana by Ryan Arelian", "", false},
		{"Will of Manager by Ryan Arelius", "", false},
		{"Will of Mana by Ryan Smith", "", false},
		// The review's eight: the subtitle is inside a longer name.
		{"The Will of Mana Wars by Ryan Arelius", "", false},
		{"Will of Mana Rising by Ryan Arelius", "", false},
		{"Will of Mana 2 by Ryan Arelius", "", false},
		{"Will of Mana Companion Guide by Ryan Arelius", "", false},
		{"Ryan Arelius - Will of Mana (Other Saga Book 5)", "", false},
		{"Ryan Arelius - Dragon Saga, Book 1 Will of Mana", "", false},
		{"Path to Transcendence Omnibus Books 1-3 Will of Mana by Ryan Arelius", "", false},
		{"Will of Mana (German)", "Ryan Arelius", false},
		// Hyphenated surname.
		{"Will of Mana by Ryan Arelius-Smith", "", false},
		{"Will of Mana", "Ann Arelius-Smith", false},
	}
	for _, c := range cases {
		got := subtitleOnlyAccepts(p, SearchResult{Title: c.title, Author: c.author})
		if got != c.want {
			t.Errorf("accepts(%q, author %q) = %v, want %v", c.title, c.author, got, c.want)
		}
	}
	// A surname that is also a subtitle word cannot vouch for itself.
	self := mustPlan(t, "Saga 1: Will of Mana", "Jane Mana")
	if subtitleOnlyAccepts(self, SearchResult{Title: "Will of Mana by Jane Doe"}) {
		t.Error("the surname matched inside the subtitle's own words")
	}
	if !subtitleOnlyAccepts(self, SearchResult{Title: "Will of Mana by Jane Mana"}) {
		t.Error("the surname outside the subtitle was refused")
	}
	// Hyphen: Lee-Smith is not Lee, but Lee alone and a spaced hyphen are.
	lee := mustPlan(t, subtitleTitle, "Ann Lee")
	for title, want := range map[string]bool{
		"Will of Mana by Ann Lee-Smith": false,
		"Will of Mana by Ann Lee":       true,
		"Ann Lee - Will of Mana":        true,
	} {
		if got := subtitleOnlyAccepts(lee, SearchResult{Title: title}); got != want {
			t.Errorf("accepts(%q) = %v, want %v", title, got, want)
		}
	}
	if subtitleOnlyAccepts(lee, SearchResult{Title: "Will of Mana", Author: "Ann Lee-Smith"}) {
		t.Error("Lee-Smith in the author attribute accepted for Lee")
	}
	// "&" and "and", and a leading "The", compare equal.
	amp := mustPlan(t, "Saga 1: The Sword & Sorcery", "Ann Lee")
	for _, title := range []string{"Sword and Sorcery by Ann Lee", "Sword & Sorcery by Ann Lee", "The Sword and Sorcery by Ann Lee"} {
		if !subtitleOnlyAccepts(amp, SearchResult{Title: title}) {
			t.Errorf("refused %q", title)
		}
	}
	if amp.Query != "Lee Sword Sorcery" {
		t.Errorf("query = %q", amp.Query)
	}
	if subtitleOnlyAccepts(amp, SearchResult{Title: "The Sword and Sorcery Wars by Ann Lee"}) {
		t.Error("longer title accepted")
	}
}

func TestSubtitleOnlyQuery(t *testing.T) {
	cases := []struct {
		name, title, author string
		want                string // "" means not sent
	}{
		{"reporter", subtitleTitle, "Ryan Arelius", "Arelius Will of Mana"},
		{"spelled out position", "Path to Transcendence, Book One: Will of Mana", "Ryan Arelius", "Arelius Will of Mana"},
		{"series number", "Feral Mage 4: The Mining Company Contract", "Chase Kilgore", "Kilgore Mining Company Contract"},
		{"trailing qualifier", "Path to Transcendence, Book 1: Will of Mana (Unabridged)", "Ryan Arelius", "Arelius Will of Mana"},
		{"two colons", "Series 2: Will of Mana: A Tale", "Ryan Arelius", "Arelius Will of Mana: A Tale"},
		{"parenthetical author", subtitleTitle, "Ann Lee (Narrator)", "Lee Will of Mana"},
		{"bracketed author", subtitleTitle, "Ann Lee [Narrator]", "Lee Will of Mana"},
		{"trailing tag cut", "Series 2: Will of Mana - A LitRPG Adventure", "Ann Lee", "Lee Will of Mana"},
		{"leading the and ampersand", "Series 2: The Sword & Sorcery", "Ann Lee", "Lee Sword Sorcery"},
		{"colon kept", "Series 2: A Big: Small Thing", "Ann Lee", "Lee A Big: Small Thing"},
		{"name suffix", subtitleTitle, "Ryan Arelius Jr.", "Arelius Will of Mana"},
		{"author list", subtitleTitle, "Ryan Arelius & Jane Doe", "Arelius Will of Mana"},
		{"mononym", subtitleTitle, "Arelius", "Arelius Will of Mana"},
		{"umlaut", "Reihe 2: Zauber der Magie", "Anna Müller", "Mueller Zauber der Magie"},

		{"no subtitle", "Path to Transcendence, Book 1", "Ryan Arelius", ""},
		{"plain subtitle, no position", "Dune: Messiah", "Frank Herbert", ""},
		{"subtitle numbers the book", "Dune: Part Two", "Frank Herbert", ""},
		{"generic novel", "Path to Transcendence, Book 1: A Novel", "Ryan Arelius", ""},
		{"generic book two", "Path to Transcendence 1: Book Two", "Ryan Arelius", ""},
		{"generic volume", "Path to Transcendence 1: Volume 3", "Ryan Arelius", ""},
		{"generic words only", "Path to Transcendence 1: The Novel Edition", "Ryan Arelius", ""},
		{"one significant word", "Path to Transcendence, Book 1: Mana", "Ryan Arelius", ""},
		{"stop words only", "Path to Transcendence, Book 1: To Be Or", "Ryan Arelius", ""},
		{"too short", "Path to Transcendence, Book 1: Go", "Ryan Arelius", ""},
		{"no author", subtitleTitle, "", ""},
		{"placeholder author", subtitleTitle, "Various", ""},
		{"initial only", subtitleTitle, "R.", ""},
		{"suffix only", subtitleTitle, "Jr.", ""},
	}
	for _, c := range cases {
		p, ok := subtitleOnlyQuery(c.title, c.author)
		if c.want == "" {
			if ok {
				t.Errorf("%s: query %q built, want none", c.name, p.Query)
			}
			continue
		}
		if !ok || p.Query != c.want {
			t.Errorf("%s: got %q (ok=%v), want %q", c.name, p.Query, ok, c.want)
		}
	}
}

// Each skip condition sends nothing beyond the cascade.
func TestBookSearch_SubtitleOnlyVariantSkips(t *testing.T) {
	ctx := context.Background()
	release := "Will of Mana by Ryan Arelius [ENG / M4B]"

	// No subtitle, a plain subtitle and a generic one: no query.
	for _, title := range []string{"Path to Transcendence, Book 1", "Dune: Messiah", "Path to Transcendence, Book 1: A Novel"} {
		srv, queries := subtitleIndexer(t, release)
		_, trace, _ := testNew(srv.URL, "k").BookSearchTraced(ctx, title, "Ryan Arelius", []int{3030})
		if trace.SubtitleQuery != "" || countContaining(queries(), "Arelius Will") != 0 {
			t.Errorf("%q: subtitle query sent: %+v %q", title, trace, queries())
		}
	}

	// A relevant result already exists: the cascade found the series book.
	srv, queries := subtitleIndexer(t, "Path to Transcendence, Book 1 by Ryan Arelius", release)
	res, trace, _ := testNew(srv.URL, "k").BookSearchTraced(ctx, subtitleTitle, "Ryan Arelius", []int{3030})
	if trace.SubtitleQuery != "" || len(res) == 0 {
		t.Errorf("sent despite a relevant result: %+v %q", trace, queries())
	}

	// No author: no query.
	srv, queries = subtitleIndexer(t, release)
	_, trace, _ = testNew(srv.URL, "k").BookSearchTraced(ctx, subtitleTitle, "", []int{3030})
	if trace.SubtitleQuery != "" {
		t.Errorf("sent without an author: %+v %q", trace, queries())
	}

	// An earlier fallback errored: nothing more is sent to this indexer.
	for name, trace := range map[string]SearchTrace{
		"number word": {NumberWordError: "boom"},
		"apostrophe":  {ApostropheError: "boom"},
		"volume":      {VolumeMarkerError: "boom"},
		"budget":      {BudgetExhausted: true},
	} {
		srv, queries := subtitleIndexer(t, release)
		_, out, err := testNew(srv.URL, "k").subtitleOnlyVariant(ctx, subtitleTitle, "Ryan Arelius", nil, []int{3030}, trace)
		if err != nil || out.SubtitleQuery != "" || len(queries()) != 0 {
			t.Errorf("%s: sent after an earlier failure: %+v %q", name, out, queries())
		}
	}

	// A cancelled search sends nothing.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	srv, queries = subtitleIndexer(t, release)
	_, out, _ := testNew(srv.URL, "k").subtitleOnlyVariant(cctx, subtitleTitle, "Ryan Arelius", nil, []int{3030}, SearchTrace{})
	if out.SubtitleQuery != "" || len(queries()) != 0 {
		t.Errorf("sent on a cancelled context: %+v", out)
	}
}

// One extra query per failing search, no more.
func TestBookSearch_SubtitleOnlyVariantCost(t *testing.T) {
	srv, queries := subtitleIndexer(t)
	_, trace, _ := testNew(srv.URL, "k").BookSearchTraced(context.Background(), subtitleTitle, "Ryan Arelius", []int{3030})
	if trace.SubtitleQuery != "Arelius Will of Mana" || trace.SubtitleResults != 0 {
		t.Errorf("trace = %+v", trace)
	}
	if n := countContaining(queries(), "Will of Mana"); n != 1 {
		t.Errorf("%d subtitle queries, want 1: %q", n, queries())
	}
}

// The subtitle query is a normal query: the daily cap counts it and refuses it.
func TestBookSearch_SubtitleOnlyVariantBudget(t *testing.T) {
	release := "Will of Mana by Ryan Arelius [ENG / M4B]"
	srv0, q0 := subtitleIndexer(t, release)
	_, _, _ = testNew(srv0.URL, "k").BookSearchTraced(context.Background(), subtitleTitle, "Ryan Arelius", []int{3030})
	total := len(q0())

	// A cap of the whole cascade plus the subtitle query: all of it is counted.
	srv1, _ := subtitleIndexer(t, release)
	b1 := newCountingBudget(total)
	res, _, err := testNew(srv1.URL, "k").BookSearchTraced(WithQueryBudget(context.Background(), b1), subtitleTitle, "Ryan Arelius", []int{3030})
	if err != nil || len(res) != 1 {
		t.Fatalf("res %d err %v", len(res), err)
	}
	if got := int(b1.taken.Load()); got != total {
		t.Errorf("budget took %d queries, want %d, so the subtitle query counts", got, total)
	}

	// One short of that: the subtitle query is refused, not sent.
	srv, queries := subtitleIndexer(t, release)
	ctx := WithQueryBudget(context.Background(), newCountingBudget(total-1))
	res, trace, err := testNew(srv.URL, "k").BookSearchTraced(ctx, subtitleTitle, "Ryan Arelius", []int{3030})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 || !trace.BudgetExhausted || trace.SubtitleError == "" {
		t.Errorf("results %d trace %+v", len(res), trace)
	}
	if len(queries()) > total-1 {
		t.Errorf("sent %d queries past a budget of %d", len(queries()), total-1)
	}
}

// A name built to make a naive search loop (every word a hyphenated surname
// match, brackets never closed) still ends.
func TestSubtitleOnlyAcceptsTerminates(t *testing.T) {
	p := mustPlan(t, subtitleTitle, "Ann Lee")
	names := []string{
		"Will of Mana " + strings.Repeat("Lee-Smith ", 20000),
		strings.Repeat("Lee-Lee-Lee ", 20000) + "Will of Mana",
		"Will of Mana " + strings.Repeat("[", 20000) + strings.Repeat("Lee-", 20000),
		strings.Repeat("Will of Mana ", 5000) + strings.Repeat("Lee-Smith ", 5000),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, n := range names {
			// Only that it returns: whether such a name is kept is not the point.
			subtitleOnlyAccepts(p, SearchResult{Title: n, Author: strings.Repeat("Lee-Smith ", 5000)})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subtitleOnlyAccepts did not return")
	}
}
