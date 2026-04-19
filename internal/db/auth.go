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
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// OIDC fields — nil for local-password users.
	OIDCSub     *string
	OIDCIssuer  *string
	Email       *string
	DisplayName *string
}

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

const userSelectCols = `id, username, password_hash, created_at, updated_at,
	oidc_sub, oidc_issuer, email, display_name`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt,
		&u.OIDCSub, &u.OIDCIssuer, &u.Email, &u.DisplayName,
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

// GetOrCreateByOIDC resolves or creates a user identified by (issuer, sub).
// On creation, username is derived from preferredUsername (falling back to sub),
// email and displayName are stored as provided.
func (r *UserRepo) GetOrCreateByOIDC(ctx context.Context, issuer, sub, preferredUsername, email, displayName string) (*User, error) {
	u, err := r.GetByOIDC(ctx, issuer, sub)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
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
		`INSERT INTO users (username, password_hash, created_at, updated_at, oidc_sub, oidc_issuer, email, display_name)
		 VALUES (?, '', ?, ?, ?, ?, ?, ?)`,
		username, now, now, sub, issuer, nullableStr(email), nullableStr(displayName),
	)
	if err != nil {
		return nil, fmt.Errorf("create oidc user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get oidc user id: %w", err)
	}
	slog.Info("oidc: auto-provisioned user", "username", username, "issuer", issuer)
	return r.GetByID(ctx, id)
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Create inserts a new user. Intended for the first-run setup flow; further
// additions need a separate UI path (not exposed today).
func (r *UserRepo) Create(ctx context.Context, username, passwordHash string) (*User, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		username, passwordHash, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get user id: %w", err)
	}
	return &User{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now}, nil
}

func (r *UserRepo) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET password_hash=?, updated_at=? WHERE id=?",
		passwordHash, time.Now().UTC(), id,
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
