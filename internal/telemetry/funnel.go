package telemetry

// Setup-funnel first-event markers.
//
// The Features payload is a current-state snapshot, which cannot answer the
// question the retention data raised: how long does a new install take to
// reach a working pipeline (indexer + download client), and where do the
// ones that never get there stall? Fleet telemetry showed installs with
// both configured retain 66% at 7 days while unconfigured installs retain
// 16% — but with state-only sampling there is no way to see the funnel.
//
// Each key below records the FIRST time an event happened, written with
// SettingsRepo.SetIfAbsent so the first write wins and repeats are no-ops.
// The wire format is a day offset since install (see Features), never a
// timestamp — an integer number of days is not identifying.

import (
	"context"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const (
	// SettingInstallCreatedAt anchors the day-offset math. Stamped when the
	// install ID is first generated; for installs that predate this field it
	// is stamped at the first post-upgrade ping, so their offsets measure
	// from the upgrade — the telemetry server filters funnel cohorts to
	// installs younger than the field to keep that skew out of the charts.
	SettingInstallCreatedAt = "telemetry.install_created_at"

	SettingFirstIndexerAt = "funnel.first_indexer_at"
	SettingFirstClientAt  = "funnel.first_client_at"
	SettingFirstAuthorAt  = "funnel.first_author_at"
	SettingFirstGrabAt    = "funnel.first_grab_at"
	SettingFirstImportAt  = "funnel.first_import_at"
)

// MarkFirst records now as the first occurrence of a funnel event, if none
// is recorded yet. Failures are logged at debug and swallowed — funnel
// bookkeeping must never break the calling request path.
func MarkFirst(ctx context.Context, settings *db.SettingsRepo, key string) {
	if settings == nil {
		return
	}
	if _, err := settings.SetIfAbsent(ctx, key, time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Debug("telemetry: funnel mark failed", "key", key, "error", err)
	}
}

// GatherFunnel fills the Setup*Day fields on f from the recorded first-event
// timestamps. Grab and import first-times are derived from the history table
// on first sight (MIN(created_at) of the "grabbed" / "bookImported" events) and
// then pinned via SetIfAbsent, so later history pruning cannot lose them.
//
// Offsets are whole days since SettingInstallCreatedAt, clamped out (field
// omitted) when the event predates the anchor — that only happens for
// installs older than the anchor field, whose funnel data is meaningless.
func GatherFunnel(ctx context.Context, settings *db.SettingsRepo, history *db.HistoryRepo, f *Features) {
	anchor, ok := settingTime(ctx, settings, SettingInstallCreatedAt)
	if !ok {
		return
	}

	// Derive-and-pin grab/import from history when not yet recorded. Uses
	// the true earliest event time rather than time-of-observation, so an
	// install upgrading to this version backfills its real funnel values.
	for _, d := range []struct {
		event string
		key   string
	}{
		{models.HistoryEventGrabbed, SettingFirstGrabAt},
		{models.HistoryEventBookImported, SettingFirstImportAt},
	} {
		if _, exists := settingTime(ctx, settings, d.key); exists {
			continue
		}
		if history == nil {
			continue
		}
		if at, err := history.FirstEventAt(ctx, d.event); err == nil && at != nil {
			if _, err := settings.SetIfAbsent(ctx, d.key, at.UTC().Format(time.RFC3339)); err != nil {
				slog.Debug("telemetry: funnel pin failed", "key", d.key, "error", err)
			}
		}
	}

	for _, m := range []struct {
		key  string
		dest **int
	}{
		{SettingFirstIndexerAt, &f.SetupIndexerDay},
		{SettingFirstClientAt, &f.SetupClientDay},
		{SettingFirstAuthorAt, &f.FirstAuthorDay},
		{SettingFirstGrabAt, &f.FirstGrabDay},
		{SettingFirstImportAt, &f.FirstImportDay},
	} {
		at, ok := settingTime(ctx, settings, m.key)
		if !ok || at.Before(anchor) {
			continue
		}
		days := int(at.Sub(anchor).Hours() / 24)
		*m.dest = &days
	}
}

// settingTime reads a setting and parses it as RFC3339. Missing keys,
// empty values, and parse failures all report ok=false.
func settingTime(ctx context.Context, settings *db.SettingsRepo, key string) (time.Time, bool) {
	v, err := settings.Get(ctx, key)
	if err != nil || v == nil || v.Value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v.Value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
