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

// FirstAdminID returns the lowest-id admin user — "the operator" for requests
// that authenticate as the install rather than as a person (local-only trust,
// API key). Returns 0 when no admin exists, which callers must treat as
// "identity unknown" rather than as a valid user id (#1725).
func (r *UserRepo) FirstAdminID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := r.db.QueryRowContext(ctx,
		"SELECT MIN(id) FROM users WHERE role='admin'").Scan(&id); err != nil {
		return 0, fmt.Errorf("first admin id: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
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

// Errors returned by Delete when the caller's plan cannot be carried out.
var (
	// ErrLastAdmin is returned when deleting a user would leave the install
	// with no admin account.
	ErrLastAdmin = errors.New("cannot delete the last admin user")
	// ErrUnknownDeleteStrategy is returned when the caller did not pick what
	// should happen to the user's rows. There is deliberately no default.
	ErrUnknownDeleteStrategy = errors.New("unknown user delete strategy")
	// ErrReassignToSelf is returned when the inheritor is the user being
	// deleted.
	ErrReassignToSelf = errors.New("cannot reassign a user's rows to that same user")
	// ErrReassignTargetMissing is returned when the chosen inheritor does not
	// exist.
	ErrReassignTargetMissing = errors.New("reassign target user does not exist")
	// ErrUserStillReferenced is returned when rows outside the tables this
	// package knows about still point at the user, so the delete was rejected
	// by a foreign key. It means a new users(id) reference was added without
	// teaching Delete about it.
	ErrUserStillReferenced = errors.New("rows still reference this user")
)

// UserDeleteStrategy says what happens to the rows a user owns when that user
// is deleted (#1899).
//
// There is no default on purpose. Nulling the owner publishes a private library
// to every other account on the install, and purging destroys it; picking
// either one silently is a decision the admin should be making, not this
// package.
type UserDeleteStrategy string

const (
	// ReassignOwnedRows hands the user's rows to another account, or makes
	// them global (owner NULL, visible to everyone) when ReassignTo is nil.
	// NULL as "shared with all users" is the meaning migration 039 established
	// and every per-user query in this package follows.
	ReassignOwnedRows UserDeleteStrategy = "reassign"
	// PurgeOwnedRows deletes the user's rows along with the user.
	PurgeOwnedRows UserDeleteStrategy = "purge"
)

// UserDeletePlan is the caller's answer to "what happens to this user's
// library". ReassignTo is only read under ReassignOwnedRows, where nil means
// global.
type UserDeletePlan struct {
	Strategy   UserDeleteStrategy
	ReassignTo *int64
}

// ownerTables are the tables whose owner_user_id names the user who owns the
// row. Migration 025 added the first six, 065 added import_lists. None of them
// declare an ON DELETE clause, and foreign keys are enforced on every
// connection (connectionPragmaDSN), so rows here reject a bare user delete
// rather than orphaning the way they did before #1727.
//
// These names are compile-time constants interpolated into SQL below; nothing
// user-supplied ever reaches that path.
var ownerTables = []string{
	"authors",
	"books",
	"quality_profiles",
	"metadata_profiles",
	"downloads",
	"root_folders",
	"import_lists",
}

// purgeOrder deletes the child side before the parent so a purge does not lean
// on cascade ordering. authors comes after books because books.author_id is
// ON DELETE CASCADE: removing an author takes every book still hanging off it,
// including one another user owns. That is inherent in removing the author, and
// it is part of why purge is a deliberate choice rather than the default.
var purgeOrder = []string{
	"downloads",
	"books",
	"authors",
	"quality_profiles",
	"metadata_profiles",
	"root_folders",
	"import_lists",
}

// perUserStateTables hold rows scoped to one account that no other account can
// act on: a recommendation is scored against one user's taste and a dismissal
// only suppresses rows in that user's feed. Migration 015 declares these
// user_id columns without a foreign key, so they never block a delete; they
// would just accumulate as rows no session can read or clear, which is the
// stranded state migration 069 had to repair. Cleared under either strategy.
var perUserStateTables = []string{
	"recommendations",
	"recommendation_dismissals",
	"recommendation_author_exclusions",
}

// UserOwnedRows counts the rows that point at a user through a foreign key to
// users(id). Any non-zero field blocks a bare DELETE, so this is what the API
// shows an admin before asking them to choose a strategy.
type UserOwnedRows struct {
	Authors          int `json:"authors"`
	Books            int `json:"books"`
	QualityProfiles  int `json:"qualityProfiles"`
	MetadataProfiles int `json:"metadataProfiles"`
	Downloads        int `json:"downloads"`
	RootFolders      int `json:"rootFolders"`
	ImportLists      int `json:"importLists"`
	// Blocklist counts rows attributing a blocklist entry to this user. It is
	// reported for transparency but never reassigned or purged; see Delete.
	Blocklist int `json:"blocklist"`
}

// Total reports how many rows in all counted tables belong to the user.
func (c UserOwnedRows) Total() int {
	return c.Authors + c.Books + c.QualityProfiles + c.MetadataProfiles +
		c.Downloads + c.RootFolders + c.ImportLists + c.Blocklist
}

// OwnedRows counts every row that references the user. A zero Total means the
// account never used the app and can be deleted under any strategy.
func (r *UserRepo) OwnedRows(ctx context.Context, id int64) (UserOwnedRows, error) {
	var c UserOwnedRows
	into := map[string]*int{
		"authors":           &c.Authors,
		"books":             &c.Books,
		"quality_profiles":  &c.QualityProfiles,
		"metadata_profiles": &c.MetadataProfiles,
		"downloads":         &c.Downloads,
		"root_folders":      &c.RootFolders,
		"import_lists":      &c.ImportLists,
	}
	for _, t := range ownerTables {
		// #nosec G201 -- t comes from ownerTables, a package-level literal
		// slice; no caller-supplied value reaches this string.
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE owner_user_id = ?", t)
		if err := r.db.QueryRowContext(ctx, q, id).Scan(into[t]); err != nil {
			return c, fmt.Errorf("count %s: %w", t, err)
		}
	}
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM blocklist WHERE created_by_user_id = ?", id,
	).Scan(&c.Blocklist); err != nil {
		return c, fmt.Errorf("count blocklist: %w", err)
	}
	return c, nil
}

// Delete removes a user and resolves what happens to the rows they own,
// according to plan.
//
// Before #1899 this was a bare DELETE, which meant any account that had ever
// added an author, added a book, started a download or created a profile could
// not be deleted at all: those tables carry a foreign key to users(id) with no
// ON DELETE clause, so SQLite's default NO ACTION rejected the statement once
// #1727 turned enforcement on per connection. In practice the delete button
// worked only on accounts nobody had used.
//
// The last-admin guard and every write run inside one transaction, so a
// concurrent mutation cannot slip between the count check and the delete, and a
// failure part way through leaves the user and their rows exactly as they were.
func (r *UserRepo) Delete(ctx context.Context, id int64, plan UserDeletePlan) error {
	switch plan.Strategy {
	case ReassignOwnedRows, PurgeOwnedRows:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownDeleteStrategy, plan.Strategy)
	}
	if plan.Strategy == ReassignOwnedRows && plan.ReassignTo != nil && *plan.ReassignTo == id {
		return ErrReassignToSelf
	}

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
		// Guard: refuse to delete the last admin. Count other admins while
		// still inside the transaction so no concurrent mutation can sneak in.
		var adminCount int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM users WHERE role='admin' AND id != ?", id,
		).Scan(&adminCount); err != nil {
			return fmt.Errorf("check admin count: %w", err)
		}
		if adminCount == 0 {
			return ErrLastAdmin
		}
	}

	if plan.Strategy == ReassignOwnedRows && plan.ReassignTo != nil {
		var n int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM users WHERE id=?", *plan.ReassignTo,
		).Scan(&n); err != nil {
			return fmt.Errorf("check reassign target: %w", err)
		}
		if n == 0 {
			return ErrReassignTargetMissing
		}
	}

	if plan.Strategy == PurgeOwnedRows {
		for _, t := range purgeOrder {
			// #nosec G201 -- t comes from purgeOrder, a package-level literal
			// slice; no caller-supplied value reaches this string.
			q := fmt.Sprintf("DELETE FROM %s WHERE owner_user_id = ?", t)
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return fmt.Errorf("purge %s: %w", t, err)
			}
		}
	} else {
		// A nil ReassignTo binds as NULL, which is this schema's "global".
		for _, t := range ownerTables {
			// #nosec G201 -- t comes from ownerTables, a package-level literal
			// slice; no caller-supplied value reaches this string.
			q := fmt.Sprintf("UPDATE %s SET owner_user_id = ? WHERE owner_user_id = ?", t)
			if _, err := tx.ExecContext(ctx, q, plan.ReassignTo, id); err != nil {
				return fmt.Errorf("reassign %s: %w", t, err)
			}
		}
	}

	// Blocklist attribution is an audit trail, not ownership. Migration 050
	// records which user promoted an entry and treats NULL as "unknown origin",
	// which is honestly what a departed user's entries are. The blocklist stays
	// global either way, so the entry itself survives both strategies and only
	// the attribution is cleared: a release that was broken for one user is
	// still broken for everyone, and purging it would make the next scan re-pay
	// the cost of a grab already known to fail.
	if _, err := tx.ExecContext(ctx,
		"UPDATE blocklist SET created_by_user_id = NULL WHERE created_by_user_id = ?", id,
	); err != nil {
		return fmt.Errorf("clear blocklist attribution: %w", err)
	}

	for _, t := range perUserStateTables {
		// #nosec G201 -- t comes from perUserStateTables, a package-level
		// literal slice; no caller-supplied value reaches this string.
		q := fmt.Sprintf("DELETE FROM %s WHERE user_id = ?", t)
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=?", id); err != nil {
		if isForeignKeyViolation(err) {
			// Everything this package knows about has been handled above, so a
			// foreign key rejection here means a new users(id) reference was
			// added without adding it to ownerTables.
			return fmt.Errorf("%w: %w", ErrUserStillReferenced, err)
		}
		return fmt.Errorf("delete user: %w", err)
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
