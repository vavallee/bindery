package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// cachedKeys are the settings the auth middleware reads on every request
// (mode, API key, session secrets). Only these are cached: they change rarely
// and are read constantly. Other auth.* keys (OIDC providers and the like) and
// per-user state (epoch, role, revocation) stay uncached, so a password change
// or logout takes effect on the very next request. The strings match the
// api.SettingAuth* constants; db cannot import api.
var cachedKeys = map[string]struct{}{
	"auth.mode":                    {},
	"auth.api_key":                 {}, //nolint:gosec // setting key name, not a credential
	"auth.session_secret":          {}, //nolint:gosec // setting key name, not a credential
	"auth.session_secret_previous": {}, //nolint:gosec // setting key name, not a credential
}

// SettingsRepo reads and writes the settings table. The keys in cachedKeys are served
// from a write-through in-process cache; every writer in this type
// invalidates it. Bindery runs as one process, so nothing else writes these
// rows behind the repo's back.
type SettingsRepo struct {
	db *sql.DB

	mu    sync.RWMutex
	cache map[string]*models.Setting // nil value means the key is absent
	gen   uint64                     // bumped on every invalidation
}

func NewSettingsRepo(db *sql.DB) *SettingsRepo {
	return &SettingsRepo{db: db, cache: make(map[string]*models.Setting)}
}

func cacheable(key string) bool {
	_, ok := cachedKeys[key]
	return ok
}

// invalidate drops cached entries for keys. Called after the write has been
// issued, whether or not it succeeded, so a failed write can never leave a
// stale entry behind.
func (r *SettingsRepo) invalidate(keys ...string) {
	r.mu.Lock()
	r.gen++
	for _, k := range keys {
		delete(r.cache, k)
	}
	r.mu.Unlock()
}

func (r *SettingsRepo) Get(ctx context.Context, key string) (*models.Setting, error) {
	if !cacheable(key) {
		return r.query(ctx, key)
	}
	r.mu.RLock()
	s, ok := r.cache[key]
	gen := r.gen
	r.mu.RUnlock()
	if ok {
		return copySetting(s), nil
	}
	s, err := r.query(ctx, key)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	// A write that landed while we were reading bumped gen; skip the fill so
	// we never cache a value the write already replaced.
	if r.gen == gen {
		r.cache[key] = copySetting(s)
	}
	r.mu.Unlock()
	return s, nil
}

func copySetting(s *models.Setting) *models.Setting {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func (r *SettingsRepo) query(ctx context.Context, key string) (*models.Setting, error) {
	var s models.Setting
	err := r.db.QueryRowContext(ctx, "SELECT key, value, updated_at FROM settings WHERE key=?", key).
		Scan(&s.Key, &s.Value, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get setting %s: %w", key, err)
	}
	return &s, nil
}

func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	defer r.invalidate(key)
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, now)
	return err
}

// SetIfAbsent atomically inserts a settings row only when the key does not
// already exist, and reports whether the insert won (true) or lost the race
// (false). Used as a one-shot lock for irreversible decisions like the OIDC
// promote-first-admin guard: two concurrent first-time logins both see no
// admins, both want to claim admin, but only one wins SetIfAbsent and the
// other falls back to the default role.
func (r *SettingsRepo) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	defer r.invalidate(key)
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO NOTHING`,
		key, value, now)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// SettingKV is a single key/value pair for SetMany.
type SettingKV struct {
	Key   string
	Value string
}

// SetMany writes several settings rows atomically inside a single transaction.
// Either every key is persisted or, on any error, none of them are: the
// transaction is rolled back so callers never observe a half-applied config.
func (r *SettingsRepo) SetMany(ctx context.Context, kvs []SettingKV) error {
	if len(kvs) == 0 {
		return nil
	}
	keys := make([]string, len(kvs))
	for i, kv := range kvs {
		keys[i] = kv.Key
	}
	defer r.invalidate(keys...)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, kv := range kvs {
		if _, err := stmt.ExecContext(ctx, kv.Key, kv.Value, now); err != nil {
			return fmt.Errorf("set setting %s: %w", kv.Key, err)
		}
	}
	return tx.Commit()
}

func (r *SettingsRepo) List(ctx context.Context) ([]models.Setting, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT key, value, updated_at FROM settings ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var settings []models.Setting
	for rows.Next() {
		var s models.Setting
		if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt); err != nil {
			return nil, err
		}
		settings = append(settings, s)
	}
	return settings, rows.Err()
}

func (r *SettingsRepo) Delete(ctx context.Context, key string) error {
	defer r.invalidate(key)
	_, err := r.db.ExecContext(ctx, "DELETE FROM settings WHERE key=?", key)
	return err
}
