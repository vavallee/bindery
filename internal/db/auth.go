package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string // "admin" or "user"
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// SessionEpoch is bumped every time the user's credentials change
	// (password self-change, admin password reset). The signed session cookie
	// carries the epoch under which it was minted; the auth middleware
	// compares it against this column on every request and rejects mismatched
	// cookies. That is how "log everyone out after a password change" is
	// enforced (Wave 1 / Bundle C audit finding).
	SessionEpoch int64
	// OIDC fields — nil for local-password users.
	OIDCSub     *string
	OIDCIssuer  *string
	Email       *string
	DisplayName *string
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

type UserRepo struct {
	db *sql.DB
}

func NewUserRepo(db *sql.DB) *UserRepo { return &UserRepo{db: db} }

// Count returns the number of users. Zero means first-run / setup required.
func (r *UserRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

const userSelectCols = `id, username, password_hash, role, created_at, updated_at,
	oidc_sub, oidc_issuer, email, display_name, session_epoch`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt,
		&u.OIDCSub, &u.OIDCIssuer, &u.Email, &u.DisplayName, &u.SessionEpoch,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx,
		"SELECT "+userSelectCols+" FROM users WHERE username=?", username))
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx,
		"SELECT "+userSelectCols+" FROM users WHERE id=?", id))
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetByOIDC looks up a user by the composite (issuer, sub) identity.
// Returns nil, nil when not found.
func (r *UserRepo) GetByOIDC(ctx context.Context, issuer, sub string) (*User, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx,
		"SELECT "+userSelectCols+" FROM users WHERE oidc_issuer=? AND oidc_sub=?", issuer, sub))
	if err != nil {
		return nil, fmt.Errorf("get user by oidc: %w", err)
	}
	return u, nil
}

// GetByEmail looks up a user by email address. Returns nil, nil when not found
// or when email is empty.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
	if email == "" {
		return nil, nil
	}
	u, err := scanUser(r.db.QueryRowContext(ctx,
		"SELECT "+userSelectCols+" FROM users WHERE email=?", email))
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// LinkOIDCSubject sets the oidc_issuer and oidc_sub fields on an existing user,
// effectively binding an OIDC identity to a local account.
func (r *UserRepo) LinkOIDCSubject(ctx context.Context, userID int64, issuer, sub string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET oidc_issuer=?, oidc_sub=?, updated_at=? WHERE id=?",
		issuer, sub, time.Now().UTC(), userID,
	)
	return err
}

// GetOrCreateByOIDC resolves or creates a user identified by (issuer, sub).
// On creation, username is derived from preferredUsername (falling back to sub),
// email and displayName are stored as provided, and the user is assigned the
// given role. role must be "admin" or "user"; any other value is coerced to
// "user" so a bad caller can never silently grant admin.
func (r *UserRepo) GetOrCreateByOIDC(ctx context.Context, issuer, sub, preferredUsername, email, displayName, role string) (*User, error) {
	u, err := r.GetByOIDC(ctx, issuer, sub)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	if role != "admin" && role != "user" {
		role = "user"
	}
	username := preferredUsername
	if username == "" {
		username = sub
	}
	// Ensure username is unique by appending a suffix if needed.
	base := username
	for i := 1; ; i++ {
		existing, err := r.GetByUsername(ctx, username)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			break
		}
		username = fmt.Sprintf("%s_%d", base, i)
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, updated_at, oidc_sub, oidc_issuer, email, display_name)
		 VALUES (?, '', ?, ?, ?, ?, ?, ?, ?)`,
		username, role, now, now, sub, issuer, nullableStr(email), nullableStr(displayName),
	)
	if err != nil {
		return nil, fmt.Errorf("create oidc user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get oidc user id: %w", err)
	}
	slog.Info("oidc: auto-provisioned user", "username", username, "issuer", issuer, "role", role)
	return r.GetByID(ctx, id)
}

// CountAdmins returns the number of users with role "admin". Used by the OIDC
// callback to detect the lockout trap (zero admins) at provision time.
func (r *UserRepo) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&n)
	return n, err
}

// SetRoleUnguarded sets a user's role without the last-admin demotion guard.
// It is used by the OIDC group-claim sync path, where the IdP is authoritative:
// demoting an OIDC user because they lost the admin group must not be blocked
// by the "cannot demote the last admin" rule (that rule protects against
// accidental lockout via the manual API, not against deliberate IdP-driven
// role changes). role must be "admin" or "user".
func (r *UserRepo) SetRoleUnguarded(ctx context.Context, id int64, role string) error {
	if role != "admin" && role != "user" {
		return fmt.Errorf("invalid role %q: must be admin or user", role)
	}
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET role=?, updated_at=? WHERE id=?", role, time.Now().UTC(), id)
	return err
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Create inserts a new user with role "user". The first user in the DB is
// promoted to "admin" by PromoteFirstUser (called during first-run setup).
func (r *UserRepo) Create(ctx context.Context, username, passwordHash string) (*User, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, 'user', ?, ?)",
		username, passwordHash, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get user id: %w", err)
	}
	// SessionEpoch matches the column default in migration 047. Returning the
	// concrete value (rather than the Go zero) keeps the in-memory User
	// consistent with the row that was just written, so callers comparing
	// against GetSessionEpoch don't see a phantom mismatch.
	return &User{ID: id, Username: username, PasswordHash: passwordHash, Role: "user", CreatedAt: now, UpdatedAt: now, SessionEpoch: 1}, nil
}

// List returns all users ordered by id.
func (r *UserRepo) List(ctx context.Context) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+userSelectCols+" FROM users ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// Delete removes a user by id. Returns an error if trying to delete the last admin.
//
// The last-admin guard (COUNT check + DELETE) runs inside a single transaction
// to prevent a TOCTOU race where two concurrent callers both pass the count
// check and both proceed, leaving zero admins.
func (r *UserRepo) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Check whether the target is an admin.
	var targetRole string
	if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", id).Scan(&targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // already gone
		}
		return fmt.Errorf("get user role: %w", err)
	}

	if targetRole == "admin" {
		// Guard: refuse to delete the last admin — count other admins while
		// still inside the transaction so no concurrent mutation can sneak in.
		var adminCount int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM users WHERE role='admin' AND id != ?", id,
		).Scan(&adminCount); err != nil {
			return fmt.Errorf("check admin count: %w", err)
		}
		if adminCount == 0 {
			return fmt.Errorf("cannot delete the last admin user")
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetRole changes a user's role to "admin" or "user".
//
// When demoting an admin to "user", the last-admin guard (COUNT check + UPDATE)
// runs inside a single transaction to prevent a TOCTOU race.
func (r *UserRepo) SetRole(ctx context.Context, id int64, role string) error {
	if role != "admin" && role != "user" {
		return fmt.Errorf("invalid role %q: must be admin or user", role)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Guard: refuse to demote the last admin.
	if role == "user" {
		var targetRole string
		if err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", id).Scan(&targetRole); err != nil {
			return fmt.Errorf("get user role: %w", err)
		}
		if targetRole == "admin" {
			var adminCount int
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM users WHERE role='admin' AND id != ?", id,
			).Scan(&adminCount); err != nil {
				return fmt.Errorf("check admin count: %w", err)
			}
			if adminCount == 0 {
				return fmt.Errorf("cannot demote the last admin user")
			}
		}
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE users SET role=?, updated_at=? WHERE id=?", role, time.Now().UTC(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// PromoteFirstUser sets role='admin' on the user with the lowest id, if any.
// Called during first-run setup after the first user is created.
func (r *UserRepo) PromoteFirstUser(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET role='admin' WHERE id = (SELECT MIN(id) FROM users)")
	return err
}

// UpdatePassword writes a new password hash AND atomically increments the
// user's session_epoch. The epoch bump is what makes a password change
// invalidate every existing session cookie for that user: the auth middleware
// compares the cookie's epoch field against this column on each request and
// rejects mismatches. Doing both writes in one UPDATE keeps the two states
// in lockstep — there is no window in which the new password is live but
// the old cookies are still trusted (Wave 1 / Bundle C audit finding).
func (r *UserRepo) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET password_hash=?, session_epoch=session_epoch+1, updated_at=? WHERE id=?",
		passwordHash, time.Now().UTC(), id,
	)
	return err
}

// GetSessionEpoch returns the user's current session_epoch, or (0, nil) when
// the user does not exist. The auth middleware calls this on every
// session-cookie-authenticated request to compare against the epoch embedded
// in the cookie payload — a mismatch means the cookie was minted before the
// most recent password change and must be rejected.
func (r *UserRepo) GetSessionEpoch(ctx context.Context, id int64) (int64, error) {
	var epoch int64
	err := r.db.QueryRowContext(ctx,
		"SELECT session_epoch FROM users WHERE id=?", id,
	).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get session epoch: %w", err)
	}
	return epoch, nil
}

// BumpSessionEpoch increments the user's session_epoch by one. Intended for
// callers that need to invalidate every outstanding session for a user
// without changing the password itself (a hook for future "log out all
// devices" UI). The password-change paths inline the bump into UpdatePassword
// so the two cannot drift apart.
func (r *UserRepo) BumpSessionEpoch(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET session_epoch=session_epoch+1, updated_at=? WHERE id=?",
		time.Now().UTC(), id,
	)
	return err
}

func (r *UserRepo) UpdateUsername(ctx context.Context, id int64, username string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET username=?, updated_at=? WHERE id=?",
		username, time.Now().UTC(), id,
	)
	return err
}

// GetOrCreateByUsername returns the existing user with the given username, or
// creates one (with an empty password hash — proxy-auth users never log in
// with a local password). Used by the proxy-auth auto-provisioning path.
func (r *UserRepo) GetOrCreateByUsername(ctx context.Context, username string) (*User, error) {
	u, err := r.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	return r.Create(ctx, username, "")
}
