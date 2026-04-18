-- +migrate Up
ALTER TABLE series ADD COLUMN monitored INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
-- SQLite does not support DROP COLUMN in older versions; migration is non-reversible.
