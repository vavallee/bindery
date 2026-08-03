-- +migrate Up
ALTER TABLE series ADD COLUMN genre_override TEXT;

-- +migrate Down
-- SQLite does not support DROP COLUMN in older versions; migration is non-reversible.
