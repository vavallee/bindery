package ranobedb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/useragent"
)

// The testdata files are real RanobeDB API responses (ODbL), captured
// 2026-11-15 with Bindery's User-Agent: two searches for Spice and Wolf
// volume 3, a search for the bare series name, and volume 3's book record.

// fakeAPI serves search answers by query and book records by id, and records
// every request.
type fakeAPI struct {
	t        *testing.T
	searches map[string]string // query -> body
	books    map[string]string // "/book/3188" -> body
	status   int               // when set, every request gets it

	mu     sync.Mutex
	paths  []string
	agents []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.agents = append(f.agents, r.Header.Get("User-Agent"))
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch {
	case r.URL.Path == "/books":
		if r.URL.Query().Get("limit") != "5" {
			f.t.Errorf("search limit = %q, want 5", r.URL.Query().Get("limit"))
		}
		body, ok := f.searches[r.URL.Query().Get("q")]
		if !ok {
			body = `{"books":[]}`
		}
		_, _ = w.Write([]byte(body))
	case f.books[r.URL.Path] != "":
		_, _ = w.Write([]byte(f.books[r.URL.Path]))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testClient(t *testing.T, api *fakeAPI) *Client {
	t.Helper()
	restore := providerhttp.SetBaseDelay(time.Millisecond)
	t.Cleanup(restore)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return &Client{http: srv.Client(), gate: providerhttp.NewGate(), base: srv.URL}
}

func spiceAPI(t *testing.T) *fakeAPI {
	return &fakeAPI{
		t: t,
		searches: map[string]string{
			"Spice and Wolf, Vol. 3":               fixture(t, "search_spice_vol3_plain.json"),
			"Spice and Wolf, Vol. 3 (light novel)": fixture(t, "search_spice_vol3.json"),
			"Spice and Wolf":                       fixture(t, "search_spice_tie.json"),
		},
		books: map[string]string{"/book/3188": fixture(t, "book_3188.json")},
	}
}

func TestSearchBooksReturnsTheClearWinner(t *testing.T) {
	for _, query := range []string{"Spice and Wolf, Vol. 3", "Spice and Wolf, Vol. 3 (light novel)"} {
		t.Run(query, func(t *testing.T) {
			api := spiceAPI(t)
			books, err := testClient(t, api).SearchBooks(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				t.Fatalf("got %d books, want 1", len(books))
			}
			b := books[0]
			if b.Title != "Spice and Wolf, Vol. 3" || b.ForeignID != "ranobedb:3188" || b.MetadataProvider != "ranobedb" {
				t.Errorf("book = %+v", b)
			}
			// The real record credits 支倉凍砂, romanised "Hasekura Isuna".
			if b.Author == nil || b.Author.Name != "Hasekura, Isuna" {
				t.Errorf("author = %+v, want Hasekura, Isuna", b.Author)
			}
			if got := strings.Join(api.paths, " "); got != "/books /book/3188" {
				t.Errorf("requests = %s, want a search then the book", got)
			}
		})
	}
}

// TestSearchBooksTieIsNoMatch: the bare series name scores 1 for every
// volume, so it names no book, and the book record is never asked for.
func TestSearchBooksTieIsNoMatch(t *testing.T) {
	api := spiceAPI(t)
	books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf")
	if err != nil || books != nil {
		t.Fatalf("got %v, %v; want no match", books, err)
	}
	if len(api.paths) != 1 {
		t.Errorf("requests = %v, want the search only", api.paths)
	}
}

// TestSearchBooksVolumeGuard: a clear winner whose numbers differ from the
// book's is refused. Vol. 3 is the winner for each query below, and each would
// pass the aggregator's substring title match against it, or the reverse.
func TestSearchBooksVolumeGuard(t *testing.T) {
	winner := fixture(t, "search_spice_vol3_plain.json")
	for _, query := range []string{"Spice and Wolf, Vol. 30", "Spice and Wolf", "Spice and Wolf, Vol. 3 Part 2"} {
		t.Run(query, func(t *testing.T) {
			api := spiceAPI(t)
			api.searches = map[string]string{query: winner}
			books, err := testClient(t, api).SearchBooks(context.Background(), query)
			if err != nil || books != nil {
				t.Fatalf("got %v, %v; want no match", books, err)
			}
			if len(api.paths) != 1 {
				t.Errorf("requests = %v, want the search only", api.paths)
			}
		})
	}
}

func TestSearchBooksNeedsEnglishTitleAndAuthor(t *testing.T) {
	for name, book := range map[string]string{
		"no english title": `{"book":{"id":3188,"titles":[{"lang":"ja","official":true,"title":"狼と香辛料III"}],
			"editions":[{"staff":[{"role_type":"author","name":"支倉凍砂","romaji":"Hasekura Isuna"}]}]}}`,
		"unofficial english title": `{"book":{"id":3188,"titles":[{"lang":"en","official":false,"title":"Spice and Wolf, Vol. 3"}],
			"editions":[{"staff":[{"role_type":"author","name":"支倉凍砂","romaji":"Hasekura Isuna"}]}]}}`,
		"no author": `{"book":{"id":3188,"titles":[{"lang":"en","official":true,"title":"Spice and Wolf, Vol. 3"}],
			"editions":[{"staff":[{"role_type":"artist","name":"文倉十","romaji":"Ayakura Juu"}]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := spiceAPI(t)
			api.books["/book/3188"] = book
			books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
			if err != nil || books != nil {
				t.Fatalf("got %v, %v; want no match", books, err)
			}
		})
	}
}

func TestAuthorNames(t *testing.T) {
	book := bookDetail{Editions: []edition{
		{Staff: []staff{
			{RoleType: "author", Name: "支倉凍砂", Romaji: "Hasekura Isuna"},
			{RoleType: "artist", Name: "文倉十", Romaji: "Ayakura Juu"},
			{RoleType: "author", Name: "ZUN", Romaji: ""},
			{RoleType: "author", Name: "九曜", Romaji: "Kuyou"},
			{RoleType: "author", Name: "x", Romaji: "Ana Maria  Matute"},
		}},
		// The same author credited again on a translation is listed once.
		{Staff: []staff{
			{RoleType: "author", Name: "支倉凍砂", Romaji: "Hasekura Isuna"},
			{RoleType: "translator", Name: "Paul Starr"},
		}},
	}}
	got := strings.Join(authorNames(book), " | ")
	want := "Hasekura, Isuna | ZUN | Kuyou | Ana Maria Matute"
	if got != want {
		t.Errorf("authorNames = %q, want %q", got, want)
	}
}

func TestClearWinner(t *testing.T) {
	r := func(scores ...float64) []searchResult {
		out := make([]searchResult, len(scores))
		for i, s := range scores {
			out[i] = searchResult{ID: i + 1, SimScore: s}
		}
		return out
	}
	for name, tc := range map[string]struct {
		in   []searchResult
		want int // winner ID, 0 for none
	}{
		"empty":            {in: nil},
		"single":           {in: r(0.6), want: 1},
		"strict":           {in: r(2, 0.9, 0.9), want: 1},
		"tie at the top":   {in: r(1, 1, 0.5)},
		"winner not first": {in: r(0.5, 0.9, 0.7), want: 2},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := clearWinner(tc.in)
			if (tc.want == 0) == ok || (ok && got.ID != tc.want) {
				t.Errorf("clearWinner = %v, %v; want ID %d", got, ok, tc.want)
			}
		})
	}
}

func TestSameNumbers(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Spice and Wolf, Vol. 3 (light novel)", "Spice and Wolf, Vol. 3", true},
		{"Spice and Wolf, Vol. 03", "Spice and Wolf, Vol. 3", true},
		{"Spice and Wolf, Vol. 1", "Spice and Wolf, Vol. 10", false},
		{"Spice and Wolf", "Spice and Wolf, Vol. 1", false},
		{"Sword Art Online 1: Aincrad", "Sword Art Online 1: Aincrad", true},
		{"Re:Zero, Vol. 2", "Re:ZERO -Starting Life in Another World-, Vol. 2", true},
		{"No Numbers", "None Here", true},
	} {
		if got := sameNumbers(tc.a, tc.b); got != tc.want {
			t.Errorf("sameNumbers(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestRequestsUseBinderysUserAgent pins the choice documented on New: the
// client identifies itself as Bindery, not as a browser.
func TestRequestsUseBinderysUserAgent(t *testing.T) {
	api := spiceAPI(t)
	if _, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3"); err != nil {
		t.Fatal(err)
	}
	for _, ua := range api.agents {
		if ua != useragent.Get() || strings.Contains(ua, "Mozilla") {
			t.Errorf("User-Agent = %q, want Bindery's own %q", ua, useragent.Get())
		}
	}
}

func TestRateLimitIsReportedAsRateLimited(t *testing.T) {
	api := spiceAPI(t)
	api.status = http.StatusTooManyRequests
	_, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

// TestPacerSpacesRequests: two requests from two goroutines start at least
// one interval apart and never overlap.
func TestPacerSpacesRequests(t *testing.T) {
	p := &pacer{interval: 50 * time.Millisecond, slot: make(chan struct{}, 1)}
	var mu sync.Mutex
	var starts []time.Time
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := p.acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	if len(starts) != 2 {
		t.Fatalf("starts = %v", starts)
	}
	gap := starts[1].Sub(starts[0])
	if gap < 0 {
		gap = -gap
	}
	if gap < 45*time.Millisecond {
		t.Errorf("requests started %v apart, want at least the 50ms interval", gap)
	}
}

func TestPacerRefusesPastDeadline(t *testing.T) {
	p := &pacer{interval: time.Hour, slot: make(chan struct{}, 1)}
	release, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.acquire(ctx); !errors.Is(err, errPaced) {
		t.Errorf("err = %v, want errPaced", err)
	}
}
