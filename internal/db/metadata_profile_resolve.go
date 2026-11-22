package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/models"
)

// SettingAuthorDefaultMetadataProfileID is the install wide metadata profile
// given to authors created without one, and the profile every consumer falls
// back to for an author whose own profile no longer exists (#4132). Empty, the
// default, means "the first profile that exists".
const SettingAuthorDefaultMetadataProfileID = "author.default_metadata_profile_id"

// MetadataProfileResolution is the answer ResolveMetadataProfile gives.
type MetadataProfileResolution struct {
	// Profile is the profile that governs the author. Never nil on success.
	Profile *models.MetadataProfile
	// RequestedID is the id stored on the author, 0 when it has none.
	RequestedID int64
	// FellBack is true when RequestedID names a profile that does not exist,
	// so Profile is a substitute. An author with no id at all is not a fallback:
	// "use the default" is what no id means.
	FellBack bool
	// BuiltIn is true when no profile exists at all and Profile is the built in
	// default.
	BuiltIn bool
}

// BuiltinMetadataProfile is the profile used when the install has none at all:
// it filters nothing, which is what an author with no profile has always meant
// for search, and what a user who deleted every profile is asking for.
func BuiltinMetadataProfile() *models.MetadataProfile {
	return &models.MetadataProfile{
		Name:                    "Built in default",
		UnknownLanguageBehavior: models.UnknownLanguagePass,
	}
}

// ResolveMetadataProfile is the one place that decides which metadata profile
// governs an author. Search (automatic and interactive), the importer's
// language check, catalogue sync, series fill and reconcile all call it so a
// missing profile means the same thing everywhere (#4132).
//
// Order: the author's own profile if it exists, else the install default
// (author.default_metadata_profile_id when that profile exists), else the
// first profile by id, else BuiltinMetadataProfile. A lookup failure returns
// the error and no profile, and each caller keeps its own policy for that.
// A nil author is treated as one with no profile.
//
// The substitution is logged once per author and missing id, not on every
// search.
func ResolveMetadataProfile(ctx context.Context, profiles *MetadataProfileRepo, author *models.Author) (MetadataProfileResolution, error) {
	if profiles == nil {
		return MetadataProfileResolution{Profile: BuiltinMetadataProfile(), BuiltIn: true}, nil
	}
	res := MetadataProfileResolution{}
	if author != nil && author.MetadataProfileID != nil {
		res.RequestedID = *author.MetadataProfileID
		p, err := profiles.GetByID(ctx, res.RequestedID)
		if err != nil {
			return MetadataProfileResolution{}, err
		}
		if p != nil {
			res.Profile = p
			return res, nil
		}
		res.FellBack = true
	}
	id, ok, err := defaultMetadataProfileID(ctx, profiles.db)
	if err != nil {
		return MetadataProfileResolution{}, err
	}
	if ok {
		p, err := profiles.GetByID(ctx, id)
		if err != nil {
			return MetadataProfileResolution{}, err
		}
		res.Profile = p
	}
	if res.Profile == nil {
		res.Profile = BuiltinMetadataProfile()
		res.BuiltIn = true
	}
	if res.FellBack && author != nil {
		logMetadataProfileFallbackOnce(author.ID, res)
	}
	return res, nil
}

var metadataProfileFallbackLogged sync.Map

func logMetadataProfileFallbackOnce(authorID int64, res MetadataProfileResolution) {
	key := fmt.Sprintf("%d/%d", authorID, res.RequestedID)
	if _, loaded := metadataProfileFallbackLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Warn("author's metadata profile does not exist; using the default profile instead",
		"authorID", authorID, "missingProfileID", res.RequestedID,
		"usingProfile", res.Profile.Name, "usingProfileID", res.Profile.ID)
}

// defaultMetadataProfileID returns the profile id new authors get: the one
// named by SettingAuthorDefaultMetadataProfileID when that profile exists,
// else the lowest id that exists. ok is false when no profile exists at all.
func defaultMetadataProfileID(ctx context.Context, dbh *sql.DB) (id int64, ok bool, err error) {
	var raw string
	err = dbh.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingAuthorDefaultMetadataProfileID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("read %s: %w", SettingAuthorDefaultMetadataProfileID, err)
	}
	if err == nil {
		if n, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && n > 0 {
			var one int
			qerr := dbh.QueryRowContext(ctx, "SELECT 1 FROM metadata_profiles WHERE id = ?", n).Scan(&one)
			if qerr == nil {
				return n, true, nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return 0, false, fmt.Errorf("check metadata profile %d: %w", n, qerr)
			}
		}
	}
	var first sql.NullInt64
	if err := dbh.QueryRowContext(ctx, "SELECT MIN(id) FROM metadata_profiles").Scan(&first); err != nil {
		return 0, false, fmt.Errorf("find first metadata profile: %w", err)
	}
	if !first.Valid {
		return 0, false, nil
	}
	return first.Int64, true, nil
}

// ValidateDefaultAuthorMetadataProfileID checks a value about to be stored in
// SettingAuthorDefaultMetadataProfileID: empty clears it, anything else must
// name a metadata profile that exists.
func ValidateDefaultAuthorMetadataProfileID(ctx context.Context, settings *SettingsRepo, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("%s %q must be a metadata profile id or empty", SettingAuthorDefaultMetadataProfileID, value)
	}
	if settings == nil {
		return nil
	}
	var one int
	err = settings.db.QueryRowContext(ctx, "SELECT 1 FROM metadata_profiles WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: metadata profile %d does not exist", SettingAuthorDefaultMetadataProfileID, id)
	}
	if err != nil {
		return fmt.Errorf("check metadata profile %d: %w", id, err)
	}
	return nil
}

// ErrMetadataProfileInUse is returned by Delete when authors still reference
// the profile.
type ErrMetadataProfileInUse struct {
	ProfileID   int64
	AuthorCount int
}

func (e *ErrMetadataProfileInUse) Error() string {
	return fmt.Sprintf("metadata profile %d is in use by %d authors", e.ProfileID, e.AuthorCount)
}

// CountAuthorsUsing returns how many authors are assigned the profile.
func (r *MetadataProfileRepo) CountAuthorsUsing(ctx context.Context, id int64) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM authors WHERE metadata_profile_id = ?", id).Scan(&n); err != nil {
		return 0, fmt.Errorf("count authors using metadata profile %d: %w", id, err)
	}
	return n, nil
}

// AuthorNamesUsing returns up to limit names of authors assigned the profile.
func (r *MetadataProfileRepo) AuthorNamesUsing(ctx context.Context, id int64, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT name FROM authors WHERE metadata_profile_id = ? ORDER BY name LIMIT ?", id, limit)
	if err != nil {
		return nil, fmt.Errorf("list authors using metadata profile %d: %w", id, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// DeleteMovingAuthors deletes the profile after pointing every author that
// uses it at moveTo, in one transaction. moveTo must exist and differ from id.
func (r *MetadataProfileRepo) DeleteMovingAuthors(ctx context.Context, id, moveTo int64) error {
	if moveTo == id {
		return fmt.Errorf("cannot move authors to the profile being deleted")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete metadata profile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var one int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM metadata_profiles WHERE id = ?", moveTo).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("metadata profile %d does not exist", moveTo)
		}
		return fmt.Errorf("check metadata profile %d: %w", moveTo, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE authors SET metadata_profile_id = ? WHERE metadata_profile_id = ?", moveTo, id); err != nil {
		return fmt.Errorf("move authors off metadata profile %d: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM metadata_profiles WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete metadata profile %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}
