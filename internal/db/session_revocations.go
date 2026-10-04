package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// SessionRevocations is the logout denylist (migration 099): hashes of
// session tokens that were explicitly signed out, each kept until the token
// would have expired on its own.
//
// It is checked on every cookie authenticated request, so lookups are served
// from memory. The process is the only writer (SQLite, one connection pool),
// so the cache is loaded from the table once and then kept current by Revoke.
// Create exactly one per database and share it between the logout handler and
// the auth middleware; two instances would each miss the other's writes until
// restart.
type SessionRevocations struct {
	db *sql.DB
	// now is the clock, swappable in tests.
	now func() time.Time

	mu     sync.Mutex
	loaded bool
	// revoked maps token hash to the token's expiry.
	revoked map[string]time.Time
}

// NewSessionRevocations returns the denylist backed by database. The table is
// read lazily on first use, so a transient failure there surfaces as an error
// from that call (which the middleware turns into a 5xx) and is retried on the
// next, rather than starting the process with an empty list.
func NewSessionRevocations(database *sql.DB) *SessionRevocations {
	return &SessionRevocations{db: database, now: time.Now, revoked: map[string]time.Time{}}
}

// load reads every unexpired row into the cache; caller holds s.mu.
func (s *SessionRevocations) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT token_hash, expires_at FROM session_revocations WHERE expires_at > ?", s.now().Unix())
	if err != nil {
		return fmt.Errorf("load session revocations: %w", err)
	}
	defer rows.Close()
	m := map[string]time.Time{}
	for rows.Next() {
		var hash string
		var exp int64
		if err := rows.Scan(&hash, &exp); err != nil {
			return fmt.Errorf("scan session revocation: %w", err)
		}
		m[hash] = time.Unix(exp, 0)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load session revocations: %w", err)
	}
	s.revoked = m
	s.loaded = true
	return nil
}

// Revoke records tokenHash as signed out until expiresAt. A token that has
// already expired is not recorded: the signature check rejects it anyway.
// Expired rows are purged on the same call, which keeps the table bounded by
// the tokens signed out within one session lifetime.
func (s *SessionRevocations) Revoke(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return err
	}
	now := s.now()
	if !expiresAt.After(now) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO session_revocations (token_hash, expires_at) VALUES (?, ?)
		 ON CONFLICT(token_hash) DO UPDATE SET expires_at = excluded.expires_at`,
		tokenHash, expiresAt.Unix()); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	s.revoked[tokenHash] = expiresAt
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM session_revocations WHERE expires_at <= ?", now.Unix()); err != nil {
		// The new row is written; a failed purge only delays cleanup until
		// the next logout, so report it without failing the revocation.
		slog.Warn("purge expired session revocations", "error", err)
	}
	for h, exp := range s.revoked {
		if !exp.After(now) {
			delete(s.revoked, h)
		}
	}
	return nil
}

// IsRevoked reports whether tokenHash was signed out and has not yet expired.
func (s *SessionRevocations) IsRevoked(ctx context.Context, tokenHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return false, err
	}
	exp, ok := s.revoked[tokenHash]
	return ok && exp.After(s.now()), nil
}
