-- +migrate Up
-- Per-media-type download category (#700). Existing rows default to empty,
-- which makes ResolveCategory fall back to category for audiobooks as well —
-- zero behaviour change until the user opts in by populating the new column.
ALTER TABLE download_clients ADD COLUMN category_audiobook TEXT NOT NULL DEFAULT '';
