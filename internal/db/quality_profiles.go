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
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at, COALESCE(owner_user_id, 0), preferred_narrators, edition_preference, remove_replaced FROM quality_profiles ORDER BY id")
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
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at, COALESCE(owner_user_id, 0), preferred_narrators, edition_preference, remove_replaced FROM quality_profiles WHERE id=?", id)
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
// Create inserts a system-owned quality profile (owner_user_id NULL). Use it
// for non-user-driven writers (seeding, migrations). User-driven creates from
// the API MUST use CreateForUser so the row is scoped to its creator —
// otherwise it lands owner-less and CheckOwnership treats it as shared, letting
// any authenticated user read/modify/delete it.
func (r *QualityProfileRepo) Create(ctx context.Context, p *models.QualityProfile) error {
	return r.create(ctx, p, 0)
}

// CreateForUser inserts a quality profile owned by ownerUserID. A zero id
// (API-key / local-only / disabled-auth requests that carry no user identity)
// is written as NULL, matching AuthorRepo.CreateForUser and the "0 ⇒ shared /
// admin-equivalent" convention CheckOwnership relies on.
func (r *QualityProfileRepo) CreateForUser(ctx context.Context, p *models.QualityProfile, ownerUserID int64) error {
	return r.create(ctx, p, ownerUserID)
}

func (r *QualityProfileRepo) create(ctx context.Context, p *models.QualityProfile, ownerUserID int64) error {
	itemsJSON, err := marshalItems(p.Items)
	if err != nil {
		return err
	}
	upgrade := 0
	if p.UpgradeAllowed {
		upgrade = 1
	}
	removeReplaced := 0
	if p.RemoveReplaced {
		removeReplaced = 1
	}
	var ownerArg any
	if ownerUserID != 0 {
		ownerArg = ownerUserID
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO quality_profiles (name, upgrade_allowed, cutoff, items, owner_user_id, preferred_narrators, edition_preference, remove_replaced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, upgrade, p.Cutoff, itemsJSON, ownerArg,
		encodeNarrators(p.PreferredNarrators), models.NormalizeEditionPreference(p.EditionPreference), removeReplaced)
	if err != nil {
		return fmt.Errorf("create quality profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get quality profile id: %w", err)
	}
	p.ID = id
	p.OwnerUserID = ownerUserID
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
	removeReplaced := 0
	if p.RemoveReplaced {
		removeReplaced = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE quality_profiles SET name=?, upgrade_allowed=?, cutoff=?, items=?,
		       preferred_narrators=?, edition_preference=?, remove_replaced=?
		WHERE id=?`,
		p.Name, upgrade, p.Cutoff, itemsJSON,
		encodeNarrators(p.PreferredNarrators), models.NormalizeEditionPreference(p.EditionPreference), removeReplaced, p.ID)
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

// AnyUpgradeAllowed reports whether any profile has ebook upgrades switched on.
// It is one cheap query, so the upgrade sweep can stop before it reads a
// library's worth of books on an install that never opted in (#2420).
func (r *QualityProfileRepo) AnyUpgradeAllowed(ctx context.Context) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM quality_profiles WHERE upgrade_allowed = 1)").Scan(&n); err != nil {
		return false, fmt.Errorf("check upgrade profiles: %w", err)
	}
	return n == 1, nil
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
	var upgradeAllowed, removeReplaced int
	var itemsJSON, narratorsJSON string
	if err := rows.Scan(&p.ID, &p.Name, &upgradeAllowed, &p.Cutoff, &itemsJSON, &p.CreatedAt, &p.OwnerUserID, &narratorsJSON, &p.EditionPreference, &removeReplaced); err != nil {
		return p, fmt.Errorf("scan quality profile: %w", err)
	}
	p.UpgradeAllowed = upgradeAllowed == 1
	p.RemoveReplaced = removeReplaced == 1
	p.PreferredNarrators = decodeNarrators(narratorsJSON)
	p.EditionPreference = models.NormalizeEditionPreference(p.EditionPreference)
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
