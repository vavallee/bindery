package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// fakeDiscoveryAuthors is an in-memory discoveryAuthors whose reads and
// stamps can be made to fail, so the job's error branches run without a
// database.
type fakeDiscoveryAuthors struct {
	mu       sync.Mutex
	eligible int
	countErr error
	due      []models.Author
	listErr  error
	stampErr error
	limit    int
	stamped  map[int64]time.Time
	// onStamp, when set, runs after each successful stamp.
	onStamp func()
}

func (f *fakeDiscoveryAuthors) CountDiscoveryEligible(context.Context) (int, error) {
	return f.eligible, f.countErr
}

func (f *fakeDiscoveryAuthors) ListDiscoveryDue(_ context.Context, _ time.Time, limit int) ([]models.Author, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = limit
	return f.due, f.listErr
}

func (f *fakeDiscoveryAuthors) StampDiscovery(_ context.Context, id int64, when time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stampErr != nil {
		return f.stampErr
	}
	if f.stamped == nil {
		f.stamped = map[int64]time.Time{}
	}
	f.stamped[id] = when
	if f.onStamp != nil {
		f.onStamp()
	}
	return nil
}

func fakeDiscoveryJob(authors *fakeDiscoveryAuthors, disc *fakeDiscoverer) *discoveryJob {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	return &discoveryJob{
		authors:    authors,
		discoverer: disc,
		interval:   func() (time.Duration, bool) { return 24 * time.Hour, true },
		now:        func() time.Time { return now },
	}
}

func discoveryAuthorsNamed(ids ...string) []models.Author {
	out := make([]models.Author, len(ids))
	for i, id := range ids {
		out[i] = models.Author{ID: int64(i + 1), ForeignID: id, Name: id}
	}
	return out
}

// TestDiscoveryTick_ReadFailuresSkip: a count or list failure ends the tick
// with a reason and asks no provider anything.
func TestDiscoveryTick_ReadFailuresSkip(t *testing.T) {
	disc := &fakeDiscoverer{outcomes: map[string]DiscoveryOutcome{}}

	countFail := &fakeDiscoveryAuthors{countErr: errors.New("locked")}
	if res := fakeDiscoveryJob(countFail, disc).tick(context.Background()); res.Skipped != "count failed" {
		t.Errorf("count failure: got Skipped %q", res.Skipped)
	}

	listFail := &fakeDiscoveryAuthors{eligible: 3, listErr: errors.New("locked")}
	if res := fakeDiscoveryJob(listFail, disc).tick(context.Background()); res.Skipped != "list failed" {
		t.Errorf("list failure: got Skipped %q", res.Skipped)
	}
	if len(disc.calls) != 0 {
		t.Errorf("no author may be synced after a read failure, got %v", disc.calls)
	}
}

// TestDiscoveryTick_BatchSizeOverrideAndStampFailure: the test hook for the
// batch size is honoured (the list asks for batch plus the spare authors and
// only that many are synced), and a stamp that fails is logged rather than
// ending the pass.
func TestDiscoveryTick_BatchSizeOverrideAndStampFailure(t *testing.T) {
	prev := discoveryPace
	discoveryPace = 0
	t.Cleanup(func() { discoveryPace = prev })
	logs := captureLogs(t)

	authors := &fakeDiscoveryAuthors{
		eligible: 50,
		due:      discoveryAuthorsNamed("A", "B", "C", "D"),
		stampErr: errors.New("read-only database"),
	}
	disc := &fakeDiscoverer{outcomes: map[string]DiscoveryOutcome{"A": {Created: 1}, "B": {Created: 2}}}
	job := fakeDiscoveryJob(authors, disc)
	job.batchSize = func(int, time.Duration) int { return 2 }

	res := job.tick(context.Background())
	if authors.limit != 2+discoverySpareAuthors {
		t.Errorf("expected the due list limited to batch+spare (%d), got %d", 2+discoverySpareAuthors, authors.limit)
	}
	if res.Checked != 2 || res.Created != 3 {
		t.Errorf("expected 2 authors checked and 3 books created, got %+v", res)
	}
	if strings.Join(disc.calls, ",") != "A,B" {
		t.Errorf("expected exactly the batch synced in order, got %v", disc.calls)
	}
	if !strings.Contains(logs.String(), "could not record the check") {
		t.Errorf("expected the stamp failure logged; logs:\n%s", logs.String())
	}
}

// TestDiscoveryTick_CancelledMidPassWritesNothing: when the context is
// cancelled while an author is syncing (shutdown), the pass stops, the
// author is left for next time, and nothing is stamped.
func TestDiscoveryTick_CancelledMidPassWritesNothing(t *testing.T) {
	authors := &fakeDiscoveryAuthors{eligible: 2, due: discoveryAuthorsNamed("A", "B")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disc := &fakeDiscoverer{
		outcomes: map[string]DiscoveryOutcome{},
		run: func(context.Context, *models.Author, *DiscoveryOutcome) {
			cancel()
		},
	}

	res := fakeDiscoveryJob(authors, disc).tick(ctx)
	if res.Checked != 0 {
		t.Errorf("a cancelled pass must not count the interrupted author, got %+v", res)
	}
	if len(disc.calls) != 1 {
		t.Errorf("expected the pass to stop after the first author, got %v", disc.calls)
	}
	if len(authors.stamped) != 0 {
		t.Errorf("a shutdown must stamp nothing, got %v", authors.stamped)
	}
}

// TestDiscoveryTick_PaceInterruptedByCancel: with a real pace between
// authors, a cancel during the wait ends the pass after the first author.
func TestDiscoveryTick_PaceInterruptedByCancel(t *testing.T) {
	prev := discoveryPace
	discoveryPace = time.Hour
	t.Cleanup(func() { discoveryPace = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The cancel lands once A is checked and stamped, so it is the hour-long
	// pace before B that has to notice it.
	authors := &fakeDiscoveryAuthors{eligible: 2, due: discoveryAuthorsNamed("A", "B"), onStamp: cancel}
	disc := &fakeDiscoverer{outcomes: map[string]DiscoveryOutcome{}}
	done := make(chan discoveryTickResult, 1)
	go func() { done <- fakeDiscoveryJob(authors, disc).tick(ctx) }()
	select {
	case res := <-done:
		if res.Checked != 1 {
			t.Errorf("expected only the first author checked, got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pace wait ignored cancellation")
	}
	if len(disc.calls) != 1 {
		t.Errorf("expected B never synced, got %v", disc.calls)
	}
}

// TestSleepCtx covers the three outcomes: no wait, a full wait, and a wait
// cut short by cancellation.
func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), 0) {
		t.Error("a zero wait on a live context must report true")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(cancelled, 0) {
		t.Error("a zero wait on a cancelled context must report false")
	}
	if sleepCtx(cancelled, time.Hour) {
		t.Error("a cancelled context must cut the wait short and report false")
	}
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("an elapsed wait must report true")
	}
}
