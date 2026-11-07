package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestWantedSearchQueue_UpcomingBookIsNotSearchedUntilItsDate is the #2523
// sweep rule: a Wanted book held until a future release date is left out of the
// scheduled search, and the same book is searched again once the date has
// passed, with nothing else changing.
func TestWantedSearchQueue_UpcomingBookIsNotSearchedUntilItsDate(t *testing.T) {
	f := newWantedQueueFixture(t)
	upcoming := f.book("Upcoming Volume", models.MediaTypeEbook)
	released := f.book("Released Volume", models.MediaTypeEbook)
	if err := f.books.SetSearchHold(f.ctx, upcoming.ID, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	queue := f.queue()
	if entryFor(queue, upcoming.ID) != nil {
		t.Error("an Upcoming book was queued for the scheduled search")
	}
	if entryFor(queue, released.ID) == nil {
		t.Error("a released book was dropped from the sweep")
	}

	if err := f.books.SetSearchHold(f.ctx, upcoming.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if entryFor(f.queue(), upcoming.ID) == nil {
		t.Error("the book was not searched after its release date arrived")
	}
}

type seriesJobFixture struct {
	repo   *db.SeriesRepo
	job    *seriesMonitorJob
	now    time.Time
	checks []int64
	out    func(s *models.Series) SeriesCheckOutcome
	ids    []int64
}

func newSeriesJobFixture(t *testing.T, monitored int) *seriesJobFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &seriesJobFixture{repo: db.NewSeriesRepo(database), now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}
	ctx := context.Background()
	for i := 0; i < monitored+1; i++ {
		s := &models.Series{ForeignID: "s" + string(rune('a'+i)), Title: "Series " + string(rune('A'+i))}
		if err := f.repo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, s.ID)
		if i < monitored {
			if err := f.repo.SetMonitoring(ctx, s.ID, true, models.SeriesMonitorUpcoming, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.out = func(*models.Series) SeriesCheckOutcome { return SeriesCheckOutcome{Status: models.SeriesCheckOK} }
	f.job = &seriesMonitorJob{
		store: f.repo,
		checker: SeriesCheckerFuncs{Check: func(_ context.Context, s *models.Series) SeriesCheckOutcome {
			f.checks = append(f.checks, s.ID)
			return f.out(s)
		}},
		interval: func() (time.Duration, bool) { return 168 * time.Hour, true },
		now:      func() time.Time { return f.now },
		// Every series in one tick, so cases are not about the spreading.
		batchSize: func(eligible int, _ time.Duration) int { return eligible },
	}
	return f
}

func TestSeriesMonitorJob_ChecksMonitoredOnlyAndOncePerInterval(t *testing.T) {
	old := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = old }()
	f := newSeriesJobFixture(t, 2)

	res := f.job.tick(context.Background())
	if res.Checked != 2 || len(f.checks) != 2 {
		t.Fatalf("first tick checked %d (%v), want the 2 monitored series", res.Checked, f.checks)
	}
	for _, id := range f.checks {
		if id == f.ids[2] {
			t.Error("an unmonitored series was checked")
		}
	}
	mon, err := f.repo.GetMonitoring(context.Background(), f.ids[0])
	if err != nil || mon == nil || mon.LastStatus != models.SeriesCheckOK || mon.LastCheckAt == nil {
		t.Fatalf("check was not recorded: %+v, %v", mon, err)
	}

	f.checks = nil
	f.now = f.now.Add(time.Hour)
	if res := f.job.tick(context.Background()); res.Checked != 0 {
		t.Errorf("a tick an hour later checked %d series, want 0", res.Checked)
	}
	f.now = f.now.Add(169 * time.Hour)
	if res := f.job.tick(context.Background()); res.Checked != 2 {
		t.Errorf("a tick after the interval checked %d series, want 2", res.Checked)
	}
}

func TestSeriesMonitorJob_OffDoesNothing(t *testing.T) {
	f := newSeriesJobFixture(t, 1)
	f.job.interval = func() (time.Duration, bool) { return 0, false }
	if res := f.job.tick(context.Background()); res.Skipped != "off" || len(f.checks) != 0 {
		t.Errorf("tick with the setting off = %+v, checks %v", res, f.checks)
	}
}

func TestSeriesMonitorJob_BackoffLeavesSeriesDue(t *testing.T) {
	old := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = old }()
	f := newSeriesJobFixture(t, 1)
	f.out = func(*models.Series) SeriesCheckOutcome {
		return SeriesCheckOutcome{Backoff: true, Err: errors.New("429")}
	}
	res := f.job.tick(context.Background())
	if !res.Backoff {
		t.Fatal("rate limit did not stop the pass")
	}
	if mon, _ := f.repo.GetMonitoring(context.Background(), f.ids[0]); mon == nil || mon.LastCheckAt != nil {
		t.Errorf("a rate limited series was stamped: %+v", mon)
	}
}

func TestSeriesMonitorJob_ProviderFailureRetriesSoonAndTripsBreaker(t *testing.T) {
	old := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = old }()
	f := newSeriesJobFixture(t, 4)
	f.out = func(*models.Series) SeriesCheckOutcome {
		return SeriesCheckOutcome{Err: errors.New("503"), Unavailable: true}
	}
	res := f.job.tick(context.Background())
	if !res.Breaker || res.Checked != discoveryFailureBreaker {
		t.Fatalf("tick = %+v, want the breaker after %d failures", res, discoveryFailureBreaker)
	}
	mon, _ := f.repo.GetMonitoring(context.Background(), f.ids[0])
	if mon == nil || mon.LastStatus != models.SeriesCheckFailed {
		t.Fatalf("failure not recorded: %+v", mon)
	}
	if want := f.now.Add(discoveryRetryAfter); !mon.NextCheckAt.Equal(want) {
		t.Errorf("next check = %v, want %v (retry, not a full interval)", mon.NextCheckAt, want)
	}
}

func TestSeriesMonitorJob_SkippedSeriesIsNotStamped(t *testing.T) {
	old := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = old }()
	f := newSeriesJobFixture(t, 1)
	f.out = func(*models.Series) SeriesCheckOutcome { return SeriesCheckOutcome{Skipped: true} }
	f.job.tick(context.Background())
	if mon, _ := f.repo.GetMonitoring(context.Background(), f.ids[0]); mon == nil || mon.LastCheckAt != nil {
		t.Errorf("a skipped series was stamped: %+v", mon)
	}
}
