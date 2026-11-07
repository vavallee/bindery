package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
)

// Scheduled series monitoring (#2523).
//
// A monitored series is checked on a schedule: the linked Hardcover catalogue
// is diffed against the library by buildHardcoverDiff, the same function a
// manual Fill uses, so box sets and split edition parts (#2524) are already
// out of the Missing set. What is left is added through
// ensureHardcoverCatalogBook, the same add path Fill uses, as a monitored
// Wanted book. The check never searches or grabs: a created book reaches an
// indexer only through the wanted sweep, which holds an Upcoming book back
// until its release date.

// SettingSeriesMonitoringInterval is the KV key for how often each monitored
// series is checked (#2523). Value is "off" or a Go duration bounded to
// [24h, 720h]. Unset and "off" both mean no scheduled checking: before this
// job the Monitored toggle did nothing, so an upgrade must not start adding
// books to every series someone toggled long ago. Read on every hourly tick.
const SettingSeriesMonitoringInterval = "series.monitoring.interval"

// ErrSeriesCheckRunning is returned by CheckSeries when a check of the same
// series is already in flight.
var ErrSeriesCheckRunning = errors.New("a check of this series is already running")

// SeriesCheckResult is what one check did, for the job to stamp and log.
type SeriesCheckResult struct {
	// Status is one of the models.SeriesCheck* values; empty when the series
	// was skipped because it is gone or no longer monitored.
	Status string
	// Message explains a non-ok Status in a sentence.
	Message string
	// Created is how many books the check added.
	Created int
	// Upcoming is how many of those (or already held ones) are Upcoming.
	Upcoming int
	// Held counts catalogue rows left alone because their identity is
	// ambiguous, or because the mode does not add them.
	Held int
}

type seriesJobOwnerKey struct{}

// withSeriesJobOwner marks ctx as an unattended series check on behalf of
// owner, so the add path refuses to attach a book to another account's author
// or reuse another account's book. 0 means no restriction.
func withSeriesJobOwner(ctx context.Context, owner int64) context.Context {
	return context.WithValue(ctx, seriesJobOwnerKey{}, owner)
}

func seriesJobOwner(ctx context.Context) int64 {
	owner, _ := ctx.Value(seriesJobOwnerKey{}).(int64)
	return owner
}

// seriesJobMayUse reports whether a row owned by rowOwner (0 = unowned) may be
// used by the current series check. Outside a check it is always true.
func seriesJobMayUse(ctx context.Context, rowOwner int64) bool {
	owner := seriesJobOwner(ctx)
	return owner == 0 || rowOwner == 0 || rowOwner == owner
}

// WithSeriesCheckMediaType sets how the media type of books a series check
// adds is resolved, normally the install default. Without it they are ebooks.
func (h *SeriesHandler) WithSeriesCheckMediaType(f func(ctx context.Context) string) *SeriesHandler {
	h.checkMediaType = f
	return h
}

// seriesCheckQualifies is the add policy. All adds every missing book.
// Upcoming adds a book only when its release date is known and either still
// ahead or later than the last check that read the whole catalogue, so a book
// announced and released between two good checks is not lost, even when a
// failed check fell in between. A missing date never qualifies for
// Upcoming: with no date there is no evidence the book is new, and the cost
// of being wrong is an unattended download of a back catalogue entry.
func seriesCheckQualifies(mode string, date *time.Time, now time.Time, lastOK *time.Time) bool {
	if mode == models.SeriesMonitorAll {
		return true
	}
	if date == nil || date.IsZero() {
		return false
	}
	if date.After(now) {
		return true
	}
	// Catalogue dates are date only (midnight) while last_ok_at is the moment
	// the check began, so a book dated the same day as the last good check but
	// listed by Hardcover after it began would fail a plain After. Compare
	// against the start of that day; a book already in the library is skipped
	// before this runs, so the wider window cannot add it twice.
	return lastOK != nil && !date.Before(utcDayStart(*lastOK))
}

func utcDayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type seriesJobHoldKey struct{}

// seriesJobHoldsUpcoming reports whether ctx is a series check, which holds a
// future dated book it creates in the same transaction as the create.
func seriesJobHoldsUpcoming(ctx context.Context) bool {
	v, _ := ctx.Value(seriesJobHoldKey{}).(bool)
	return v
}

// CheckSeries runs one scheduled check of series. It re-reads the series
// first, so one deleted or unmonitored since the job picked it is skipped. A
// provider failure comes back as an error wrapping errSeriesMetadataProvider.
func (h *SeriesHandler) CheckSeries(ctx context.Context, series *models.Series) (SeriesCheckResult, error) {
	var res SeriesCheckResult
	if series == nil || series.ID == 0 {
		return res, errors.New("check series: series has no id")
	}
	if _, loaded := h.checking.LoadOrStore(series.ID, struct{}{}); loaded {
		return res, ErrSeriesCheckRunning
	}
	defer h.checking.Delete(series.ID)

	cur, err := h.series.GetByID(ctx, series.ID)
	if err != nil {
		return res, fmt.Errorf("check series %d: %w", series.ID, err)
	}
	if cur == nil || !cur.Monitored {
		slog.Info("series check skipped a series that is gone or no longer monitored", "seriesId", series.ID)
		return res, nil
	}
	mon := cur.Monitoring
	if mon == nil {
		mon = &models.SeriesMonitoring{Mode: models.SeriesMonitorUpcoming}
	}

	// Whose series this is. Tenancy has no owner column on series, so it is
	// taken from whoever toggled monitoring, or for a flag set before this
	// job existed, from the books in it. A series holding several accounts'
	// books has no single owner to add for, so it is left alone.
	owner := mon.OwnerUserID
	if owner == 0 && auth.EnforceTenancy() {
		owners, err := h.series.DistinctBookOwners(ctx, cur.ID)
		if err != nil {
			return res, fmt.Errorf("check series %d: %w", cur.ID, err)
		}
		switch len(owners) {
		case 0:
		case 1:
			owner = owners[0]
		default:
			res.Status = models.SeriesCheckAmbiguous
			res.Message = "This series holds books from more than one account, so Bindery does not know whose library to add to."
			return res, nil
		}
	}

	if !h.enhancedHardcoverEnabled(ctx) {
		res.Status = models.SeriesCheckDisabled
		res.Message = "Enhanced Hardcover support is turned off, so there is no catalogue to check."
		return res, nil
	}
	link := cur.HardcoverLink
	if link == nil {
		res.Status = models.SeriesCheckNoLink
		res.Message = "This series is not linked to Hardcover. Link it so Bindery has a catalogue to check."
		return res, nil
	}
	if h.meta == nil || h.authors == nil {
		res.Status = models.SeriesCheckFailed
		res.Message = "The metadata provider is not configured."
		return res, nil
	}

	// The diff is read as the owner sees the series, so another account's
	// copy of a book does not count as present.
	scoped := cur
	if owner != 0 {
		scoped, err = h.series.GetByIDForUser(ctx, cur.ID, owner)
		if err != nil || scoped == nil {
			return res, fmt.Errorf("check series %d: re-read for owner %d: %w", cur.ID, owner, err)
		}
		scoped.HardcoverLink = link
	}
	catalog, err := h.meta.GetSeriesCatalog(ctx, link.HardcoverSeriesID)
	if err != nil {
		res.Status = models.SeriesCheckFailed
		res.Message = "The Hardcover catalogue could not be fetched."
		return res, fmt.Errorf("%w: %w", errSeriesMetadataProvider, err)
	}
	if catalog == nil {
		res.Status = models.SeriesCheckFailed
		res.Message = "Hardcover returned no catalogue for the linked series."
		return res, nil
	}

	ctx = context.WithValue(withSeriesJobOwner(ctx, owner), seriesJobHoldKey{}, true)
	now := time.Now()
	diff := buildHardcoverDiff(ctx, nil, 0, scoped, link, catalog)
	format := requestedFormat{mediaType: models.MediaTypeEbook}
	if h.checkMediaType != nil {
		if mt := h.checkMediaType(ctx); mt != "" {
			format.mediaType = mt
		}
	}

	// A moved release date: a held book follows the catalogue.
	h.refreshUpcomingHolds(ctx, diff, now)

	collides := missingPositionCollisions(diff)
	failed := 0
	for _, missing := range diff.Missing {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		// Fail toward not adding. A row with no identifier of its own cannot
		// be told from its neighbours, and one sharing a position with a book
		// the library already tracks is as likely a reprint or a new edition
		// as a new work (#2524).
		if strings.TrimSpace(missing.ForeignBookID) == "" || collides[missing.ForeignBookID] {
			res.Held++
			slog.Debug("series check: not adding a catalogue row whose identity is ambiguous",
				"seriesId", cur.ID, "title", missing.Title, "position", missing.Position)
			continue
		}
		if !seriesCheckQualifies(mon.Mode, missing.ReleaseDate, now, mon.LastOKAt) {
			res.Held++
			continue
		}
		catalogBook, ok := findCatalogBook(catalog.Books, missing.ForeignBookID, "")
		if !ok {
			continue
		}
		callStart := time.Now()
		book, err := h.ensureHardcoverCatalogBook(ctx, scoped, catalog.AuthorName, catalogBook, format, true, false)
		if err != nil {
			if errors.Is(err, errSeriesBookFilteredByProfile) {
				continue
			}
			if errors.Is(err, errSeriesMetadataProvider) || ctx.Err() != nil {
				return res, err
			}
			slog.Warn("series check: failed to add a catalogue book; continuing",
				"seriesId", cur.ID, "foreignBookID", missing.ForeignBookID, "error", err)
			failed++
			continue
		}
		if book == nil || book.CreatedAt.Before(callStart) {
			// Refused, or it resolved to a book the library already had.
			continue
		}
		res.Created++
		// The hold was written with the book (see ensureHardcoverCatalogBook).
		if book.SearchHeldUntil != nil {
			res.Upcoming++
		}
	}
	if failed > 0 {
		// Not ok: last_ok_at stays where it was, so a past dated book that
		// failed to add is still newer than it on the next check.
		res.Status = models.SeriesCheckPartial
		res.Message = fmt.Sprintf("%d books could not be added and will be retried.", failed)
		slog.Warn("series check finished with failures", "seriesId", cur.ID, "series", cur.Title,
			"created", res.Created, "failed", failed)
		return res, nil
	}
	res.Status = models.SeriesCheckOK
	slog.Info("series check finished", "seriesId", cur.ID, "series", cur.Title, "mode", mon.Mode,
		"created", res.Created, "upcoming", res.Upcoming, "left", res.Held)
	return res, nil
}

// refreshUpcomingHolds keeps the Upcoming hold of every present book in the
// diff equal to the catalogue's release date: moved later or earlier it
// follows, and once the date is no longer ahead the hold is lifted. A wanted
// book with a future date and no hold gets one, which repairs a book whose
// hold was never written (a failure or timeout between creating it and
// holding it) before the wanted sweep searches it. A catalogue that drops the
// date leaves the hold alone until the date it had passes. Holds are
// refreshed only while the series is monitored.
func (h *SeriesHandler) refreshUpcomingHolds(ctx context.Context, diff seriesHardcoverDiffResponse, now time.Time) {
	holds, err := h.books.SearchHolds(ctx, now)
	if err != nil {
		return
	}
	for _, item := range diff.Present {
		if item.LocalBookID == nil || item.ReleaseDate == nil {
			continue
		}
		id := *item.LocalBookID
		until, held := holds[id]
		switch {
		case !held:
			if item.ReleaseDate.After(now) && item.LocalStatus == models.BookStatusWanted {
				if err := h.books.SetSearchHold(ctx, id, *item.ReleaseDate); err != nil {
					slog.Warn("series check: could not mark a book Upcoming", "bookId", id, "error", err)
				}
			}
		case !item.ReleaseDate.After(now):
			if err := h.books.ClearSearchHold(ctx, id); err != nil {
				slog.Warn("series check: could not lift an Upcoming hold", "bookId", id, "error", err)
			}
		case !item.ReleaseDate.Equal(until):
			if err := h.books.SetSearchHold(ctx, id, *item.ReleaseDate); err != nil {
				slog.Warn("series check: could not move an Upcoming hold", "bookId", id, "error", err)
			}
		}
	}
}

// missingPositionCollisions returns the foreign ids of Missing rows that share
// a position with a book the library already tracks or with another Missing
// row.
func missingPositionCollisions(diff seriesHardcoverDiffResponse) map[string]bool {
	var taken []string
	for _, group := range [][]seriesHardcoverDiffBook{diff.Present, diff.Uncertain, diff.Covered, diff.LocalOnly} {
		for _, item := range group {
			if p := strings.TrimSpace(item.Position); p != "" {
				taken = append(taken, p)
			}
		}
	}
	out := make(map[string]bool)
	for i, m := range diff.Missing {
		p := strings.TrimSpace(m.Position)
		if p == "" {
			continue
		}
		hit := false
		for _, t := range taken {
			if seriesmatch.SamePosition(p, t) {
				hit = true
				break
			}
		}
		for j, other := range diff.Missing {
			if i != j && !hit && seriesmatch.SamePosition(p, strings.TrimSpace(other.Position)) {
				hit = true
			}
		}
		if hit {
			out[m.ForeignBookID] = true
		}
	}
	return out
}
