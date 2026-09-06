package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// quotaWindow is how far back the daily cap looks. Named rather than inlined
// because the store, the reset estimate and the prune all have to agree on it.
const quotaWindow = 24 * time.Hour

// quotaFlushInterval bounds how often a running tally is written back.
//
// A sweep over a few thousand wanted books issues tens of thousands of counted
// requests, and one UPDATE apiece would dwarf the searching. The tally is held
// in memory and flushed on this interval instead, which is the same trade
// healthRefreshInterval makes for the same reason. An unclean shutdown therefore
// loses under one interval's worth of counts — the cap can be overspent by that
// much once, which is a far smaller error than the #1934 cooldown's, since that
// loses its entire state on any restart.
const quotaFlushInterval = 30 * time.Second

// IndexerQuotaStore persists per-indexer query counts. Implemented by
// *db.IndexerRepo.
type IndexerQuotaStore interface {
	LoadQueryCounts(ctx context.Context, since time.Time) (map[int64]map[time.Time]int, error)
	AddQueryCount(ctx context.Context, id int64, hourStart time.Time, delta int) error
	PruneQueryCounts(ctx context.Context, before time.Time) error
}

// indexerQuota enforces Indexer.DailyQueryLimit across every search path (#2312).
//
// Persisting the tally is the point. #1934's cooldown lives in memory and is
// lost on restart, which is fine for a one-hour bench and useless for a budget
// measured in a day: an install that restarts nightly would never accumulate a
// count and the cap would never bind.
//
// Counts are bucketed by hour so the window rolls. A fixed window that resets in
// one step would let a sweep spend a full day's allowance the instant it flipped,
// which is the burst the cap exists to prevent, just on a 24-hour clock.
type indexerQuota struct {
	store IndexerQuotaStore

	mu sync.Mutex
	// loaded records that the stored buckets were read successfully. A failed
	// read deliberately leaves it false so the next search retries: latching it
	// on the attempt would mean one transient "database is locked" disabled the
	// cap for the lifetime of the process.
	loaded bool
	// counts is indexer id -> bucket start -> requests this process has counted
	// in that hour, seeded from the store on a successful load.
	counts map[int64]map[time.Time]int
	// flushed is how much of each bucket has already been written, so a flush
	// can send the delta rather than the total. Without it a process that never
	// managed to load would overwrite the stored buckets with its own small
	// numbers.
	flushed   map[int64]map[time.Time]int
	lastFlush map[int64]time.Time

	// now is injectable so tests can advance time without sleeping, matching
	// indexerCooldowns and indexerHealth.
	now func() time.Time
}

func (q *indexerQuota) clock() time.Time {
	if q != nil && q.now != nil {
		return q.now()
	}
	return time.Now()
}

// truncHour returns the bucket t belongs to. Kept in lockstep with
// db.TruncateQueryHour; both must agree or a flush would write a bucket the
// reader never sums.
func truncHour(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

// quotaLimit returns the effective cap for idx and whether one is set. A nil or
// non-positive limit means unlimited, so clearing the field in the UI and typing
// a zero both mean the same thing.
func quotaLimit(idx models.Indexer) (int, bool) {
	if idx.DailyQueryLimit == nil || *idx.DailyQueryLimit <= 0 {
		return 0, false
	}
	return *idx.DailyQueryLimit, true
}

// load reads the stored buckets once, on the first search that consults a cap.
// Doing it lazily keeps the wiring to a single WithQuota call with no startup
// step, the same shape WithHealth has.
func (q *indexerQuota) load(ctx context.Context) {
	if q.counts == nil {
		q.counts = make(map[int64]map[time.Time]int)
		q.flushed = make(map[int64]map[time.Time]int)
		q.lastFlush = make(map[int64]time.Time)
	}
	if q.loaded {
		return
	}

	stored, err := q.store.LoadQueryCounts(ctx, q.clock().Add(-quotaWindow))
	if err != nil {
		// Search anyway rather than refuse: an install that stops looking for
		// books because one read failed is far worse than one that briefly
		// undercounts. loaded stays false so the next search tries again, and
		// because flushes send deltas, the requests counted in the meantime are
		// still added to whatever the stored buckets already hold.
		slog.Warn("failed to load indexer query counts; retrying on the next search", "error", err)
		return
	}
	q.loaded = true

	// Seed both maps from the store: those requests are already persisted, so
	// only what happens from here is a delta this process owes the database.
	q.counts = stored
	q.flushed = make(map[int64]map[time.Time]int, len(stored))
	for id, buckets := range stored {
		copied := make(map[time.Time]int, len(buckets))
		for hour, n := range buckets {
			copied[hour] = n
		}
		q.flushed[id] = copied
	}
}

// usedLocked sums the buckets still inside the window and drops the ones that
// have aged out.
//
// The cutoff is rounded down to the hour, so the bucket the window boundary
// falls inside is counted in full and the sum can cover up to 25 hours rather
// than exactly 24. That errs towards counting too much, which is the safe
// direction: the cap exists so Bindery does not overspend someone else's
// allowance, and being slightly early to stop is a much cheaper mistake than
// being slightly late.
func (q *indexerQuota) usedLocked(id int64, now time.Time) int {
	buckets := q.counts[id]
	if buckets == nil {
		return 0
	}
	cutoff := truncHour(now.Add(-quotaWindow))
	total := 0
	for hour, n := range buckets {
		if hour.Before(cutoff) {
			delete(buckets, hour)
			delete(q.flushed[id], hour)
			continue
		}
		total += n
	}
	return total
}

// freesUpLocked estimates when the cap will next admit a request: the moment the
// oldest bucket still inside the window falls out of it. Only meaningful when
// the cap is currently reached.
func (q *indexerQuota) freesUpLocked(id int64, now time.Time) time.Duration {
	oldest := time.Time{}
	for hour := range q.counts[id] {
		if oldest.IsZero() || hour.Before(oldest) {
			oldest = hour
		}
	}
	if oldest.IsZero() {
		return 0
	}
	d := oldest.Add(quotaWindow + time.Hour).Sub(now)
	if d < time.Minute {
		d = time.Minute
	}
	return d.Round(time.Minute)
}

// hold reports whether idx has spent its daily cap, and if so a human-readable
// reason. The signature mirrors indexerCooldowns.active so the three fan-out
// loops can treat the two skips identically.
func (q *indexerQuota) hold(ctx context.Context, idx models.Indexer) (string, bool) {
	if q == nil || q.store == nil || idx.ID == 0 {
		return "", false
	}
	limit, capped := quotaLimit(idx)
	if !capped {
		return "", false
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.load(ctx)

	now := q.clock()
	used := q.usedLocked(idx.ID, now)
	if used < limit {
		return "", false
	}
	return fmt.Sprintf("daily query cap reached (%d of %d), frees up in %s",
		used, limit, q.freesUpLocked(idx.ID, now)), true
}

// budget returns the permission object the newznab client consults before each
// request. A nil return means unlimited, which is what an indexer with no cap
// and an unwired searcher both get.
func (q *indexerQuota) budget(idx models.Indexer) newznab.QueryBudget {
	if q == nil || q.store == nil || idx.ID == 0 {
		return nil
	}
	limit, capped := quotaLimit(idx)
	if !capped {
		return nil
	}
	return &quotaTicket{quota: q, id: idx.ID, limit: limit}
}

// quotaTicket bills one indexer's requests against its cap.
type quotaTicket struct {
	quota *indexerQuota
	id    int64
	limit int
}

// Take admits one request and counts it, or refuses once the window is full.
//
// Refusing here rather than only at the top of the fan-out is what makes the cap
// exact: a single BookSearch can issue up to eight requests, so a per-search
// check would overshoot by seven every time an indexer crossed the line
// mid-cascade.
func (t *quotaTicket) Take() bool {
	q := t.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	// Normally hold() has already loaded, since every fan-out checks the cap
	// before it starts searching. This covers the callers that go straight to a
	// budget, and is a no-op once loaded.
	q.load(context.Background())

	now := q.clock()
	if q.usedLocked(t.id, now) >= t.limit {
		return false
	}
	hour := truncHour(now)
	if q.counts[t.id] == nil {
		q.counts[t.id] = make(map[time.Time]int)
	}
	q.counts[t.id][hour]++
	return true
}

// flush persists everything idx has counted but not yet written, at most once
// per quotaFlushInterval. force skips the interval, so a refusal is durable
// immediately rather than after another half minute of a sweep that is no
// longer issuing requests.
//
// Every outstanding bucket is written, not just the current hour. Writing only
// the current one would permanently lose the tail of the previous hour whenever
// a sweep crossed an hour boundary between flushes, and that loss is bounded by
// how long the indexer stayed idle rather than by the flush interval.
func (q *indexerQuota) flush(ctx context.Context, idx models.Indexer, force bool) {
	if q == nil || q.store == nil || idx.ID == 0 {
		return
	}
	if _, capped := quotaLimit(idx); !capped {
		return
	}

	q.mu.Lock()
	if q.counts == nil {
		q.mu.Unlock()
		return
	}
	now := q.clock()
	if !force && now.Sub(q.lastFlush[idx.ID]) < quotaFlushInterval {
		q.mu.Unlock()
		return
	}
	pending := make(map[time.Time]int)
	for hour, n := range q.counts[idx.ID] {
		if delta := n - q.flushed[idx.ID][hour]; delta > 0 {
			pending[hour] = delta
		}
	}
	if len(pending) == 0 {
		q.mu.Unlock()
		return
	}
	q.lastFlush[idx.ID] = now
	// Mark the deltas as written before releasing the lock so a concurrent Take
	// counts against the next flush rather than this one. A write that then
	// fails puts its delta back below.
	if q.flushed[idx.ID] == nil {
		q.flushed[idx.ID] = make(map[time.Time]int)
	}
	for hour, delta := range pending {
		q.flushed[idx.ID][hour] += delta
	}
	q.mu.Unlock()

	// Detached context for the same reason indexerHealth uses one: the search
	// that spent the budget may be cancelled or on its way out, and losing the
	// count would let the next sweep spend it again.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for hour, delta := range pending {
		if err := q.store.AddQueryCount(writeCtx, idx.ID, hour, delta); err != nil {
			slog.Warn("failed to record indexer query count", "indexer", idx.Name, "error", err)
			q.mu.Lock()
			q.flushed[idx.ID][hour] -= delta
			q.mu.Unlock()
		}
	}
	if err := q.store.PruneQueryCounts(writeCtx, now.Add(-quotaWindow)); err != nil {
		slog.Debug("failed to prune indexer query counts", "error", err)
	}
}

// WithQuota attaches per-indexer daily query caps. Without it the searcher
// behaves exactly as before — no cap is consulted and no count is written —
// which is what keeps every existing test and every un-wired caller unaffected.
func (s *Searcher) WithQuota(store IndexerQuotaStore) *Searcher {
	s.quota = &indexerQuota{store: store}
	return s
}

// quotaHold reports whether idx has spent its daily cap.
func (s *Searcher) quotaHold(ctx context.Context, idx models.Indexer) (string, bool) {
	return s.quota.hold(ctx, idx)
}

// quotaBudget returns the budget to attach to idx's search context.
func (s *Searcher) quotaBudget(idx models.Indexer) newznab.QueryBudget {
	return s.quota.budget(idx)
}

// flushQuota persists idx's running count after a search leg finishes.
func (s *Searcher) flushQuota(ctx context.Context, idx models.Indexer, force bool) {
	s.quota.flush(ctx, idx, force)
}

// budgetExhausted reports whether err is the searcher refusing itself a request
// under the daily cap, rather than anything the indexer said.
//
// The distinction matters at every error branch in the fan-outs: routing this
// through noteIndexerError or health.recordFailure would put "daily query cap
// reached" in the Settings health banner as though the indexer had rejected us,
// and leave a healthy indexer looking broken for the rest of the window.
func budgetExhausted(err error) bool {
	return errors.Is(err, newznab.ErrQueryBudgetExhausted)
}
