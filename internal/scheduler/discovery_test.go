package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestDiscoveryBatchSize(t *testing.T) {
	week := 168 * time.Hour
	cases := []struct {
		eligible int
		interval time.Duration
		want     int
	}{
		{eligible: 1, interval: week, want: 1},
		{eligible: 168, interval: week, want: 1},
		{eligible: 169, interval: week, want: 2},
		{eligible: 500, interval: week, want: 3},
		{eligible: 50, interval: 24 * time.Hour, want: 3},
		{eligible: 1000, interval: 24 * time.Hour, want: 25},
		{eligible: 100000, interval: 720 * time.Hour, want: 25},
		{eligible: 0, interval: week, want: 1},
		{eligible: 10, interval: 30 * time.Minute, want: 10},
	}
	for _, tc := range cases {
		if got := discoveryBatchSize(tc.eligible, tc.interval); got != tc.want {
			t.Errorf("discoveryBatchSize(%d, %s) = %d, want %d", tc.eligible, tc.interval, got, tc.want)
		}
	}
}

// fakeDiscoverer answers per author from outcomes, records who it was asked
// about, and reports bulk as its bulk refresh state.
type fakeDiscoverer struct {
	mu       sync.Mutex
	outcomes map[string]DiscoveryOutcome
	calls    []string
	bulk     bool
	// run, when set, is called for an author before its outcome is
	// returned, and may replace the outcome.
	run func(ctx context.Context, a *models.Author, out *DiscoveryOutcome)
}

func (f *fakeDiscoverer) DiscoverAuthor(ctx context.Context, a *models.Author) DiscoveryOutcome {
	f.mu.Lock()
	f.calls = append(f.calls, a.ForeignID)
	out := f.outcomes[a.ForeignID]
	run := f.run
	f.mu.Unlock()
	if run != nil {
		run(ctx, a, &out)
	}
	return out
}

func (f *fakeDiscoverer) BulkRefreshRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bulk
}

type discoveryJobFixture struct {
	repo    *db.AuthorRepo
	job     *discoveryJob
	disc    *fakeDiscoverer
	now     time.Time
	authors map[string]*models.Author
}

// newDiscoveryJobFixture creates the named monitored authors, never checked,
// and a job over them with a fixed clock and a weekly interval.
func newDiscoveryJobFixture(t *testing.T, foreignIDs ...string) *discoveryJobFixture {
	t.Helper()
	prev := discoveryPace
	discoveryPace = 0
	t.Cleanup(func() { discoveryPace = prev })

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &discoveryJobFixture{
		repo:    db.NewAuthorRepo(database),
		disc:    &fakeDiscoverer{outcomes: map[string]DiscoveryOutcome{}},
		now:     time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
		authors: map[string]*models.Author{},
	}
	for _, id := range foreignIDs {
		a := &models.Author{ForeignID: id, Name: id, SortName: id, Monitored: true}
		if err := f.repo.Create(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		f.authors[id] = a
	}
	f.job = &discoveryJob{
		authors:    f.repo,
		discoverer: f.disc,
		interval:   func() (time.Duration, bool) { return 168 * time.Hour, true },
		now:        func() time.Time { return f.now },
	}
	return f
}

func (f *discoveryJobFixture) cursor(t *testing.T, foreignID string) *time.Time {
	t.Helper()
	got, err := f.repo.LastDiscoveryAt(context.Background(), f.authors[foreignID].ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An ordinary error still stamps the author, so one broken author cannot
// hold the front of the queue; a success stamps too.
func TestDiscoveryTick_StampsOnSuccessAndOrdinaryError(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Created: 2}
	f.disc.outcomes["OL2A"] = DiscoveryOutcome{Err: errors.New("provider 500")}

	res := f.job.tick(context.Background())
	if res.Checked != 2 || res.Created != 2 || res.Backoff {
		t.Fatalf("tick = %+v, want 2 checked, 2 created, no backoff", res)
	}
	for _, id := range []string{"OL1A", "OL2A"} {
		if got := f.cursor(t, id); got == nil || !got.Equal(f.now) {
			t.Errorf("%s cursor = %v, want %v", id, got, f.now)
		}
	}
}

// A rate limit stops the pass at once and leaves the refused author, and
// everyone after it, unstamped.
func TestDiscoveryTick_BackoffStopsAndLeavesCursor(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A")
	// Batch size for 3 authors over a week is 1, so widen the batch by making
	// the interval short enough to take them all.
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Created: 1}
	// A held catalogue run can create books before it stops; they still count.
	f.disc.outcomes["OL2A"] = DiscoveryOutcome{Created: 2, Backoff: true, Err: errors.New("rate limited")}

	res := f.job.tick(context.Background())
	if !res.Backoff || res.Checked != 1 || res.Created != 3 {
		t.Fatalf("tick = %+v, want backoff after 1 checked with 3 created", res)
	}
	if len(f.disc.calls) != 2 {
		t.Fatalf("discoverer called for %v, want the pass to stop at the refused author", f.disc.calls)
	}
	if f.cursor(t, "OL1A") == nil {
		t.Error("the author checked before the rate limit was not stamped")
	}
	for _, id := range []string{"OL2A", "OL3A"} {
		if got := f.cursor(t, id); got != nil {
			t.Errorf("%s was stamped (%v) although the pass backed off before checking it", id, got)
		}
	}
}

// An author whose sync is already running is skipped without a stamp, and the
// pass carries on.
func TestDiscoveryTick_BusyAuthorSkippedWithoutStamp(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Busy: true}

	res := f.job.tick(context.Background())
	if res.Checked != 1 {
		t.Fatalf("tick = %+v, want 1 checked", res)
	}
	if got := f.cursor(t, "OL1A"); got != nil {
		t.Errorf("busy author was stamped: %v", got)
	}
	if f.cursor(t, "OL2A") == nil {
		t.Error("the author after the busy one was not checked")
	}
}

func TestDiscoveryTick_SkipsWhileBulkRefreshRuns(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A")
	f.disc.bulk = true
	res := f.job.tick(context.Background())
	if res.Skipped == "" || len(f.disc.calls) != 0 {
		t.Fatalf("tick = %+v with calls %v, want a skipped tick and no discovery", res, f.disc.calls)
	}
}

func TestDiscoveryTick_OffDoesNothing(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A")
	f.job.interval = func() (time.Duration, bool) { return 0, false }
	res := f.job.tick(context.Background())
	if res.Skipped != "off" || len(f.disc.calls) != 0 {
		t.Fatalf("tick = %+v with calls %v, want off and no discovery", res, f.disc.calls)
	}
}

// Due date logic on the injected clock: an author checked within the interval
// is not visited again until the clock passes it.
func TestDiscoveryTick_HonoursIntervalOnInjectedClock(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A")
	if res := f.job.tick(context.Background()); res.Checked != 1 {
		t.Fatalf("first tick = %+v, want the never checked author visited", res)
	}
	f.now = f.now.Add(167 * time.Hour)
	if res := f.job.tick(context.Background()); res.Checked != 0 {
		t.Fatalf("tick inside the interval = %+v, want nothing due", res)
	}
	f.now = f.now.Add(2 * time.Hour)
	if res := f.job.tick(context.Background()); res.Checked != 1 {
		t.Fatalf("tick past the interval = %+v, want the author due again", res)
	}
}

func TestResolveDiscoveryInterval(t *testing.T) {
	cases := []struct {
		value  string
		wantOn bool
		want   time.Duration
	}{
		{"", false, 0},
		{"off", false, 0},
		{"24h", true, 24 * time.Hour},
		{"720h", true, 720 * time.Hour},
		{"1h", true, 168 * time.Hour},
		{"nonsense", true, 168 * time.Hour},
	}
	if d, on := (&Scheduler{}).resolveDiscoveryInterval(); on || d != 0 {
		t.Errorf("no settings repo: got (%s, %v), want discovery off", d, on)
	}
	s := schedulerWithSetting(t, true, settingAuthorDiscoveryInterval, "")
	for _, tc := range cases {
		if err := s.settings.Set(context.Background(), settingAuthorDiscoveryInterval, tc.value); err != nil {
			t.Fatal(err)
		}
		got, on := s.resolveDiscoveryInterval()
		if on != tc.wantOn || got != tc.want {
			t.Errorf("value %q: got (%s, %v), want (%s, %v)", tc.value, got, on, tc.want, tc.wantOn)
		}
	}
}

// Discovery ships off: with nothing stored under authors.discovery.interval
// the registered hourly job must make no provider call and stamp no author,
// and storing an interval must turn it on without a restart, since the job
// re-reads the setting on every tick.
func TestDiscovery_OffUntilAnIntervalIsStored(t *testing.T) {
	prev := discoveryPace
	discoveryPace = 0
	t.Cleanup(func() { discoveryPace = prev })

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL2236A", Name: "Ann Leckie", SortName: "Leckie, Ann", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	disc := &fakeDiscoverer{outcomes: map[string]DiscoveryOutcome{}}
	s := &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		authors:  authors,
		settings: db.NewSettingsRepo(database),
	}
	s.WithAuthorDiscoverer(disc)
	runTick := func() {
		for _, e := range s.cron.Entries() {
			e.Job.Run()
		}
	}

	runTick()
	if len(disc.calls) != 0 {
		t.Fatalf("discovery ran with no interval stored: %v", disc.calls)
	}
	if when, err := authors.LastDiscoveryAt(ctx, author.ID); err != nil || when != nil {
		t.Fatalf("author stamped with no interval stored: %v, %v", when, err)
	}

	if err := s.settings.Set(ctx, settingAuthorDiscoveryInterval, "168h"); err != nil {
		t.Fatal(err)
	}
	runTick()
	if len(disc.calls) != 1 {
		t.Fatalf("discovery calls after storing an interval = %v, want the author checked", disc.calls)
	}
	if when, err := authors.LastDiscoveryAt(ctx, author.ID); err != nil || when == nil {
		t.Fatalf("author not stamped after a run: %v, %v", when, err)
	}
}

func TestWithAuthorDiscoverer_RegistersHourlyJobOnlyWhenWired(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	s := &Scheduler{cron: cron.New(cron.WithSeconds()), authors: db.NewAuthorRepo(database)}
	s.WithAuthorDiscoverer(nil)
	if n := len(s.cron.Entries()); n != 0 {
		t.Fatalf("nil discoverer registered %d jobs", n)
	}
	s.WithAuthorDiscoverer(&fakeDiscoverer{})
	entries := s.cron.Entries()
	if len(entries) != 1 || !hasEntryWithDelay(entries, time.Hour) {
		t.Fatalf("entries = %d, want one hourly author-discovery job", len(entries))
	}
}
