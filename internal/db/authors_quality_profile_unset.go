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
	// AllOwners covers every author whatever its owner. Only for a caller
	// with no user identity (API key, local only and disabled auth), which
	// the rest of Bindery already treats as admin equivalent and unscoped.
	AllOwners bool
	// OwnerUserID covers the authors this account owns.
	OwnerUserID int64
	// IncludeUnowned also covers authors with no owner, the shared rows that
	// imports and pre tenancy installs create. Set only when the caller may
	// write those rows one at a time (auth.CheckWriteOwnership with owner 0).
	IncludeUnowned bool
}

// where returns the owner clause and its args. Another account's authors are
// never in scope unless AllOwners is set.
func (s AuthorOwnerScope) where() (string, []any) {
	switch {
	case s.AllOwners:
		return "1 = 1", nil
	case s.IncludeUnowned:
		return "(owner_user_id = ? OR owner_user_id IS NULL)", []any{s.OwnerUserID}
	default:
		return "owner_user_id = ?", []any{s.OwnerUserID}
	}
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
// updated_at moves with the change so an Edit Author dialog opened before the
// apply gets the usual stale write refusal instead of saving its old view of
// the author over this one.
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
