package openlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

// OpenLibrary's structured series record names no series, only a key; it is
// kept as "<key> #<position>" for the client to resolve. Before, it was
// dropped like any object without a title.
func TestFlexStringSliceStructuredSeries(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want []string
	}{
		"string position":    {`[{"series":{"key":"/series/OL1L"},"position":"21"}]`, []string{"/series/OL1L #21"}},
		"number position":    {`[{"series":{"key":"/series/OL1L"},"position":2.5}]`, []string{"/series/OL1L #2.5"}},
		"no position":        {`[{"series":{"key":"/series/OL1L"}}]`, []string{"/series/OL1L"}},
		"odd position":       {`[{"series":{"key":"/series/OL1L"},"position":"1-3"}]`, []string{"/series/OL1L"}},
		"bad key":            {`[{"series":{"key":"/series/../authors/OL1A"},"position":"1"}]`, nil},
		"not a series key":   {`[{"series":{"key":"/works/OL1W"}}]`, nil},
		"key with no digits": {`[{"series":{"key":"/series/OLL"},"position":"1"}]`, nil},
		"title and series":   {`[{"title":"Fjellserien","series":{"key":"/series/OL1L"},"position":"2"}]`, []string{"Fjellserien"}},
		"no series field":    {`[{"position":"1"}]`, nil},
		"mixed, order kept":  {`["Vindserien #1",{"series":{"key":"/series/OL2L"},"position":"3"},{"title":"Fjellserien"}]`, []string{"Vindserien #1", "/series/OL2L #3", "Fjellserien"}},
	} {
		t.Run(name, func(t *testing.T) {
			var got flexStringSlice
			if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal([]string(got), tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// End to end through GetBook: the structured record becomes a series named
// after its OpenLibrary series record, with the same ol-series id the plain
// string form of that name gets.
func TestGetBookResolvesStructuredSeries(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL1W.json":       `{"key":"/works/OL1W","title":"Fjellvinden","series":[{"series":{"key":"/series/OL327402L"},"position":"21"}]}`,
		"/series/OL327402L.json": `{"name":"Vindserien","type":{"key":"/series/OL327402L"}}`,
	})
	book, err := c.GetBook(context.Background(), "OL1W")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := parseSeriesRef("Vindserien #21")
	if len(book.SeriesRefs) != 1 {
		t.Fatalf("series refs %+v, want one", book.SeriesRefs)
	}
	got := book.SeriesRefs[0]
	if got.Title != "Vindserien" || got.Position != "21" || got.ForeignID != plain.ForeignID || !got.Primary {
		t.Errorf("got %+v, want Vindserien #21 as %s, primary", got, plain.ForeignID)
	}
}

// An author's works that share a series ask OpenLibrary about it once. A
// series OpenLibrary doesn't have is dropped and remembered; a failed lookup
// is dropped and remembered for seriesRetryAfter, then asked again.
func TestAuthorWorksSeriesLookups(t *testing.T) {
	var asked, missing, broken atomic.Int32
	works := `{"size":4,"entries":[
		{"key":"/works/OL1W","title":"Fjellvinden","series":[{"series":{"key":"/series/OL10L"},"position":"1"}]},
		{"key":"/works/OL2W","title":"Havgapet","series":[{"series":{"key":"/series/OL10L"},"position":"2"}]},
		{"key":"/works/OL3W","title":"Nordlys","series":[{"series":{"key":"/series/OL11L"},"position":"1"}]},
		{"key":"/works/OL4W","title":"Stormkast","series":[{"series":{"key":"/series/OL12L"},"position":"1"}]}]}`
	pt := &pathTransport{t: t, handlers: map[string]interface{}{
		"/authors/OL1A/works.json": works,
		"/search.json":             `{"docs":[]}`,
		"/series/OL10L.json":       func(*http.Request) string { asked.Add(1); return `{"name":"Vindserien"}` },
		"/series/OL11L.json":       func(*http.Request) string { missing.Add(1); return `{"error":"notfound"}` },
		"/series/OL12L.json":       func(*http.Request) string { broken.Add(1); return `{"error":"down"}` },
	}, status: map[string]int{"/series/OL11L.json": http.StatusNotFound, "/series/OL12L.json": http.StatusBadRequest}}
	c := &Client{http: &http.Client{Transport: pt}}

	for range 2 {
		books, err := c.GetAuthorWorks(context.Background(), "OL1A")
		if err != nil {
			t.Fatal(err)
		}
		byTitle := map[string][]string{}
		for _, b := range books {
			for _, r := range b.SeriesRefs {
				byTitle[b.Title] = append(byTitle[b.Title], r.Title+" #"+r.Position)
			}
		}
		if !slices.Equal(byTitle["Fjellvinden"], []string{"Vindserien #1"}) || !slices.Equal(byTitle["Havgapet"], []string{"Vindserien #2"}) {
			t.Errorf("series %v", byTitle)
		}
		if len(byTitle["Nordlys"]) != 0 || len(byTitle["Stormkast"]) != 0 {
			t.Errorf("an unresolved series was kept: %v", byTitle)
		}
	}
	if asked.Load() != 1 || missing.Load() != 1 {
		t.Errorf("lookups: found %d, missing %d; want each asked once over two syncs", asked.Load(), missing.Load())
	}
	if broken.Load() != 1 {
		t.Errorf("a failed lookup was asked %d times over two syncs, want 1 (remembered)", broken.Load())
	}
	prev := seriesRetryAfter
	seriesRetryAfter = 0
	t.Cleanup(func() { seriesRetryAfter = prev })
	if _, err := c.GetAuthorWorks(context.Background(), "OL1A"); err != nil {
		t.Fatal(err)
	}
	if broken.Load() != 2 {
		t.Errorf("a failed lookup was asked %d times once seriesRetryAfter passed, want 2", broken.Load())
	}
}

// A series endpoint answering 503 costs one lookup (with its retries), not
// one per work: 50 works in one series used to make 50 lookups of 4 requests
// each, and every 5xx holds the whole client.
func TestSeriesLookupOutageAskedOnce(t *testing.T) {
	defer providerhttp.SetBaseDelay(time.Millisecond)()
	var hits atomic.Int32
	entries := make([]string, 50)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"key":"/works/OL%dW","title":"Fjellvinden %d","series":[{"series":{"key":"/series/OL10L"},"position":"%d"}]}`, i+1, i+1, i+1)
	}
	pt := &pathTransport{t: t, handlers: map[string]interface{}{
		"/authors/OL1A/works.json": `{"size":50,"entries":[` + strings.Join(entries, ",") + `]}`,
		"/search.json":             `{"docs":[]}`,
		"/series/OL10L.json":       func(*http.Request) string { hits.Add(1); return `{"error":"unavailable"}` },
	}, status: map[string]int{"/series/OL10L.json": http.StatusServiceUnavailable}}
	c := &Client{http: &http.Client{Transport: pt}}

	books, err := c.GetAuthorWorks(context.Background(), "OL1A")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 50 {
		t.Fatalf("got %d works, want 50", len(books))
	}
	if n := hits.Load(); n < 1 || n > providerhttp.MaxRetries+1 {
		t.Errorf("50 works sharing a failing series made %d requests, want one lookup (at most %d)", n, providerhttp.MaxRetries+1)
	}
	for _, b := range books {
		if len(b.SeriesRefs) != 0 {
			t.Fatalf("%q kept an unresolved series: %+v", b.Title, b.SeriesRefs)
		}
	}
}

// A series name is trimmed, and capped, since it now comes from a network
// record rather than the work itself.
func TestSeriesNameTrimmedAndCapped(t *testing.T) {
	long := strings.Repeat("ø", maxSeriesNameRunes+40)
	c := newClientWithPaths(t, map[string]interface{}{
		"/series/OL1L.json": `{"name":"  Vindserien  "}`,
		"/series/OL2L.json": `{"name":"` + long + `"}`,
	})
	if got := c.seriesName(context.Background(), "/series/OL1L"); got != "Vindserien" {
		t.Errorf("name %q, want it trimmed", got)
	}
	if got := c.seriesName(context.Background(), "/series/OL2L"); utf8.RuneCountInString(got) != maxSeriesNameRunes {
		t.Errorf("name of %d runes, want %d", utf8.RuneCountInString(got), maxSeriesNameRunes)
	}
}
