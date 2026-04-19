-- +migrate Up

-- Add role column to users. First created user is promoted to admin in application
-- code (see db/auth.go PromoteFirstUser). OIDC-provisioned users default to 'user'.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user';

-- Add owner_user_id to each user-owned table.
ALTER TABLE authors          ADD COLUMN owner_user_id INTEGER REFERENCES users(id);
ALTER TABLE books             ADD COLUMN owner_user_id INTEGER REFERENCES users(id);
ALTER TABLE quality_profiles  ADD COLUMN owner_user_id INTEGER REFERENCES users(id);
ALTER TABLE metadata_profiles ADD COLUMN owner_user_id INTEGER REFERENCES users(id);
ALTER TABLE downloads         ADD COLUMN owner_user_id INTEGER REFERENCES users(id);
ALTER TABLE root_folders      ADD COLUMN owner_user_id INTEGER REFERENCES users(id);

-- Backfill: assign all existing rows to user id=1. If there are no users at all
-- (fresh install before first-run setup) this UPDATE is a no-op.
UPDATE authors          SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;
UPDATE books             SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;
UPDATE quality_profiles  SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;
UPDATE metadata_profiles SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;
UPDATE downloads         SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;
UPDATE root_folders      SET owner_user_id = 1 WHERE (SELECT COUNT(*) FROM users WHERE id = 1) > 0;

-- Promote user id=1 to admin (first registered account is always the admin).
UPDATE users SET role = 'admin' WHERE id = 1;

-- Composite indexes for efficient per-user queries.
CREATE INDEX idx_authors_owner          ON authors          (owner_user_id);
CREATE INDEX idx_books_owner            ON books             (owner_user_id);
CREATE INDEX idx_quality_profiles_owner ON quality_profiles  (owner_user_id);
CREATE INDEX idx_metadata_profiles_owner ON metadata_profiles (owner_user_id);
CREATE INDEX idx_downloads_owner        ON downloads         (owner_user_id);
CREATE INDEX idx_root_folders_owner     ON root_folders      (owner_user_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_root_folders_owner;
DROP INDEX IF EXISTS idx_downloads_owner;
DROP INDEX IF EXISTS idx_metadata_profiles_owner;
DROP INDEX IF EXISTS idx_quality_profiles_owner;
DROP INDEX IF EXISTS idx_books_owner;
DROP INDEX IF EXISTS idx_authors_owner;
-- SQLite does not support DROP COLUMN in older versions; use schema rebuild if needed.
-- The down migration is best-effort for development rollbacks only.
