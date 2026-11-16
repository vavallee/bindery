package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// SettingAuthorDefaultQualityProfileID is the install wide quality profile
// given to authors created without one (#3831). Empty, the default, means
// "none": the author is created with no quality profile, which is how every
// author was created before the setting existed.
//
// Why it exists: an author with no quality profile has no format filter at
// all. ResolveAuthorQualityProfile returns nil, so neither the grab time
// check nor the importer's rejectedDownloadFormat refuses anything, and a pdf
// only download imports under what the user believes is an epub only setup.
// The Add Author dialog never sent a profile, so that was the normal state of
// an author added from the UI.
//
// Quality profiles are instance wide configuration (only admins write them,
// every account can list them), so this is one install wide value, written
// by admins through the generic settings API like the other author defaults.
//
// Lives in internal/db for the same reason as the monitor defaults: the
// create paths that honour it (the importers, the series fill, the list
// syncer and the add handlers) all depend on internal/db and none of them can
// depend on internal/api.
const SettingAuthorDefaultQualityProfileID = "author.default_quality_profile_id"

// ResolveDefaultAuthorQualityProfileID returns the configured default quality
// profile id, or nil when there is none to apply.
//
// Every failure mode answers nil, the same "no default" an unset setting
// gives: no settings repo, an unreadable or unparseable value, or an id whose
// profile no longer exists. A profile deleted after it was made the default
// must not fail author creation, and must not stamp a dangling id either,
// since the column has no foreign key and ResolveAuthorQualityProfile would
// then log a warning for that author on every search.
func ResolveDefaultAuthorQualityProfileID(ctx context.Context, settings *SettingsRepo) *int64 {
	if settings == nil {
		return nil
	}
	s, err := settings.Get(ctx, SettingAuthorDefaultQualityProfileID)
	if err != nil || s == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(s.Value), 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	exists, err := settings.qualityProfileExists(ctx, id)
	if err != nil {
		slog.Warn("could not check the default quality profile; authors are created without one",
			"quality_profile_id", id, "error", err)
		return nil
	}
	if !exists {
		slog.Debug("default quality profile no longer exists; authors are created without one",
			"quality_profile_id", id)
		return nil
	}
	return &id
}

// ApplyAuthorQualityProfileDefault stamps the default quality profile onto an
// author about to be created when it carries none. A profile already on the
// author wins, so a caller with a deliberate choice keeps it: the Hardcover
// list syncer copies the list's own profile, and the add author handler
// passes what the request asked for.
func ApplyAuthorQualityProfileDefault(ctx context.Context, settings *SettingsRepo, a *models.Author) {
	if a == nil || a.QualityProfileID != nil {
		return
	}
	a.QualityProfileID = ResolveDefaultAuthorQualityProfileID(ctx, settings)
}

// ApplyAuthorCreateDefaults stamps every install wide author default onto an
// author about to be created: the monitor defaults and the default quality
// profile. Call it immediately before Create on every path that creates an
// author without an explicit choice from the user, so a new default reaches
// all of them at once instead of each path learning about it separately.
func ApplyAuthorCreateDefaults(ctx context.Context, settings *SettingsRepo, a *models.Author) {
	ApplyAuthorMonitorDefaults(ctx, settings, a)
	ApplyAuthorQualityProfileDefault(ctx, settings, a)
}

// ValidateDefaultAuthorQualityProfileID checks a value about to be stored in
// SettingAuthorDefaultQualityProfileID: empty clears it, anything else must
// name a quality profile that exists.
func ValidateDefaultAuthorQualityProfileID(ctx context.Context, settings *SettingsRepo, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("%s %q must be a quality profile id or empty", SettingAuthorDefaultQualityProfileID, value)
	}
	if settings == nil {
		return nil
	}
	exists, err := settings.qualityProfileExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s: quality profile %d does not exist", SettingAuthorDefaultQualityProfileID, id)
	}
	return nil
}

func (r *SettingsRepo) qualityProfileExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, "SELECT 1 FROM quality_profiles WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check quality profile %d: %w", id, err)
	}
	return true, nil
}
