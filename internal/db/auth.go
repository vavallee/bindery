package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := r.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username=?",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	err := r.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE id=?",
		id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
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
