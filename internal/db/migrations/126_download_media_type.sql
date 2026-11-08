-- +migrate Up
-- The format slot a grab was made for, stamped when the grab is created so the
-- import path reads it instead of re-deriving it from the release (#1964).
-- Empty means unknown: rows from before this migration, manual imports, and
-- grabs whose format was never known. Existing rows are deliberately NOT
-- backfilled, a guess here would be the same inference the column replaces,
-- so they keep resolving through the old fallback chain.
ALTER TABLE downloads ADD COLUMN media_type TEXT NOT NULL DEFAULT '';
