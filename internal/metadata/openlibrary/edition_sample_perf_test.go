package openlibrary

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// countingTransport records every request URL (in order) and serves one canned
// editions payload. It also tracks peak in-flight concurrency so a test can
// tell a serial loop from a bounded fan-out.
type countingTransport struct {
	mu       sync.Mutex
	urls     []string
	inFlight int
	peak     int
	delay    time.Duration
	body     string
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.urls = append(c.urls, r.URL.String())
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()

	if c.delay > 0 {
		time.Sleep(c.delay)
	}

	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    r,
	}, nil
}

func (c *countingTransport) snapshot() ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	urls := make([]string, len(c.urls))
	copy(urls, c.urls)
	return urls, c.peak
}

// editionSampleBody is one edition carrying both a language and a cover, so a
// single response can satisfy both the language sampler and the cover sampler.
const editionSampleBody = `{"entries":[{"key":"/books/OL1M","title":"T","languages":[{"key":"/languages/eng"}],"covers":[42]}]}`

func newSamplingClient(rt http.RoundTripper) *Client {
	c := New()
	c.http = &http.Client{Transport: rt}
	return c
}

func authorWorkSet(n int) []models.Book {
	books := make([]models.Book, n)
	for i := range books {
		books[i] = models.Book{
			ForeignID:        "OL" + string(rune('A'+i%26)) + itoa(i) + "W",
			Title:            "Work",
			MetadataProvider: "openlibrary",
		}
	}
	return books
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// TestEditionSamplingRoundTripsPerWork pins the number of OpenLibrary requests
// the author-refresh pre-loop spends per work. FillMissingWorkLanguages and
// FillMissingWorkCovers both sample /works/{id}/editions.json?limit=5 — the
// same URL — so before #1888 a work missing both language and cover cost two
// identical round trips. One shared sample now serves both (#1888).
//
// Since #1779 the sample also reads the work record first, for its featured
// cover_edition. The canned body here has none, so each work costs exactly
// the work record plus the editions sample: two distinct requests, shared by
// both samplers, never repeated.
func TestEditionSamplingRoundTripsPerWork(t *testing.T) {
	const works = 65

	rt := &countingTransport{body: editionSampleBody}
	c := newSamplingClient(rt)
	books := authorWorkSet(works)

	if filled := c.FillMissingWorkLanguages(context.Background(), books); filled != works {
		t.Fatalf("language fill: got %d, want %d", filled, works)
	}
	if filled := c.FillMissingWorkCovers(context.Background(), books); filled != works {
		t.Fatalf("cover fill: got %d, want %d", filled, works)
	}

	urls, _ := rt.snapshot()
	if len(urls) != 2*works {
		t.Fatalf("edition sampling spent %d OpenLibrary round trips for %d works, want %d "+
			"(one work record and one shared sample per work, not one per sampler)", len(urls), works, 2*works)
	}

	seen := map[string]int{}
	for _, u := range urls {
		seen[u]++
	}
	for u, n := range seen {
		if n > 1 {
			t.Fatalf("duplicate round trip: %s requested %d times", u, n)
		}
	}
}

// TestEditionSamplingIsNotSerial pins the fan-out: before #1888 both samplers
// walked the book list one work at a time, so a 65-work author paid every
// OpenLibrary round trip in sequence. Asserting peak in-flight requests rather
// than wall clock keeps the test off the scheduler's timing.
func TestEditionSamplingIsNotSerial(t *testing.T) {
	rt := &countingTransport{body: editionSampleBody, delay: 20 * time.Millisecond}
	c := newSamplingClient(rt)
	books := authorWorkSet(65)

	c.FillMissingWorkLanguages(context.Background(), books)

	if _, peak := rt.snapshot(); peak < 2 {
		t.Fatalf("edition sampling ran serially (peak in-flight requests %d); "+
			"a 65-work author pays every OpenLibrary round trip in sequence", peak)
	}
}
