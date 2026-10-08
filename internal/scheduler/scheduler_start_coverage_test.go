package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// countingPinger counts telemetry pings. Pings arrive from the startup
// goroutine and from the cron job, so the counter is atomic.
type countingPinger struct{ n atomic.Int32 }

func (p *countingPinger) Ping(context.Context) { p.n.Add(1) }

// recordingRecommender records the user id every run is attributed to.
type recordingRecommender struct {
	mu   sync.Mutex
	uids []int64
	err  error
}

func (r *recordingRecommender) Run(_ context.Context, uid int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uids = append(r.uids, uid)
	return r.err
}

// failingHCSyncer counts syncs and fails every one, so the job's error log
// path runs.
type failingHCSyncer struct{ n atomic.Int32 }

func (f *failingHCSyncer) Sync(context.Context) error {
	f.n.Add(1)
	return errors.New("hardcover unreachable")
}

type startFixture struct {
	s        *Scheduler
	settings *db.SettingsRepo
	logs     *db.LogRepo
	calibre  *stubCalibreSyncer
	rec      *recordingRecommender
	hc       *failingHCSyncer
	ping     *countingPinger
}

// newStartFixture wires every optional job Start can register onto a
// DB-backed scheduler built through New, with a nil app context so New's
// fallback is exercised too.
func newStartFixture(t *testing.T) *startFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	settings := db.NewSettingsRepo(database)
	history := db.NewHistoryRepo(database)
	// An empty library dir makes ScanLibrary record a "not configured" scan
	// result, which is the observable the scan-library job leaves behind.
	scanner := importer.NewScanner(downloads, clients, books, authors, history, "", "", "", "", "").WithSettings(settings)

	var nilCtx context.Context
	s := New(nilCtx, scanner, nil, nil, authors, books, db.NewIndexerRepo(database),
		downloads, clients, settings, db.NewBlocklistRepo(database))
	if s.ctx() == nil {
		t.Fatal("New with a nil app context must fall back to a usable context")
	}

	f := &startFixture{
		s:        s,
		settings: settings,
		logs:     db.NewLogRepo(database),
		calibre:  &stubCalibreSyncer{},
		rec:      &recordingRecommender{err: errors.New("engine failed")},
		hc:       &failingHCSyncer{},
		ping:     &countingPinger{},
	}
	s.WithDownloadClientHealth(downloader.NewHealthStore(), "")
	s.WithCalibreSyncer(f.calibre)
	s.WithRecommender(f.rec)
	s.WithOperatorUserID(func(context.Context) int64 { return 7 })
	s.WithHardcoverSyncer(f.hc)
	s.WithTelemetry(f.ping)
	s.WithLogRepo(f.logs, 0)
	return f
}

// runAllJobs invokes every registered cron job once, synchronously, in
// registration order. Nothing waits on the real schedule.
func runAllJobs(t *testing.T, c *cron.Cron) int {
	t.Helper()
	n := 0
	for id := cron.EntryID(1); ; id++ {
		e := c.Entry(id)
		if !e.Valid() {
			break
		}
		e.Job.Run()
		n++
	}
	return n
}

// TestStart_RunsEveryRegisteredJob registers the full job set (every optional
// dependency attached), stops the cron before anything fires, and then runs
// each job body directly. Each job must leave its own observable trace.
func TestStart_RunsEveryRegisteredJob(t *testing.T) {
	f := newStartFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, "recommendations.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, "log.retention_days", "3"); err != nil {
		t.Fatal(err)
	}
	old := db.LogEntry{TS: time.Now().Add(-10 * 24 * time.Hour), Level: "INFO", Component: "test", Message: "ancient"}
	fresh := db.LogEntry{TS: time.Now().Add(-time.Hour), Level: "INFO", Component: "test", Message: "recent"}
	for _, e := range []db.LogEntry{old, fresh} {
		if err := f.logs.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	f.s.Start()
	f.s.Stop()

	// Stop drains the startup ping goroutine, so exactly one ping happened.
	if got := f.ping.n.Load(); got != 1 {
		t.Fatalf("expected the startup telemetry ping once before Stop returned, got %d", got)
	}

	// check-downloads, check-stalled, download-client-health, search-wanted,
	// refresh-metadata, scan-library, calibre-sync, recommendations,
	// hardcover-sync, telemetry-ping, log-trim.
	if n := runAllJobs(t, f.s.cron); n != 11 {
		t.Fatalf("expected 11 registered jobs with every option attached, got %d", n)
	}

	if f.calibre.called != 1 {
		t.Errorf("calibre-sync job: expected 1 sync, got %d", f.calibre.called)
	}
	if len(f.rec.uids) != 1 || f.rec.uids[0] != 7 {
		t.Errorf("recommendations job: expected one run attributed to operator 7, got %v", f.rec.uids)
	}
	if got := f.hc.n.Load(); got != 1 {
		t.Errorf("hardcover-sync job: expected 1 sync, got %d", got)
	}
	if got := f.ping.n.Load(); got != 2 {
		t.Errorf("telemetry-ping job: expected a second ping from the job, got %d total", got)
	}

	rows, err := f.logs.Query(ctx, db.LogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Message != "recent" {
		t.Errorf("log-trim job: expected only the recent entry kept under a 3 day retention, got %+v", rows)
	}

	scan, err := f.settings.Get(ctx, "library.lastScan")
	if err != nil {
		t.Fatal(err)
	}
	if scan == nil || !strings.Contains(scan.Value, "library directory not configured") {
		t.Errorf("scan-library job: expected a not-configured scan result to be recorded, got %+v", scan)
	}
}

// TestStart_JobSettingsGates covers the settings each job reads on every run:
// with recommendations.enabled unset the nightly job must not call the engine,
// a zero operator id falls back to user 1 once it is enabled, and an
// unusable log.retention_days falls back to the WithLogRepo retention.
func TestStart_JobSettingsGates(t *testing.T) {
	f := newStartFixture(t)
	ctx := context.Background()
	f.s.WithOperatorUserID(func(context.Context) int64 { return 0 })
	f.s.WithLogRepo(f.logs, 30)
	if err := f.settings.Set(ctx, "log.retention_days", "not-a-number"); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{20 * 24 * time.Hour, 40 * 24 * time.Hour} {
		if err := f.logs.Insert(ctx, db.LogEntry{TS: time.Now().Add(-age), Level: "INFO", Message: age.String()}); err != nil {
			t.Fatal(err)
		}
	}
	f.s.Start()
	f.s.Stop()
	runAllJobs(t, f.s.cron)

	if len(f.rec.uids) != 0 {
		t.Fatalf("expected no recommendation run while disabled, got %v", f.rec.uids)
	}
	rows, err := f.logs.Query(ctx, db.LogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Message != (20*24*time.Hour).String() {
		t.Errorf("expected the 20 day old entry kept and the 40 day old one trimmed under 30 days, got %+v", rows)
	}

	if err := f.settings.Set(ctx, "recommendations.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	runAllJobs(t, f.s.cron)
	if len(f.rec.uids) != 1 || f.rec.uids[0] != 1 {
		t.Fatalf("expected one run under the fallback user 1, got %v", f.rec.uids)
	}
}

// TestOptionSetters_AssignRepos covers the With* setters that only store a
// dependency; each is read by the grab path, so a setter that dropped its
// argument would silently disable a feature.
func TestOptionSetters_AssignRepos(t *testing.T) {
	// The setters never touch the database, so the repos need no connection.
	var database *sql.DB
	s := &Scheduler{}
	dp := db.NewDelayProfileRepo(database)
	pr := db.NewPendingReleaseRepo(database)
	al := db.NewAuthorAliasRepo(database)
	mp := db.NewMetadataProfileRepo(database)
	ed := db.NewEditionRepo(database)
	lr := db.NewLogRepo(database)
	tp := &countingPinger{}
	op := func(context.Context) int64 { return 42 }

	s.WithDelayProfiles(dp)
	s.WithPendingReleases(pr)
	s.WithAliases(al)
	s.WithMetadataProfiles(mp)
	s.WithEditions(ed)
	s.WithTelemetry(tp)
	s.WithLogRepo(lr, 9)
	s.WithOperatorUserID(op)

	switch {
	case s.delayProfiles != dp:
		t.Error("WithDelayProfiles did not assign")
	case s.pending != pr:
		t.Error("WithPendingReleases did not assign")
	case s.aliases != al:
		t.Error("WithAliases did not assign")
	case s.profiles != mp:
		t.Error("WithMetadataProfiles did not assign")
	case s.editions != ed:
		t.Error("WithEditions did not assign")
	case s.telemetry != tp:
		t.Error("WithTelemetry did not assign")
	case s.logs != lr || s.logRetainDays != 9:
		t.Errorf("WithLogRepo did not assign: %v %d", s.logs == lr, s.logRetainDays)
	case s.operatorUserID == nil || s.operatorUserID(context.Background()) != 42:
		t.Error("WithOperatorUserID did not assign")
	}
}

// TestRunJob_RecoversPanic: a panicking job must not escape runJob, or one
// buggy job would tear down the cron goroutine.
func TestRunJob_RecoversPanic(t *testing.T) {
	ran := false
	job := runJob("panics", func() {
		ran = true
		panic("boom")
	})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runJob let a panic escape: %v", r)
		}
	}()
	job()
	if !ran {
		t.Fatal("job body never ran")
	}
}

// closedDB returns a database handle that has already been closed, so every
// query through a repo built on it fails. That is the cheapest way to drive
// the read-error branches: no migrations run.
func closedDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return database
}

// TestRefreshDownloadClientHealth_ListErrorLeavesStoreAlone: an unreadable
// client table must not publish a placeholder or probe anything.
func TestRefreshDownloadClientHealth_ListErrorLeavesStoreAlone(t *testing.T) {
	database := closedDB(t)
	store := downloader.NewHealthStore()
	s := &Scheduler{clients: db.NewDownloadClientRepo(database)}
	s.WithDownloadClientHealth(store, "")
	s.refreshDownloadClientHealth(context.Background())
	if h := store.Get(1); h != nil {
		t.Fatalf("expected no health entry after a failed client list, got %+v", h)
	}

	// No store attached: a no-op even with a working repo.
	s2 := &Scheduler{clients: db.NewDownloadClientRepo(database)}
	s2.refreshDownloadClientHealth(context.Background())
}

// TestRefreshDownloadClientHealth_PublishesForEnabledClients: an enabled
// client gets a health entry, a disabled one does not.
func TestRefreshDownloadClientHealth_PublishesForEnabledClients(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	clients := db.NewDownloadClientRepo(database)
	on := &models.DownloadClient{Name: "on", Type: "sabnzbd", Host: "127.0.0.1", Port: 1, Enabled: true}
	off := &models.DownloadClient{Name: "off", Type: "sabnzbd", Host: "127.0.0.1", Port: 1, Enabled: false}
	for _, c := range []*models.DownloadClient{on, off} {
		if err := clients.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	store := downloader.NewHealthStore()
	s := &Scheduler{clients: clients}
	s.WithDownloadClientHealth(store, "")
	// The client points at a closed loopback port, so the async probe fails
	// fast; the placeholder is published synchronously either way.
	s.refreshDownloadClientHealth(ctx)
	if store.Get(on.ID) == nil {
		t.Error("expected a health entry for the enabled client")
	}
	if store.Get(off.ID) != nil {
		t.Error("expected no health entry for the disabled client")
	}
}
