package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Staggered author profile refresh (#2595).
//
// The refresh used to run once every 24 hours, list every author and call
// GetAuthor for each with no pacing and no freshness check, so a library of a
// thousand Hardcover authors spent a thousand requests in one burst, and a
// restart or an explicit refresh did not save the next burst any of them. It
// now ticks hourly and, like the discovery job (discovery.go), walks a bounded
// batch of the stalest authors per tick on a persisted cursor, here
// authors.last_metadata_refresh_at. The cursor is written only after an
// author's refresh succeeded, so an author that failed or was rate limited
// stays stale and is retried.
//
// This job refreshes the author's own profile only. Catalogue discovery,
// series refresh and edition hydration are other jobs and are untouched.
//
// It is global provider data, not user data, so like the loop it replaces it
// walks every user's authors without an owner filter.

const (
	// metadataRefreshInterval is how often each author's profile is due: one
	// day, the cadence of the job before it was staggered.
	metadataRefreshInterval = 24 * time.Hour

	// maxMetadataRefreshBatch caps how many authors one hourly tick refreshes.
	// Discovery caps at 25 (maxDiscoveryBatch) because one discovery check is
	// many provider calls; a profile refresh is one, so the same hour holds
	// four times as many for roughly the same load. 100 authors take about
	// five minutes of pace sleeps, well inside the hour, and cover 2,400
	// authors a day. A library past that stretches its cycle beyond a day
	// instead of growing the batch, as discovery does.
	maxMetadataRefreshBatch = 100

	// metadataRefreshSpareAuthors is how many due authors past the batch a
	// tick reads, so an author skipped by the cooldown or reloaded as no
	// longer eligible does not leave the tick short.
	metadataRefreshSpareAuthors = 3

	// metadataRefreshFailureBreaker is how many authors in a row may fail
	// with the provider unavailable before the pass stops. Consecutive
	// failures point at the provider, not the authors. The same count as
	// discoveryFailureBreaker.
	metadataRefreshFailureBreaker = 3

	// metadataRefreshAuthorCooldown is how long an author whose refresh failed
	// is passed over, when the failure is the author's and not the whole
	// provider's: not found, a record that does not parse, no provider for its
	// id, or a server error or timeout followed by an author that did answer.
	// It is not stamped, so without this it would head every tick's queue,
	// retried 24 times a day, and with a batch of one starve every author
	// behind it. A rate limit, a held quota or a tripped breaker leave the
	// authors uncooled. Kept in memory: a restart retries it at once.
	metadataRefreshAuthorCooldown = 6 * time.Hour
)

// metadataRefreshPace is the gap between two authors in one tick, so a batch
// does not burst the metadata provider. The same pace as discovery. A var only
// so tests can set it to 0.
var metadataRefreshPace = discoveryPace

// metadataRefreshBatchSize spreads the eligible authors over the 24 ticks of a
// day, so each is refreshed about once a day: ceil(eligible / 24), at least
// one and at most maxMetadataRefreshBatch.
func metadataRefreshBatchSize(eligible int) int {
	hours := int(metadataRefreshInterval / time.Hour)
	batch := (eligible + hours - 1) / hours
	if batch < 1 {
		batch = 1
	}
	if batch > maxMetadataRefreshBatch {
		batch = maxMetadataRefreshBatch
	}
	return batch
}

// coolingAuthors returns the authors still inside their cooldown, forgetting
// the ones whose cooldown has ended.
func (s *Scheduler) coolingAuthors(now time.Time) map[int64]bool {
	s.metadataCooldownMu.Lock()
	defer s.metadataCooldownMu.Unlock()
	for id, t := range s.metadataBreakerTrips {
		if now.Sub(t.at) > breakerTripMemory {
			delete(s.metadataBreakerTrips, id)
		}
	}
	out := make(map[int64]bool, len(s.metadataCooldown))
	for id, until := range s.metadataCooldown {
		if now.Before(until) {
			out[id] = true
		} else {
			delete(s.metadataCooldown, id)
		}
	}
	return out
}

func (s *Scheduler) coolAuthor(id int64, now time.Time) {
	s.metadataCooldownMu.Lock()
	defer s.metadataCooldownMu.Unlock()
	if s.metadataCooldown == nil {
		s.metadataCooldown = make(map[int64]time.Time)
	}
	s.metadataCooldown[id] = now.Add(metadataRefreshAuthorCooldown)
}

// metadataRefreshBreakerTripsToCool is how many tripped passes in a row an
// author may be part of before it is cooled like any author that keeps failing.
//
// A pass that trips the breaker cannot tell a provider outage from three
// authors the provider cannot answer for (a record it 500s on, a profile so
// large it times out): both look like three failures in a row. The first
// pass leaves them uncooled, so a real outage costs nothing. But if the same
// authors are the three stalest on every tick and trip it every time, they
// starve everyone behind them. Counting trips per author tells the two apart
// over time: an outage ends and the count is cleared by the first success,
// a bad record trips again and again.
//
// The threshold is 3, which at an hourly tick means an outage has to outlast
// two full ticks and still be going on the third before anyone is cooled.
// Two would cool after one hour of a continuing outage, and the authors it
// cools are the stalest, the ones a recovered provider should reach first.
// The price of 3 is that a persistently failing triple costs three wasted
// ticks (12 slots at a batch of 4) before the library moves again, once per
// cooldown of 6 hours. The worst case for an outage lasting many hours is
// bounded too: only the authors of a tripped pass are counted, so it cools at
// most 3 authors every 3 ticks, each for 6 hours, never the whole library.
const metadataRefreshBreakerTripsToCool = 3

// breakerTripMemory is how long a trip count is kept without a new trip, so
// the map is pruned with the cooldown map and cannot grow without bound.
const breakerTripMemory = 24 * time.Hour

type breakerTrip struct {
	n  int
	at time.Time
}

// noteBreakerTrip records that id was part of a pass that tripped the breaker
// and cools it once that has happened metadataRefreshBreakerTripsToCool times
// in a row, restarting its count.
func (s *Scheduler) noteBreakerTrip(id int64, now time.Time) {
	s.metadataCooldownMu.Lock()
	defer s.metadataCooldownMu.Unlock()
	if s.metadataBreakerTrips == nil {
		s.metadataBreakerTrips = make(map[int64]breakerTrip)
	}
	t := s.metadataBreakerTrips[id]
	t.n++
	t.at = now
	if t.n >= metadataRefreshBreakerTripsToCool {
		delete(s.metadataBreakerTrips, id)
		if s.metadataCooldown == nil {
			s.metadataCooldown = make(map[int64]time.Time)
		}
		s.metadataCooldown[id] = now.Add(metadataRefreshAuthorCooldown)
		return
	}
	s.metadataBreakerTrips[id] = t
}

// clearBreakerTrips forgets id's trips: the provider answered for it.
func (s *Scheduler) clearBreakerTrips(id int64) {
	s.metadataCooldownMu.Lock()
	defer s.metadataCooldownMu.Unlock()
	delete(s.metadataBreakerTrips, id)
}

func (s *Scheduler) breakerTripCount(id int64) int {
	s.metadataCooldownMu.Lock()
	defer s.metadataCooldownMu.Unlock()
	return s.metadataBreakerTrips[id].n
}

// refreshMetadata runs one tick of the author profile refresh: the batch of
// authors whose profile is oldest or missing, refreshed one at a time.
func (s *Scheduler) refreshMetadata() {
	ctx := s.ctx()
	start := time.Now()

	eligible, err := s.authors.CountMetadataRefreshEligible(ctx)
	if err != nil {
		slog.Error("failed to list authors due for metadata refresh (count)", "error", err)
		return
	}
	if eligible == 0 {
		return
	}
	batch := metadataRefreshBatchSize(eligible)
	cooling := s.coolingAuthors(start)
	due, err := s.authors.ListMetadataRefreshDue(ctx, start, start.Add(-metadataRefreshInterval), batch+metadataRefreshSpareAuthors+len(cooling))
	if err != nil {
		slog.Error("failed to list authors due for metadata refresh", "error", err)
		return
	}

	attempted, refreshed := 0, 0
	// pending holds the current streak of authors that failed with the
	// provider unavailable. They are not yet known to be at fault: a success
	// or an author specific error after them says the provider is fine and
	// they are the problem, so they are cooled like any author that fails for
	// its own reasons. A tripped breaker or a rate limit says the provider as
	// a whole is down, and they are left alone so they head the next tick.
	var pending []int64
	flushPending := func() {
		now := time.Now()
		for _, id := range pending {
			s.coolAuthor(id, now)
		}
		pending = nil
	}
	stopped := ""
loop:
	for _, listed := range due {
		if attempted >= batch {
			break
		}
		if cooling[listed.ID] {
			continue
		}
		if attempted > 0 && !sleepCtx(ctx, metadataRefreshPace) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if err := s.meta.CheckQuota(ctx); err != nil {
			slog.Warn("metadata refresh stopped", "error", err)
			stopped = "quota"
			break
		}

		// Re-read the row rather than writing back the snapshot listed
		// before the pace sleeps: an edit saved meanwhile must neither be
		// overwritten nor cost this author its refresh (#2926). An author
		// deleted, unmonitored or turned into a Calibre shell since is
		// skipped without a provider call and without using a slot.
		current, err := s.authors.GetByID(ctx, listed.ID)
		if err != nil {
			slog.Warn("failed to reload author for refresh", "author", listed.Name, "error", err)
			continue
		}
		if current == nil || !current.Monitored || hasCalibrePrefix(current.ForeignID) {
			continue
		}
		author := *current
		attempted++

		updated, err := s.meta.GetAuthor(ctx, author.ForeignID)
		switch {
		case err != nil && (metadata.IsProviderHold(err) || errors.Is(err, metadata.ErrRateLimited)):
			// The provider refused to answer. Stop the pass; the author is
			// not stamped, so it heads the next tick.
			slog.Warn("metadata refresh stopped, the metadata provider is rate limiting",
				"author", author.Name, "refreshed", refreshed, "error", err)
			stopped = "rate limited"
			break loop
		case err != nil && metadata.IsProviderUnavailable(err):
			pending = append(pending, author.ID)
			slog.Warn("failed to refresh author, the provider looks unavailable", "author", author.Name, "error", err)
			if len(pending) >= metadataRefreshFailureBreaker {
				slog.Warn("metadata refresh stopped after consecutive provider failures; those authors retry next tick",
					"failures", len(pending))
				for _, id := range pending {
					s.noteBreakerTrip(id, time.Now())
				}
				pending = nil
				stopped = "breaker"
				break loop
			}
			continue
		case err != nil:
			// About this author, not the provider.
			flushPending()
			s.clearBreakerTrips(author.ID)
			slog.Warn("failed to refresh author", "author", author.Name, "error", err)
			s.coolAuthor(author.ID, time.Now())
			continue
		}
		flushPending()
		s.clearBreakerTrips(author.ID)
		// Aggregator returns (nil, nil) when no configured provider owns this
		// foreignID prefix (e.g. a Hardcover-prefixed author after Hardcover
		// was disabled, or any author whose ForeignID prefix no longer maps to
		// a provider). Without this guard the deref below panics, which exits
		// the range loop and kills every subsequent author's refresh that
		// cycle.
		if updated == nil {
			slog.Debug("no metadata provider for author, skipping refresh", "author", author.Name, "foreignID", author.ForeignID)
			s.coolAuthor(author.ID, time.Now())
			continue
		}

		if s.persistRefreshedAuthor(ctx, &author, updated, current.UpdatedAtRaw) {
			refreshed++
			slog.Debug("refreshed author", "author", author.Name)
		}
	}

	// The pass ended with a streak shorter than the breaker and no success
	// after it (a short batch): those authors, not the provider, are the
	// likeliest cause, and leaving them uncooled would send the same ones to
	// the head of every tick. A rate limit or a tripped breaker cleared the
	// streak already; a shutdown writes nothing.
	if stopped == "" && ctx.Err() == nil {
		flushPending()
	}

	slog.Info("job: author profile refresh pass finished",
		"eligible", eligible, "batch", batch, "due", len(due), "attempted", attempted,
		"refreshed", refreshed, "stopped", stopped, "elapsed", time.Since(start).Round(time.Millisecond))
}

// persistRefreshedAuthor merges the provider's profile into author and writes
// it together with the freshness stamp, reporting whether it was written. The
// stamp rides in the same guarded write as the profile, so it exists only for
// a refresh that was persisted: a failed save, or an author edited while the
// provider was being asked, stays stale and is retried by a later tick.
func (s *Scheduler) persistRefreshedAuthor(ctx context.Context, author, updated *models.Author, expectedUpdatedAt string) bool {
	// Update changed fields. Only overwrite when the upstream record
	// actually carries a value: a sparse OpenLibrary refresh must not
	// clobber a previously enriched description/ratings with empties
	// (#1463). Same guard the ImageURL update has always had.
	// MergeProfileField also keeps an enriched photo or bio (#3287) from
	// displacing one the primary provider or a user supplied.
	models.MergeProfileField(&author.Description, &author.DescriptionSource, updated.Description, updated.DescriptionSource)
	models.MergeProfileField(&author.ImageURL, &author.ImageSource, updated.ImageURL, updated.ImageSource)
	if updated.AverageRating != 0 {
		author.AverageRating = updated.AverageRating
	}
	if updated.RatingsCount != 0 {
		author.RatingsCount = updated.RatingsCount
	}
	now := time.Now().UTC()
	author.LastMetadataRefreshAt = &now

	// Guarded on the row as read before the provider call, so an edit saved
	// during the call wins. The refresh is dropped, not retried now: the
	// author is still stale, so a later tick picks it up.
	written, err := s.authors.UpdateIfUnchanged(ctx, author, expectedUpdatedAt)
	if err != nil {
		slog.Warn("failed to persist refreshed author", "author", author.Name, "error", err)
		s.coolAuthor(author.ID, now)
		return false
	}
	if !written {
		slog.Info("author refresh skipped: the author changed while its metadata was being fetched; a later tick retries it",
			"author", author.Name, "authorId", author.ID)
		return false
	}
	return true
}

// hasCalibrePrefix reports a Calibre-imported author. Those have synthetic
// "calibre:author:N" IDs with no counterpart in OL/Hardcover; skipping them
// avoids noisy 404 errors.
func hasCalibrePrefix(foreignID string) bool {
	return strings.HasPrefix(foreignID, "calibre:")
}
