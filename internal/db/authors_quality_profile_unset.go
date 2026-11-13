package db

import (
	"context"
	"fmt"
	"time"
)

// AuthorOwnerScope picks which authors a bulk author operation may touch.
//
// It is explicit rather than read from the request context so the rule is
// visible at the call site and testable without an HTTP request. The handler
// builds it from the caller's identity (see
// internal/api.qualityProfileUnsetScope).
type AuthorOwnerScope struct {
	// AllOwners covers every author whatever its owner: an admin, or any
	// caller while tenancy is off, the same unscoped view the library list
	// gives them.
	AllOwners bool
	// OwnerUserID covers only the authors this account owns, for a non admin
	// under tenancy. Unowned rows are not theirs to change.
	OwnerUserID int64
}

// where returns the owner clause and its args. Another account's authors are
// never in scope unless AllOwners is set.
func (s AuthorOwnerScope) where() (string, []any) {
	if s.AllOwners {
		return "1 = 1", nil
	}
	return "owner_user_id = ?", []any{s.OwnerUserID}
}

// CountWithoutQualityProfile counts the authors in scope that have no quality
// profile, which means every format passes for them at grab and at import
// (#3831).
func (r *AuthorRepo) CountWithoutQualityProfile(ctx context.Context, scope AuthorOwnerScope) (int, error) {
	clause, args := scope.where()
	var n int
	err := r.exec.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM authors WHERE quality_profile_id IS NULL AND "+clause, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count authors without a quality profile: %w", err)
	}
	return n, nil
}

// ApplyQualityProfileToUnset gives profileID to exactly the authors in scope
// that have no quality profile, and returns how many changed. Authors that
// already have a profile keep it.
//
// updated_at moves with the change. That is not a general stale write guard:
// the Edit Author dialog sends no snapshot, so a dialog opened before the
// apply still saves whatever fields it sends. It only protects a PUT already in flight, which
// compares the row it loaded with the row it is about to write and refuses
// when the apply landed in between.
func (r *AuthorRepo) ApplyQualityProfileToUnset(ctx context.Context, scope AuthorOwnerScope, profileID int64) (int64, error) {
	clause, args := scope.where()
	args = append([]any{profileID, timeValueArg(time.Now().UTC())}, args...)
	res, err := r.exec.ExecContext(ctx,
		"UPDATE authors SET quality_profile_id = ?, updated_at = ? WHERE quality_profile_id IS NULL AND "+clause, args...)
	if err != nil {
		return 0, fmt.Errorf("apply quality profile to authors without one: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("apply quality profile rows affected: %w", err)
	}
	return n, nil
}
