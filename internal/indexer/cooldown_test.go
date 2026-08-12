package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func TestParseRetryHint(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want time.Duration
		ok   bool
	}{
		// The description that prompted #1934, verbatim from NZB.life.
		{"minutes, real-world", "indexer error 500: Request limit reached. Retry in 485 minutes.", 485 * time.Minute, true},
		{"singular unit", "Request limit reached. Retry in 1 hour", time.Hour, true},
		{"seconds", "Too many requests, retry in 30 seconds", 30 * time.Second, true},
		{"days", "Grab limit reached. Retry in 1 day.", 24 * time.Hour, true},
		{"case insensitive", "RETRY IN 5 MINUTES", 5 * time.Minute, true},
		{"space-free unit", "retry in 12minutes", 12 * time.Minute, true},
		// No hint at all is the common case: the caller falls back to the
		// default rather than treating it as "retry immediately".
		{"no hint", "Request limit reached", 0, false},
		{"unit we don't understand", "Retry in 3 fortnights", 0, false},
		{"number missing", "Retry in a while", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryHint(tc.desc)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCooldownClampsHint pins the two ends of the clamp: an indexer cannot
// bench itself for a month, and a zero-length hint cannot produce a cooldown
// that has already expired by the time it is stored.
func TestCooldownClampsHint(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		desc string
		want time.Duration
	}{
		{"absurdly long hint is capped", "Request limit reached. Retry in 30 days.", maxRateLimitCooldown},
		{"zero hint gets the floor", "Request limit reached. Retry in 0 minutes.", minRateLimitCooldown},
		{"no hint gets the default", "Request limit reached.", defaultRateLimitCooldown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &indexerCooldowns{now: func() time.Time { return now }}
			if !c.note(models.Indexer{ID: 1, Name: "idx"}, &newznab.IndexerError{Code: 500, Description: tc.desc}) {
				t.Fatal("note did not record a cooldown for a 500")
			}
			c.mu.Lock()
			got := c.entries[1].until.Sub(now)
			c.mu.Unlock()
			if got != tc.want {
				t.Errorf("cooldown = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCooldownExpires holds the deadline to the indexer's OWN hint: still held
// one minute before it, released one minute after. The hint (485 minutes) is
// deliberately far from defaultRateLimitCooldown, so a build that ignores the
// hint and always applies the default fails this rather than coincidentally
// passing it.
func TestCooldownExpires(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 7, Name: "NZB.life"}
	c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached. Retry in 485 minutes."})

	if _, held := c.active(idx); !held {
		t.Fatal("indexer is not in cooldown immediately after a 500")
	}
	now = now.Add(484 * time.Minute)
	reason, held := c.active(idx)
	if !held {
		t.Error("cooldown released a minute before the indexer said to come back")
	}
	if !strings.Contains(reason, "rate limited") {
		t.Errorf("reason = %q, want it to name the rate limit", reason)
	}
	now = now.Add(2 * time.Minute)
	if _, held := c.active(idx); held {
		t.Error("cooldown outlived the deadline the indexer gave")
	}
}

// TestCooldownClearedByIndexerEdit covers the recovery path: changing the
// indexer row (new API key, different account, re-enable) means the user wants
// it retried now, and must not be made to wait out a lockout that belonged to
// the old configuration.
func TestCooldownClearedByIndexerEdit(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 7, Name: "NZB.life", UpdatedAt: now.Add(-time.Hour)}
	c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached. Retry in 485 minutes."})

	if _, held := c.active(idx); !held {
		t.Fatal("indexer is not in cooldown after a 500")
	}
	edited := idx
	edited.UpdatedAt = now.Add(time.Minute)
	if _, held := c.active(edited); held {
		t.Error("an edited indexer is still held; the user's change should retry immediately")
	}
	// The entry is gone, not merely bypassed, so the un-edited copy is clear too.
	if _, held := c.active(idx); held {
		t.Error("the cooldown entry survived the edit")
	}
}

// TestCooldownIgnoresNonRateLimitErrors is the deliberate scope line of #1934:
// a suspended account (101) or a network failure must not bench the indexer on
// a timer. Auth failures need a human, and one who fixes their API key must see
// it work immediately (#1935).
func TestCooldownIgnoresNonRateLimitErrors(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	idx := models.Indexer{ID: 3, Name: "NZB Finder"}
	for _, err := range []error{
		&newznab.IndexerError{Code: 101, Description: "Account suspended"},
		&newznab.IndexerError{Code: 100, Description: "Incorrect user credentials"},
		context.DeadlineExceeded,
	} {
		c := &indexerCooldowns{now: func() time.Time { return now }}
		if c.note(idx, err) {
			t.Errorf("%v recorded a cooldown", err)
		}
		if _, held := c.active(idx); held {
			t.Errorf("%v put the indexer in cooldown", err)
		}
	}
}

// TestCooldownIgnoresUnidentifiedIndexers guards the map key: an indexer with
// no id has nothing stable to key on, and tracking them all under 0 would let
// one rate-limited indexer bench every other unsaved one.
func TestCooldownIgnoresUnidentifiedIndexers(t *testing.T) {
	c := &indexerCooldowns{}
	idx := models.Indexer{Name: "unsaved"}
	if c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached."}) {
		t.Error("recorded a cooldown against indexer id 0")
	}
	if _, held := c.active(idx); held {
		t.Error("an id-less indexer is in cooldown")
	}
}

// rateLimitedIndexer serves the Newznab 500 that NZB.life sends and counts how
// many times it was asked.
func rateLimitedIndexer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<error code="500" description="Request limit reached. Retry in 485 minutes."/>`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestSearchBookStopsQueryingARateLimitedIndexer is the behavioural pin for
// #1934: the second search must not reach an indexer that already said to come
// back in 485 minutes.
func TestSearchBookStopsQueryingARateLimitedIndexer(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook}

	s.SearchBook(context.Background(), idxs, crit)
	first := hits.Load()
	if first == 0 {
		t.Fatal("the first search never reached the indexer")
	}

	s.SearchBook(context.Background(), idxs, crit)
	if got := hits.Load(); got != first {
		t.Errorf("the indexer was queried %d more time(s) while in cooldown", got-first)
	}
}

// TestSearchBookWithDebugReportsCooldown pins the visibility half: the panel
// that showed the original error must also show why the indexer is no longer
// being queried, rather than silently dropping it from the list.
func TestSearchBookWithDebugReportsCooldown(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook}

	_, dbg := s.SearchBookWithDebug(context.Background(), idxs, crit)
	if len(dbg.Indexers) != 1 || dbg.Indexers[0].Error == "" {
		t.Fatalf("first search did not record the indexer error: %+v", dbg.Indexers)
	}
	first := hits.Load()

	_, dbg = s.SearchBookWithDebug(context.Background(), idxs, crit)
	if got := hits.Load(); got != first {
		t.Errorf("the indexer was queried %d more time(s) while in cooldown", got-first)
	}
	if len(dbg.Indexers) != 1 {
		t.Fatalf("the held indexer vanished from the debug output: %+v", dbg.Indexers)
	}
	entry := dbg.Indexers[0]
	if !entry.Skipped {
		t.Error("the held indexer is not marked skipped")
	}
	if !strings.Contains(entry.SkipReason, "rate limited") {
		t.Errorf("skipReason = %q, want it to explain the rate limit", entry.SkipReason)
	}
}

// TestCooldownIsSharedAcrossSearchPaths matters because one *Searcher is shared
// by the scheduler's auto-grab and the API's interactive search
// (cmd/bindery/main.go). A limit hit by one must be respected by the other, or
// the 12-hour wanted scan keeps hammering an indexer the user can see is held.
func TestCooldownIsSharedAcrossSearchPaths(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}

	s.SearchBookWithDebug(context.Background(), idxs, MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook})
	first := hits.Load()
	if first == 0 {
		t.Fatal("the interactive search never reached the indexer")
	}

	s.SearchBook(context.Background(), idxs, MatchCriteria{Title: "Lock In", Author: "John Scalzi", MediaType: models.MediaTypeEbook})
	s.SearchQuery(context.Background(), idxs, "scalzi")
	if got := hits.Load(); got != first {
		t.Errorf("auto-grab and freeform search sent %d more request(s) to a held indexer", got-first)
	}
}
