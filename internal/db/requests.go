package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// RequestRepo stores requester requests (migration 089).
//
// Every method a requester's own traffic reaches takes the owner id and
// filters on it unconditionally, whether or not BINDERY_ENFORCE_TENANCY is on:
// one requester never sees, cancels or learns about another's requests. The
// admin methods (ListAll, CountPending, Claim, Complete, Release, Decline)
// are reached only through RequireAdmin routes.
type RequestRepo struct {
	db       *sql.DB
	claimTTL time.Duration
}

func NewRequestRepo(db *sql.DB) *RequestRepo {
	return &RequestRepo{db: db, claimTTL: RequestClaimTTL}
}

// WithClaimTTL shortens the claim TTL, for tests of claim renewal.
func (r *RequestRepo) WithClaimTTL(d time.Duration) *RequestRepo {
	r.claimTTL = d
	return r
}

// ClaimTTL is how long a claim holds without renewal.
func (r *RequestRepo) ClaimTTL() time.Duration { return r.claimTTL }

var (
	// ErrRequestExists is a second request for the same kind and foreign id
	// by the same owner.
	ErrRequestExists = errors.New("request already exists")
	// ErrRequestNotPending is a state change on a request that is no longer
	// pending: already approved, declined, or claimed by another approval.
	ErrRequestNotPending = errors.New("request is not pending")
	// ErrRequestCapReached is a create or reopen that would take the owner
	// past their cap of pending requests.
	ErrRequestCapReached = errors.New("pending request cap reached")
	// ErrRequestAutoApproveQuota is a claim refused because the owner has
	// already had their daily allowance of automatic approvals. The request
	// stays pending for a human.
	ErrRequestAutoApproveQuota = errors.New("daily auto-approve quota reached")
)

// RequestClaimTTL is how long an approval's claim holds without renewal
// before another approval may take the row over. A running approval renews
// its claim well inside this (see RenewClaim), so only a claim whose process
// died goes stale.
const RequestClaimTTL = 5 * time.Minute

const requestColumns = `r.id, r.owner_user_id, COALESCE(u.username, ''), r.kind, r.foreign_id, r.media_type,
	r.title, r.author_name, r.payload_json, r.status, r.decline_reason, r.decided_by,
	r.result_book_id, r.result_author_id, r.created_at, r.updated_at, r.decided_at,
	COALESCE(b.status, '')`

const requestJoins = `FROM requests r
	LEFT JOIN users u ON u.id = r.owner_user_id
	LEFT JOIN books b ON b.id = r.result_book_id`

func scanRequest(s scanner) (*models.LibraryRequest, error) {
	var (
		req                                 models.LibraryRequest
		decidedBy, resultBook, resultAuthor sql.NullInt64
		decidedAt                           sql.NullTime
	)
	if err := s.Scan(&req.ID, &req.OwnerUserID, &req.OwnerUsername, &req.Kind, &req.ForeignID, &req.MediaType,
		&req.Title, &req.AuthorName, &req.PayloadJSON, &req.Status, &req.DeclineReason, &decidedBy,
		&resultBook, &resultAuthor, &req.CreatedAt, &req.UpdatedAt, &decidedAt, &req.BookStatus); err != nil {
		return nil, err
	}
	if decidedBy.Valid {
		req.DecidedBy = &decidedBy.Int64
	}
	if resultBook.Valid {
		req.ResultBookID = &resultBook.Int64
	}
	if resultAuthor.Valid {
		req.ResultAuthorID = &resultAuthor.Int64
	}
	if decidedAt.Valid {
		t := decidedAt.Time
		req.DecidedAt = &t
	}
	return &req, nil
}

// pendingBelowCap is the predicate that keeps an owner under their cap. It
// sits inside the INSERT or UPDATE it guards, so the count and the write are
// one statement under SQLite's single writer: concurrent creates cannot all
// see room and all insert.
const pendingBelowCap = `(SELECT COUNT(*) FROM requests WHERE owner_user_id = ? AND status IN ('pending', 'approving')) < ?`

// Create inserts a pending request. A duplicate (owner, kind, foreign id)
// returns ErrRequestExists. With maxPending above zero, a create that would
// give the owner more than maxPending requests awaiting a decision inserts
// nothing and returns ErrRequestCapReached.
func (r *RequestRepo) Create(ctx context.Context, req *models.LibraryRequest, maxPending int) error {
	if maxPending <= 0 {
		maxPending = int(^uint32(0) >> 1)
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO requests (owner_user_id, kind, foreign_id, media_type, title, author_name,
		                      payload_json, status, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?
		WHERE `+pendingBelowCap,
		req.OwnerUserID, req.Kind, req.ForeignID, req.MediaType, req.Title, req.AuthorName,
		req.PayloadJSON, now, now, req.OwnerUserID, maxPending)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrRequestExists
		}
		return fmt.Errorf("create request: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("create request: %w", err)
	} else if n == 0 {
		return ErrRequestCapReached
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("create request id: %w", err)
	}
	req.ID = id
	req.Status = models.RequestStatusPending
	req.CreatedAt, req.UpdatedAt = now, now
	return nil
}

// GetForOwner returns ownerID's request for kind and foreignID, or nil.
func (r *RequestRepo) GetForOwner(ctx context.Context, ownerID int64, kind, foreignID string) (*models.LibraryRequest, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` `+requestJoins+`
		WHERE r.owner_user_id = ? AND r.kind = ? AND r.foreign_id = ?`, ownerID, kind, foreignID)
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

// GetByID returns the request with id, or nil. Admin use only: it does not
// filter by owner.
func (r *RequestRepo) GetByID(ctx context.Context, id int64) (*models.LibraryRequest, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` `+requestJoins+` WHERE r.id = ?`, id)
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

// ListByOwner returns one page of ownerID's requests, newest first, and the
// owner's total.
func (r *RequestRepo) ListByOwner(ctx context.Context, ownerID int64, limit, offset int) ([]models.LibraryRequest, int, error) {
	return r.list(ctx, "WHERE r.owner_user_id = ?", []any{ownerID}, limit, offset)
}

// ListAll returns one page of every owner's requests, newest first. status
// narrows to one status; "pending" includes rows claimed by a running
// approval. Empty lists everything. Admin use only.
func (r *RequestRepo) ListAll(ctx context.Context, status string, limit, offset int) ([]models.LibraryRequest, int, error) {
	switch status {
	case "":
		return r.list(ctx, "", nil, limit, offset)
	case models.RequestStatusPending:
		return r.list(ctx, "WHERE r.status IN ('pending', 'approving')", nil, limit, offset)
	default:
		return r.list(ctx, "WHERE r.status = ?", []any{status}, limit, offset)
	}
}

func (r *RequestRepo) list(ctx context.Context, where string, args []any, limit, offset int) ([]models.LibraryRequest, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests r "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count requests: %w", err)
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	// #nosec G202 -- requestColumns and requestJoins are package constants and where is one of three fixed literals chosen in ListByOwner or ListAll; the owner id and status are bound args
	rows, err := r.db.QueryContext(ctx, `SELECT `+requestColumns+` `+requestJoins+` `+where+`
		ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()
	out := []models.LibraryRequest{}
	var authorIDs []int64
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		if req.Kind == models.RequestKindAuthor && req.ResultAuthorID != nil {
			authorIDs = append(authorIDs, *req.ResultAuthorID)
		}
		out = append(out, *req)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.fillAuthorProgress(ctx, out, authorIDs); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// fillAuthorProgress sets AuthorBooks and AuthorBooksImported on the author
// requests in page with one grouped query for the whole page (plan item P6),
// never one query per row.
func (r *RequestRepo) fillAuthorProgress(ctx context.Context, page []models.LibraryRequest, authorIDs []int64) error {
	if len(authorIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(authorIDs)), ",")
	args := make([]any, len(authorIDs))
	for i, id := range authorIDs {
		args[i] = id
	}
	// #nosec G202 -- placeholders is a run of "?," built from a count, never input.
	rows, err := r.db.QueryContext(ctx, `
		SELECT author_id, COUNT(*), COALESCE(SUM(CASE WHEN status = 'imported' THEN 1 ELSE 0 END), 0)
		FROM books WHERE excluded = 0 AND author_id IN (`+placeholders+`)
		GROUP BY author_id`, args...)
	if err != nil {
		return fmt.Errorf("request author progress: %w", err)
	}
	defer rows.Close()
	type progress struct{ total, imported int }
	byAuthor := make(map[int64]progress, len(authorIDs))
	for rows.Next() {
		var id int64
		var p progress
		if err := rows.Scan(&id, &p.total, &p.imported); err != nil {
			return err
		}
		byAuthor[id] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range page {
		if page[i].Kind != models.RequestKindAuthor || page[i].ResultAuthorID == nil {
			continue
		}
		p := byAuthor[*page[i].ResultAuthorID]
		page[i].AuthorBooks, page[i].AuthorBooksImported = p.total, p.imported
	}
	return nil
}

// CountPendingByOwner counts ownerID's requests awaiting a decision.
func (r *RequestRepo) CountPendingByOwner(ctx context.Context, ownerID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE owner_user_id = ? AND status IN ('pending', 'approving')", ownerID).Scan(&n)
	return n, err
}

// CountPending counts every request awaiting a decision. Admin use only.
func (r *RequestRepo) CountPending(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE status IN ('pending', 'approving')").Scan(&n)
	return n, err
}

// Claim takes a pending request for approval by adminID. It is a compare and
// swap on status: of two approvals racing for one row, exactly one gets the
// row back and the other gets ErrRequestNotPending. A claim not renewed within
// the claim TTL is abandoned and can be retaken.
//
// The returned request carries a random ClaimToken. RenewClaim, Complete and
// Release act only while the row still holds that token, so an approval whose
// claim was retaken cannot complete or release another approval's claim.
func (r *RequestRepo) Claim(ctx context.Context, id, adminID int64) (*models.LibraryRequest, error) {
	return r.claimPending(ctx, id, adminID, "", nil)
}

// ClaimAutoApprove claims a pending request for an automatic approval by
// ownerID, but only while the owner is under their daily auto-approve quota.
// The quota counts the owner's automatic approvals — the rows with decided_by
// NULL — at or after since, and it is enforced in the same statement as the
// claim, the way pendingBelowCap guards Create, so a burst of creates cannot
// all pass a separate count. Returns ErrRequestAutoApproveQuota when the quota
// is reached, leaving the request pending for a human.
func (r *RequestRepo) ClaimAutoApprove(ctx context.Context, id, ownerID int64, since time.Time, quota int) (*models.LibraryRequest, error) {
	req, err := r.claimPending(ctx, id, 0,
		`owner_user_id = ? AND (SELECT COUNT(*) FROM requests
			WHERE owner_user_id = ? AND status = 'approved' AND decided_by IS NULL AND decided_at >= ?) < ?`,
		[]any{ownerID, ownerID, since, quota})
	if errors.Is(err, ErrRequestNotPending) {
		// Tell "no longer pending" apart from "at the quota", so the caller
		// leaves the latter queued instead of reporting a lost claim.
		if n, cerr := r.countAutoApprovedSince(ctx, ownerID, since); cerr == nil && n >= quota {
			return nil, ErrRequestAutoApproveQuota
		}
	}
	return req, err
}

// countAutoApprovedSince counts ownerID's requests that approved themselves at
// or after since. decided_by is NULL only on the automatic path (an admin's
// Claim stamps their id), so this is the auto-approve tally.
func (r *RequestRepo) countAutoApprovedSince(ctx context.Context, ownerID int64, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM requests
		WHERE owner_user_id = ? AND status = 'approved' AND decided_by IS NULL AND decided_at >= ?`,
		ownerID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count auto approved requests: %w", err)
	}
	return n, nil
}

// claimPending runs the compare-and-swap claim. When extraWhere is set it is
// appended to the WHERE clause with extraArgs bound after the fixed ones; it is
// only ever a package constant predicate.
func (r *RequestRepo) claimPending(ctx context.Context, id, adminID int64, extraWhere string, extraArgs []any) (*models.LibraryRequest, error) {
	now := time.Now().UTC()
	stale := now.Add(-r.claimTTL).UnixMilli()
	token, err := claimToken()
	if err != nil {
		return nil, err
	}
	query := `
		UPDATE requests SET status = 'approving', claimed_at = ?, claim_token = ?, decided_by = ?, updated_at = ?
		WHERE id = ? AND (status = 'pending' OR (status = 'approving' AND COALESCE(claimed_at, 0) < ?))`
	args := []any{now.UnixMilli(), token, nullableID(adminID), now, id, stale}
	if extraWhere != "" {
		// #nosec G202 -- extraWhere is a package constant predicate, never caller input; its values are bound args.
		query += " AND " + extraWhere
		args = append(args, extraArgs...)
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("claim request: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrRequestNotPending
	}
	req, err := r.GetByID(ctx, id)
	if err != nil || req == nil {
		return nil, fmt.Errorf("reload claimed request: %w", err)
	}
	req.ClaimToken = token
	return req, nil
}

func claimToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("claim token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// RenewClaim restamps a claim this approval still holds, so a long running
// approval never goes stale. ErrRequestNotPending when the claim was lost.
func (r *RequestRepo) RenewClaim(ctx context.Context, id int64, token string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET claimed_at = ?
		WHERE id = ? AND status = 'approving' AND claim_token = ?`, time.Now().UTC().UnixMilli(), id, token)
	if err != nil {
		return fmt.Errorf("renew claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// Complete marks a request this approval still holds approved with what the
// approval created.
func (r *RequestRepo) Complete(ctx context.Context, id int64, token string, bookID, authorID *int64) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'approved', result_book_id = ?, result_author_id = ?,
		       decided_at = ?, updated_at = ?, claimed_at = NULL, claim_token = NULL
		WHERE id = ? AND status = 'approving' AND claim_token = ?`, bookID, authorID, now, now, id, token)
	if err != nil {
		return fmt.Errorf("complete request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// Release returns a request this approval still holds to pending after the
// approval failed.
func (r *RequestRepo) Release(ctx context.Context, id int64, token string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'pending', decided_by = NULL, claimed_at = NULL, claim_token = NULL, updated_at = ?
		WHERE id = ? AND status = 'approving' AND claim_token = ?`, time.Now().UTC(), id, token)
	return err
}

// Decline declines a pending request. Compare and swap on status, like Claim.
func (r *RequestRepo) Decline(ctx context.Context, id, adminID int64, reason string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'declined', decline_reason = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE id = ? AND status = 'pending'`, reason, nullableID(adminID), now, now, id)
	if err != nil {
		return fmt.Errorf("decline request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

// Reopen puts ownerID's approved request back to pending, for when what it
// added has since left the library and the requester asks again. It keeps
// the row, so the (owner, kind, foreign id) uniqueness still holds. The cap
// check is part of the same UPDATE, as in Create.
func (r *RequestRepo) Reopen(ctx context.Context, id, ownerID int64, mediaType, payload string, maxPending int) error {
	if maxPending <= 0 {
		maxPending = int(^uint32(0) >> 1)
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE requests SET status = 'pending', media_type = ?, payload_json = ?, decided_by = NULL,
		       decided_at = NULL, result_book_id = NULL, result_author_id = NULL,
		       decline_reason = '', created_at = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND status = 'approved' AND `+pendingBelowCap,
		mediaType, payload, now, now, id, ownerID, ownerID, maxPending)
	if err != nil {
		return fmt.Errorf("reopen request: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var status string
	if err := r.db.QueryRowContext(ctx, "SELECT status FROM requests WHERE id = ? AND owner_user_id = ?", id, ownerID).Scan(&status); err == nil && status == models.RequestStatusApproved {
		return ErrRequestCapReached
	}
	return ErrRequestNotPending
}

// DeletePendingForOwner withdraws ownerID's own pending request. Returns
// ErrRequestNotPending when there is no such pending request for that owner,
// which covers another owner's id without saying it exists.
func (r *RequestRepo) DeletePendingForOwner(ctx context.Context, id, ownerID int64) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM requests WHERE id = ? AND owner_user_id = ? AND status = 'pending'", id, ownerID)
	if err != nil {
		return fmt.Errorf("withdraw request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRequestNotPending
	}
	return nil
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
