package db

import (
	"context"
	"strconv"

	"github.com/vavallee/bindery/internal/models"
)

// Install-wide author monitor defaults, surfaced in Settings → Metadata Profiles as
// "Default monitor mode" with the hint "Applied to newly added authors".
//
// The keys live here rather than in internal/api because the create paths that
// have to honour them are spread across the importers — calibre, abs, migrate
// (CSV/Readarr/Goodreads), the series backfill and the add-author/add-book
// handlers. Every one of those already depends on internal/db and none of them
// can depend on internal/api. internal/api re-exports both names.
//
// Before #1666 only the manual Add Author endpoint read these, so the setting
// did nothing on precisely the bulk paths a user sets it to protect against.
const (
	SettingAuthorDefaultMonitorMode        = "author.default_monitor_mode"
	SettingAuthorDefaultMonitorLatestCount = "author.default_monitor_latest_count"
)

// ResolveAuthorMonitorDefaults returns the install-wide default monitor mode
// and latest-count, falling back to the built-in defaults when the settings are
// unset, unreadable or hold a value the validator would no longer accept.
//
// "series" is refused even though IsAuthorMonitorModeValid accepts it: series
// mode pins an author to a user-picked set of series (#810), which has no
// install-wide meaning. The settings handler already refuses to store it; this
// is the read side of the same rule, so a row written by an older build or by
// hand can't put an author into a mode the UI can't express.
func ResolveAuthorMonitorDefaults(ctx context.Context, settings *SettingsRepo) (string, int) {
	mode := models.DefaultAuthorMonitorMode
	latestCount := models.DefaultAuthorMonitorLatestCount
	if settings == nil {
		return mode, latestCount
	}
	if s, err := settings.Get(ctx, SettingAuthorDefaultMonitorMode); err == nil && s != nil {
		if models.IsAuthorMonitorModeValidAsGlobalDefault(s.Value) {
			mode = s.Value
		}
	}
	if s, err := settings.Get(ctx, SettingAuthorDefaultMonitorLatestCount); err == nil && s != nil {
		if n, convErr := strconv.Atoi(s.Value); convErr == nil && n > 0 {
			latestCount = n
		}
	}
	return mode, latestCount
}

// ApplyAuthorMonitorDefaults stamps the install-wide monitor defaults onto an
// author that is about to be created. Call it immediately before Create on
// every path that does not take an explicit mode from the user.
//
// A mode already set on the author wins, so a caller with a deliberate policy
// keeps it — the Hardcover list syncer pins "none" (#1290) and the add-author
// handler passes whatever the request asked for. Only the zero value, which is
// what every importer leaves behind and what normalizeAuthorMonitorDefaults
// would otherwise silently turn into "all", is filled in from settings.
func ApplyAuthorMonitorDefaults(ctx context.Context, settings *SettingsRepo, a *models.Author) {
	if a == nil {
		return
	}
	mode, latestCount := ResolveAuthorMonitorDefaults(ctx, settings)
	if !models.IsAuthorMonitorModeValid(a.MonitorMode) {
		a.MonitorMode = mode
	}
	if a.MonitorLatestCount <= 0 {
		a.MonitorLatestCount = latestCount
	}
}
