package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// scriptedAuthorProvider records every GetAuthor call and answers through fn,
// so a test decides per author whether the provider succeeds, fails or
// rate limits.
type scriptedAuthorProvider struct {
	mockMetaProvider
	mu    sync.Mutex
	calls []string
	fn    func(foreignID string) error
}

func (p *scriptedAuthorProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	p.mu.Lock()
	p.calls = append(p.calls, foreignID)
	fn := p.fn
	p.mu.Unlock()
	if fn != nil {
		if err := fn(foreignID); err != nil {
			return nil, err
		}
	}
	return &models.Author{ForeignID: foreignID, Name: foreignID, Description: "bio of " + foreignID}, nil
}

func (p *scriptedAuthorProvider) called() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *scriptedAuthorProvider) reset() {
	p.mu.Lock()
	p.calls = nil
	p.mu.Unlock()
}

type refreshFixture struct {
	t        *testing.T
	authors  *db.AuthorRepo
	provider *scriptedAuthorProvider
	s        *Scheduler
	ids      map[string]int64
}

func newRefreshFixture(t *testing.T, n int) *refreshFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	prev := metadataRefreshPace
	metadataRefreshPace = 0
	t.Cleanup(func() { metadataRefreshPace = prev })

	f := &refreshFixture{t: t, authors: db.NewAuthorRepo(database), provider: &scriptedAuthorProvider{}, ids: map[string]int64{}}
	for i := 0; i < n; i++ {
		fid := fmt.Sprintf("OL%03dA", i)
		a := &models.Author{ForeignID: fid, Name: fid, SortName: fid, MetadataProvider: "openlibrary", Monitored: true}
		if err := f.authors.Create(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		f.ids[fid] = a.ID
	}
	f.s = &Scheduler{authors: f.authors, meta: metadata.NewAggregator(f.provider)}
	return f
}

func (f *refreshFixture) stampAgo(fid string, ago time.Duration) {
	f.t.Helper()
	if err := f.authors.StampMetadataRefresh(context.Background(), f.ids[fid], time.Now().Add(-ago)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *refreshFixture) stamped(fid string) *time.Time {
	f.t.Helper()
	a, err := f.authors.GetByID(context.Background(), f.ids[fid])
	if err != nil || a == nil {
		f.t.Fatalf("reload %s: %v", fid, err)
	}
	return a.LastMetadataRefreshAt
}

// A tick refreshes the stalest authors first and only a bounded share of the
// library: ceil(60 / 24) = 3 of 60.
func TestRefreshMetadata_StalestFirstAndBounded(t *testing.T) {
	f := newRefreshFixture(t, 60)
	// Everyone was refreshed between 25h and 84h ago; index 7 is the stalest
	// stamped author, and two authors have never been refreshed.
	for i := 0; i < 60; i++ {
		fid := fmt.Sprintf("OL%03dA", i)
		switch i {
		case 11, 42:
			continue // never refreshed
		case 7:
			f.stampAgo(fid, 200*time.Hour)
		default:
			f.stampAgo(fid, time.Duration(25+i%50)*time.Hour)
		}
	}

	f.s.refreshMetadata()

	got := f.provider.called()
	want := []string{"OL011A", "OL042A", "OL007A"}
	if len(got) != len(want) {
		t.Fatalf("refreshed %d authors %v, want exactly %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refresh order = %v, want %v", got, want)
		}
	}
	for _, fid := range want {
		if f.stamped(fid) == nil || time.Since(*f.stamped(fid)) > time.Minute {
			t.Errorf("%s was refreshed but not stamped fresh: %v", fid, f.stamped(fid))
		}
	}
}

// Authors refreshed inside the last 24 hours (by an explicit refresh, an
// earlier tick, an import) are left alone.
func TestRefreshMetadata_SkipsRecentlyRefreshed(t *testing.T) {
	f := newRefreshFixture(t, 5)
	for fid := range f.ids {
		f.stampAgo(fid, 3*time.Hour)
	}
	f.s.refreshMetadata()
	if got := f.provider.called(); len(got) != 0 {
		t.Fatalf("fresh authors were refreshed again: %v", got)
	}
}

// The stamp is written only after the refresh succeeded: a failed author stays
// stale so it is retried, a successful one does not.
func TestRefreshMetadata_FailedAuthorNotStamped(t *testing.T) {
	f := newRefreshFixture(t, 48) // batch 2: the failing author and one more
	f.provider.fn = func(fid string) error {
		if fid == "OL000A" {
			return errors.New("author not found")
		}
		return nil
	}
	f.s.refreshMetadata()

	if len(f.provider.called()) == 0 {
		t.Fatal("no author was refreshed")
	}
	if got := f.stamped("OL000A"); got != nil {
		t.Errorf("failed author was stamped %v, want it left stale", got)
	}
	if got := f.stamped("OL001A"); got == nil {
		t.Error("successful author was not stamped")
	}
}

// Consecutive authors failing with the provider unavailable stop the pass, and
// none of them is stamped.
func TestRefreshMetadata_BreakerStopsAfterConsecutiveProviderFailures(t *testing.T) {
	f := newRefreshFixture(t, 96) // batch 4
	f.provider.fn = func(string) error { return fmt.Errorf("get author: %w", metadata.ErrUnavailable) }

	f.s.refreshMetadata()

	got := f.provider.called()
	if len(got) != metadataRefreshFailureBreaker {
		t.Fatalf("calls = %d (%v), want the pass to stop after %d consecutive failures", len(got), got, metadataRefreshFailureBreaker)
	}
	for fid := range f.ids {
		if f.stamped(fid) != nil {
			t.Errorf("%s stamped after a provider failure", fid)
		}
	}
}

// A rate limit stops the pass at once, stamps nobody, and the next tick starts
// again at the same author.
func TestRefreshMetadata_RateLimitDefersThenResumes(t *testing.T) {
	f := newRefreshFixture(t, 48) // batch 2
	limited := true
	f.provider.fn = func(string) error {
		if limited {
			return fmt.Errorf("get author: %w", metadata.ErrRateLimited)
		}
		return nil
	}

	f.s.refreshMetadata()
	first := f.provider.called()
	if len(first) != 1 {
		t.Fatalf("calls during a rate limit = %v, want the pass to stop at the first", first)
	}
	if f.stamped(first[0]) != nil {
		t.Fatalf("rate limited author %s was stamped", first[0])
	}

	limited = false
	f.provider.reset()
	f.s.refreshMetadata()
	second := f.provider.called()
	if len(second) != 2 || second[0] != first[0] {
		t.Fatalf("resumed tick refreshed %v, want it to start at %s", second, first[0])
	}
	for _, fid := range second {
		if f.stamped(fid) == nil {
			t.Errorf("%s not stamped after the resumed tick", fid)
		}
	}
}

// A held daily quota stops the pass before any request and leaves everyone
// stale (the existing CheckQuota guard, now with the stamp contract).
func TestRefreshMetadata_QuotaHoldStampsNobody(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authors := db.NewAuthorRepo(database)
	a := &models.Author{Name: "First", ForeignID: "hc:first", Monitored: true}
	if err := authors.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	p := &pausedRefreshProvider{}
	s := &Scheduler{authors: authors, meta: metadata.NewAggregator(p)}
	s.refreshMetadata()
	got, _ := authors.GetByID(context.Background(), a.ID)
	if got.LastMetadataRefreshAt != nil {
		t.Errorf("author stamped during a quota hold: %v", got.LastMetadataRefreshAt)
	}
}

// Authors are paced: two refreshes in one tick are at least one pace apart.
func TestRefreshMetadata_PacesAuthors(t *testing.T) {
	f := newRefreshFixture(t, 48) // batch 2
	metadataRefreshPace = 80 * time.Millisecond

	start := time.Now()
	f.s.refreshMetadata()
	elapsed := time.Since(start)

	if got := f.provider.called(); len(got) != 2 {
		t.Fatalf("calls = %v, want 2", got)
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("tick took %v, want at least one 80ms pace between two authors", elapsed)
	}
}

// One author that always fails for its own reasons must not hold the front of
// the queue forever: with a batch of one, the next tick moves on.
func TestRefreshMetadata_PermanentlyFailingAuthorDoesNotStarveOthers(t *testing.T) {
	f := newRefreshFixture(t, 3) // batch 1
	f.provider.fn = func(fid string) error {
		if fid == "OL000A" {
			return errors.New("author not found")
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		f.s.refreshMetadata()
	}
	for _, fid := range []string{"OL001A", "OL002A"} {
		if f.stamped(fid) == nil {
			t.Errorf("%s never refreshed behind the failing author; calls %v", fid, f.provider.called())
		}
	}
}

// Steady state: with 48 authors and one tick an hour, 24 ticks refresh every
// author exactly once.
func TestRefreshMetadata_DayOfTicksCoversEveryoneOnce(t *testing.T) {
	f := newRefreshFixture(t, 48)
	for i := 0; i < 24; i++ {
		before := len(f.provider.called())
		f.s.refreshMetadata()
		if got := len(f.provider.called()) - before; got != 2 {
			t.Fatalf("tick %d refreshed %d authors, want an even spread of 2", i, got)
		}
	}
	seen := map[string]int{}
	for _, fid := range f.provider.called() {
		seen[fid]++
	}
	if len(seen) != 48 {
		t.Fatalf("refreshed %d distinct authors in a day of ticks, want 48", len(seen))
	}
	for fid, n := range seen {
		if n != 1 {
			t.Errorf("%s refreshed %d times, want once", fid, n)
		}
	}
}
