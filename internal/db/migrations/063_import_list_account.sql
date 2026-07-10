-- +migrate Up
-- Per-account Hardcover reading lists (#1489). Hardcover's built-in shelves
-- share one slug per account ("want-to-read", ...), so two accounts' lists
-- were indistinguishable once saved: the picker matched local rows by slug
-- alone and treated the second account's shelf as the already-added first
-- one. account stores the Hardcover username the list was loaded from, so
-- list identity is (slug, account). Empty for legacy rows and non-Hardcover
-- list types.
ALTER TABLE import_lists ADD COLUMN account TEXT NOT NULL DEFAULT '';
