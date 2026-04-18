-- +migrate Up
-- When the metadata source does not report a language for a book (OpenLibrary
-- often omits language at the work level), 'pass' imports the book anyway and
-- 'fail' skips it. Default 'pass' preserves pre-#232 behavior.
ALTER TABLE metadata_profiles ADD COLUMN unknown_language_behavior TEXT NOT NULL DEFAULT 'pass';

-- +migrate Down
-- SQLite does not support DROP COLUMN in older versions; migration is non-reversible.
