package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Scheduled series monitoring (#2523).
//
// The job has the shape of author discovery (#2236): an hourly tick that
// checks a share of the monitored series so each is visited about once per
// interval, paced, with a time budget per series and a breaker on consecutive
// provider failures. It reuses that job's pace, batch sizing, breaker and
// budget constants. A check is the same catalogue diff and add path a manual
// Fill runs, and never searches or grabs.

// settingSeriesMonitoringInterval mirrors api.SettingSeriesMonitoringInterval.
const settingSeriesMonitoringInterval = "series.monitoring.interval"

// seriesCheckBudget bounds one series' check, like discoveryAuthorBudget. A var
// only so tests can shorten it.
var seriesCheckBudget = 5 * time.Minute

// SeriesCheckOutcome is what one series check reports back to the job. The
// scheduler imports neither internal/api nor the metadata providers, so the
// wiring in main.go translates their errors into these flags.
type SeriesCheckOutcome struct {
	// Skipped means the series was gone or no longer monitored: nothing is
	// stamped.
	Skipped bool
	// Status and Message are stored on the series for the UI.
	Status  string
	Message string
	Created int
	Err     error
	// Backoff means the provider refused to answer (a rate limit): the pass
	// stops and the series is not stamped.
	Backoff bool
	// Busy means a check of this series is already running: skipped, not stamped.
	Busy bool
	// Unavailable means Err is the provider's fault. Only these count toward
	// the breaker, and the series is retried sooner than a full interval.
	Unavailable bool
}

// SeriesChecker checks one series and says whether a bulk author refresh,
// which already walks the catalogue providers, is in progress.
type SeriesChecker interface {
	CheckSeries(ctx context.Context, series *models.Series) SeriesCheckOutcome
	BulkRefreshRunning() bool
}

// SeriesCheckerFuncs adapts two closures to SeriesChecker.
type SeriesCheckerFuncs struct {
	Check       func(ctx context.Context, series *models.Series) SeriesCheckOutcome
	BulkRunning func() bool
}

// CheckSeries implements SeriesChecker.
func (f SeriesCheckerFuncs) CheckSeries(ctx context.Context, series *models.Series) SeriesCheckOutcome {
	return f.Check(ctx, series)
}

// BulkRefreshRunning implements SeriesChecker.
func (f SeriesCheckerFuncs) BulkRefreshRunning() bool { return f.BulkRunning != nil && f.BulkRunning() }

// seriesMonitorStore is the part of the series repo the job reads and writes.
type seriesMonitorStore interface {
	CountMonitoringEligible(ctx context.Context) (int, error)
	ListMonitoringDue(ctx context.Context, now time.Time, limit int) ([]models.Series, error)
	StampCheck(ctx context.Context, seriesID int64, when, next time.Time, status, message string, created int) error
}

type seriesMonitorJob struct {
	store    seriesMonitorStore
	checker  SeriesChecker
	interval func() (time.Duration, bool)
	now      func() time.Time
	// batchSize overrides discoveryBatchSize in tests; nil in production.
	batchSize func(eligible int, interval time.Duration) int
}

type seriesTickResult struct {
	Skipped  string
	Stopped  string
	Checked  int
	Created  int
	TimedOut int
	Backoff  bool
	Breaker  bool
}

// WithSeriesChecker registers the hourly series-monitoring job. The job is
// registered even while checking is off, because each tick re-reads the
// setting, so an operator can turn it on without a restart. Needs the series
// repo (WithSeries); without it nothing is registered.
func (s *Scheduler) WithSeriesChecker(c SeriesChecker) {
	if c == nil || s.series == nil {
		return
	}
	job := &seriesMonitorJob{
		store:    s.series,
		checker:  c,
		interval: s.resolveSeriesMonitorInterval,
		now:      time.Now,
	}
	s.addJob("@every 1h", "series-monitoring", func() {
		job.tick(s.ctx())
	})
}

// resolveSeriesMonitorInterval reads series.monitoring.interval. It ships off:
// before this job the Monitored toggle did nothing, so an upgrade must not
// start adding books to every series someone toggled long ago. Only a stored
// duration turns it on. The bounds are author discovery's.
func (s *Scheduler) resolveSeriesMonitorInterval() (time.Duration, bool) {
	if s.settings == nil {
		return 0, false
	}
	v, _ := s.settings.Get(s.ctx(), settingSeriesMonitoringInterval)
	if v == nil || v.Value == "" || v.Value == "off" {
		return 0, false
	}
	return s.resolveInterval(settingSeriesMonitoringInterval, defaultDiscoveryInterval, minDiscoveryInterval, maxDiscoveryInterval), true
}

func (j *seriesMonitorJob) tick(ctx context.Context) seriesTickResult {
	start := j.now()
	var res seriesTickResult
	interval, on := j.interval()
	if !on {
		res.Skipped = "off"
		slog.Debug("job: series monitoring is off")
		return res
	}
	if j.checker.BulkRefreshRunning() {
		res.Skipped = "bulk refresh running"
		slog.Info("job: series monitoring skipped, a bulk author refresh is running")
		return res
	}
	eligible, err := j.store.CountMonitoringEligible(ctx)
	if err != nil {
		slog.Warn("job: series monitoring could not count series", "error", err)
		res.Skipped = "count failed"
		return res
	}
	if eligible == 0 {
		res.Skipped = "no monitored series"
		return res
	}
	batch := discoveryBatchSize(eligible, interval)
	if j.batchSize != nil {
		batch = j.batchSize(eligible, interval)
	}
	due, err := j.store.ListMonitoringDue(ctx, start, batch+discoverySpareAuthors)
	if err != nil {
		slog.Warn("job: series monitoring could not list due series", "error", err)
		res.Skipped = "list failed"
		return res
	}

	retry := discoveryRetryAfter
	if retry > interval {
		retry = interval
	}
	stamp := func(s models.Series, began time.Time, out SeriesCheckOutcome, next time.Duration) {
		status, msg := out.Status, out.Message
		if out.Err != nil && status == "" {
			status, msg = models.SeriesCheckFailed, "The check failed and will be retried."
		}
		if err := j.store.StampCheck(ctx, s.ID, began, j.now().Add(next), status, msg, out.Created); err != nil {
			slog.Warn("job: series monitoring could not record the check", "series", s.Title, "error", err)
		}
	}

	streak, attempted := 0, 0
	for i := range due {
		if attempted >= batch {
			break
		}
		if attempted > 0 && !sleepCtx(ctx, discoveryPace) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if j.checker.BulkRefreshRunning() {
			res.Stopped = "bulk refresh running"
			break
		}
		series := due[i]
		began := j.now()
		cctx, cancel := context.WithTimeout(ctx, seriesCheckBudget)
		out := j.checker.CheckSeries(cctx, &series)
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if ctx.Err() != nil {
			break
		}
		if out.Skipped || (out.Busy && !timedOut) {
			continue
		}
		attempted++
		res.Created += out.Created
		if out.Backoff {
			res.Backoff = true
			slog.Warn("job: series monitoring stopped, the metadata provider is rate limiting",
				"series", series.Title, "error", out.Err)
			break
		}
		res.Checked++
		switch {
		case timedOut:
			// Stamped like a check: retrying an hour later would stall the
			// slot the same way.
			res.TimedOut++
			streak++
			out.Status, out.Message = models.SeriesCheckTimedOut, "The check ran out of time and will be retried."
			stamp(series, began, out, interval)
		case out.Err != nil && out.Unavailable:
			streak++
			out.Status, out.Message = models.SeriesCheckFailed, "The metadata provider looks unavailable. Bindery will retry soon."
			slog.Warn("job: series monitoring failed, the provider looks unavailable", "series", series.Title, "error", out.Err)
			stamp(series, began, out, retry)
		case out.Err != nil:
			slog.Warn("job: series monitoring failed for a series", "series", series.Title, "error", out.Err)
			stamp(series, began, out, interval)
			streak = 0
		case out.Status == models.SeriesCheckNoLink || out.Status == models.SeriesCheckDisabled || out.Status == models.SeriesCheckAmbiguous || out.Status == models.SeriesCheckPartial:
			// Nothing was asked of the provider, and a link or setting is
			// fixed in a click, so look again soon rather than in a month.
			stamp(series, began, out, retry)
			streak = 0
		default:
			stamp(series, began, out, interval)
			streak = 0
		}
		if streak >= discoveryFailureBreaker {
			res.Breaker = true
			slog.Warn("job: series monitoring stopped after consecutive provider failures", "failures", streak)
			break
		}
	}
	slog.Info("job: series monitoring pass finished",
		"eligible", eligible, "batch", batch, "due", len(due), "checked", res.Checked, "created", res.Created,
		"timed_out", res.TimedOut, "backoff", res.Backoff, "breaker", res.Breaker, "stopped", res.Stopped,
		"elapsed", j.now().Sub(start).Round(time.Millisecond))
	return res
}
