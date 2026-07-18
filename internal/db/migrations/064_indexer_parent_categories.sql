-- +migrate Up
-- Opt in per indexer to searching broad Books (7000) and Audio (3000)
-- categories. Existing and newly synced rows retain the safer child-only
-- behavior through the false default.
ALTER TABLE indexers ADD COLUMN include_parent_categories INTEGER NOT NULL DEFAULT 0;
