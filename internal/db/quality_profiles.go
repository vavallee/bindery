package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// ErrQualityProfileInUse is returned by Delete when one or more authors still
// reference the profile. The caller (HTTP handler) maps this to 409 Conflict.
type ErrQualityProfileInUse struct {
	ProfileID   int64
	AuthorCount int
}

func (e *ErrQualityProfileInUse) Error() string {
	return fmt.Sprintf("quality profile %d is in use by %d author(s)", e.ProfileID, e.AuthorCount)
}

type QualityProfileRepo struct {
	db *sql.DB
}

func NewQualityProfileRepo(db *sql.DB) *QualityProfileRepo {
	return &QualityProfileRepo{db: db}
}

func (r *QualityProfileRepo) List(ctx context.Context) ([]models.QualityProfile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at, COALESCE(owner_user_id, 0) FROM quality_profiles ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list quality profiles: %w", err)
	}
	defer rows.Close()

	var profiles []models.QualityProfile
	for rows.Next() {
		p, err := scanQualityProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (r *QualityProfileRepo) GetByID(ctx context.Context, id int64) (*models.QualityProfile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at, COALESCE(owner_user_id, 0) FROM quality_profiles WHERE id=?", id)
	if err != nil {
		return nil, fmt.Errorf("get quality profile %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	p, err := scanQualityProfile(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

// Create inserts a new quality profile. The items slice is serialised to JSON
// in a single column — items are inherently ordered (preference) and small
// enough that a separate join table buys nothing.
func (r *QualityProfileRepo) Create(ctx context.Context, p *models.QualityProfile) error {
	itemsJSON, err := marshalItems(p.Items)
	if err != nil {
		return err
	}
	upgrade := 0
	if p.UpgradeAllowed {
		upgrade = 1
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO quality_profiles (name, upgrade_allowed, cutoff, items)
		VALUES (?, ?, ?, ?)`,
		p.Name, upgrade, p.Cutoff, itemsJSON)
	if err != nil {
		return fmt.Errorf("create quality profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get quality profile id: %w", err)
	}
	p.ID = id
	return nil
}

// Update replaces all editable fields on an existing profile in a single
// statement. Items live in a JSON column, so there's nothing transactional
// to coordinate across tables.
func (r *QualityProfileRepo) Update(ctx context.Context, p *models.QualityProfile) error {
	itemsJSON, err := marshalItems(p.Items)
	if err != nil {
		return err
	}
	upgrade := 0
	if p.UpgradeAllowed {
		upgrade = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE quality_profiles SET name=?, upgrade_allowed=?, cutoff=?, items=?
		WHERE id=?`,
		p.Name, upgrade, p.Cutoff, itemsJSON, p.ID)
	if err != nil {
		return fmt.Errorf("update quality profile %d: %w", p.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update quality profile %d rows affected: %w", p.ID, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a quality profile. Returns *ErrQualityProfileInUse when one
// or more authors still reference it — authors.quality_profile_id has no FK
// constraint, so we enforce referential integrity here.
func (r *QualityProfileRepo) Delete(ctx context.Context, id int64) error {
	count, err := r.CountAuthorsUsing(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return &ErrQualityProfileInUse{ProfileID: id, AuthorCount: count}
	}
	res, err := r.db.ExecContext(ctx, "DELETE FROM quality_profiles WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete quality profile %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete quality profile %d rows affected: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountAuthorsUsing returns the number of authors currently assigned this
// profile. Used for the delete-in-use guard.
func (r *QualityProfileRepo) CountAuthorsUsing(ctx context.Context, id int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM authors WHERE quality_profile_id = ?", id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count authors using quality profile %d: %w", id, err)
	}
	return n, nil
}

// AuthorNamesUsing returns up to limit author names currently assigned this
// profile, ordered by name. Used to enrich the delete-in-use error response.
func (r *QualityProfileRepo) AuthorNamesUsing(ctx context.Context, id int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT name FROM authors WHERE quality_profile_id = ? ORDER BY name LIMIT ?", id, limit)
	if err != nil {
		return nil, fmt.Errorf("list authors using quality profile %d: %w", id, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan author name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// NameExists reports whether any profile other than excludeID uses the given
// name. excludeID=0 means "no exclusion" (i.e. checking for a Create).
func (r *QualityProfileRepo) NameExists(ctx context.Context, name string, excludeID int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM quality_profiles WHERE name = ? AND id != ?", name, excludeID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check quality profile name %q: %w", name, err)
	}
	return n > 0, nil
}

func marshalItems(items []models.QualityItem) (string, error) {
	if items == nil {
		items = []models.QualityItem{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("marshal quality profile items: %w", err)
	}
	return string(b), nil
}

func scanQualityProfile(rows *sql.Rows) (models.QualityProfile, error) {
	var p models.QualityProfile
	var upgradeAllowed int
	var itemsJSON string
	if err := rows.Scan(&p.ID, &p.Name, &upgradeAllowed, &p.Cutoff, &itemsJSON, &p.CreatedAt, &p.OwnerUserID); err != nil {
		return p, fmt.Errorf("scan quality profile: %w", err)
	}
	p.UpgradeAllowed = upgradeAllowed == 1
	if err := json.Unmarshal([]byte(itemsJSON), &p.Items); err != nil {
		return p, fmt.Errorf("unmarshal quality profile items: %w", err)
	}
	if p.Items == nil {
		p.Items = []models.QualityItem{}
	}
	return p, nil
}

// AsInUseError attempts to extract an *ErrQualityProfileInUse from err.
// Returns (nil, false) when err is something else.
func AsInUseError(err error) (*ErrQualityProfileInUse, bool) {
	var e *ErrQualityProfileInUse
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
